package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

type checkoutTransport struct {
	requests int
	bodies   []string
	status   int
	keys     []string
}

func (t *checkoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests++
	t.keys = append(t.keys, r.Header.Get("Idempotency-Key"))
	requestBody, _ := io.ReadAll(r.Body)
	t.bodies = append(t.bodies, string(requestBody))
	body := `{"id":"cs_test_1","url":"https://checkout.test/cs_test_1"}`
	if strings.HasSuffix(r.URL.Path, "/customers") {
		body = `{"id":"cus_test_1"}`
	}
	status := t.status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
func signedStripeEvent(body, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
func TestStripeTestNanoPurchaseAndWebhookActivatesOnce(t *testing.T) {
	s, h := testServer(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.cfg.BillingPurchasesEnabled = true
	s.cfg.BillingProvider = "stripe"
	s.cfg.BillingEnvironment = "test"
	s.cfg.BillingRateCard = "production-nano-v1"
	s.cfg.StripeTestSecret = "sk_test_example"
	s.cfg.StripeWebhookSecret = "whsec_example"
	transport := &checkoutTransport{}
	provider, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	provider.Client = &http.Client{Transport: transport}
	provider.BaseURL = "https://stripe.test"
	s.purchaseProvider, s.paymentWebhook = provider, provider
	req, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/purchases/nano", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer tph_s_test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "checkout.test") {
		t.Fatalf("checkout status=%d body=%s", resp.StatusCode, body)
	}
	var created struct {
		PurchaseID string `json:"purchase_id"`
	}
	if json.Unmarshal(body, &created) != nil || created.PurchaseID == "" {
		t.Fatalf("purchase response=%s", body)
	}
	wantKey := "tpahub:purchase:" + created.PurchaseID + ":stripe:test:checkout:v2"
	if len(transport.keys) != 2 || transport.keys[1] != wantKey || !strings.Contains(transport.bodies[1], "metadata%5Btpahub_purchase_id%5D="+created.PurchaseID) || strings.Contains(transport.bodies[1], "commerce_product_id") {
		t.Fatalf("v2 checkout compatibility keys=%v body=%s", transport.keys, transport.bodies[1])
	}
	purchase, _ := s.db.Purchase(context.Background(), created.PurchaseID)
	if purchase.Status != "pending" {
		t.Fatalf("status=%s", purchase.Status)
	}
	// Simulate inherited Standard limits at webhook time. Activation must use
	// the Nano purchase snapshot, not whatever product grants currently exist.
	until := now.AddDate(1, 0, 0)
	for _, grant := range []commerce.Grant{
		{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryPublish, OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), ValidUntil: &until, Period: commerce.PeriodMonthly, Limits: commerce.Limits{StorageBytes: 500_000_000}, Source: "legacy", CreatedAt: now, UpdatedAt: now},
		{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryDownload, OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), ValidUntil: &until, Period: commerce.PeriodMonthly, Limits: commerce.Limits{EgressBytes: 20_000_000_000}, Source: "legacy", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.db.PutGrant(context.Background(), grant); err != nil {
			t.Fatal(err)
		}
	}
	event := fmt.Sprintf(`{"id":"evt_test_1","type":"checkout.session.completed","data":{"object":{"id":"cs_test_1","payment_status":"paid","amount_total":500,"currency":"eur","metadata":{"tpahub_purchase_id":"%s"}}}}`, created.PurchaseID)
	web, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/providers/stripe/webhook", strings.NewReader(event))
	web.Header.Set("Stripe-Signature", signedStripeEvent(event, "whsec_example", now))
	wr, err := http.DefaultClient.Do(web)
	if err != nil {
		t.Fatal(err)
	}
	wb, _ := io.ReadAll(wr.Body)
	wr.Body.Close()
	if wr.StatusCode != http.StatusOK {
		t.Fatalf("webhook status=%d body=%s", wr.StatusCode, wb)
	}
	purchase, _ = s.db.Purchase(context.Background(), created.PurchaseID)
	if purchase.Status != "paid" {
		t.Fatalf("paid status=%s", purchase.Status)
	}
	ent, _ := s.db.Entitlement(context.Background(), "a")
	if ent.Source != "purchase" || ent.PurchaseID != created.PurchaseID || ent.ValidFrom != "2026-09-15T00:00:00Z" || ent.ValidUntil != "2027-09-15T00:00:00Z" {
		t.Fatalf("entitlement=%+v", ent)
	}
	active, activeErr := ent.ActiveAt(now)
	if activeErr != nil || !active {
		t.Fatalf("paid entitlement active=%v err=%v", active, activeErr)
	}
	grant, grantErr := s.db.GetGrant(context.Background(), "a", commerce.ProductTPAHub, CapabilityRepositoryPublish, now)
	if grantErr != nil || grant.Source != "purchase" || grant.PurchaseID != created.PurchaseID || grant.ValidUntil == nil || grant.OfferID != "nano" || grant.Period != commerce.PeriodAnnual || grant.Limits.StorageBytes != NanoStorageBytes {
		t.Fatalf("activated publish grant=%+v err=%v", grant, grantErr)
	}
	downloadGrant, downloadErr := s.db.GetGrant(context.Background(), "a", commerce.ProductTPAHub, CapabilityRepositoryDownload, now)
	if downloadErr != nil || downloadGrant.OfferID != "nano" || downloadGrant.Period != commerce.PeriodAnnual || downloadGrant.Limits.EgressBytes != NanoEgressBytes {
		t.Fatalf("activated download grant=%+v err=%v", downloadGrant, downloadErr)
	}
	grantBeforeDuplicate := grant
	entBeforeDuplicate := ent
	web2, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/providers/stripe/webhook", strings.NewReader(event))
	web2.Header.Set("Stripe-Signature", signedStripeEvent(event, "whsec_example", now))
	wr, err = http.DefaultClient.Do(web2)
	if err != nil {
		t.Fatal(err)
	}
	wr.Body.Close()
	if wr.StatusCode != http.StatusOK {
		t.Fatalf("duplicate status=%d", wr.StatusCode)
	}
	entAfterDuplicate, _ := s.db.Entitlement(context.Background(), "a")
	if !reflect.DeepEqual(entBeforeDuplicate, entAfterDuplicate) {
		t.Fatalf("duplicate webhook mutated entitlement: before=%+v after=%+v", entBeforeDuplicate, entAfterDuplicate)
	}
	grantAfterDuplicate, _ := s.db.GetGrant(context.Background(), "a", commerce.ProductTPAHub, CapabilityRepositoryPublish, now)
	if !reflect.DeepEqual(grantBeforeDuplicate, grantAfterDuplicate) {
		t.Fatalf("duplicate webhook mutated grant: before=%+v after=%+v", grantBeforeDuplicate, grantAfterDuplicate)
	}
}
func TestStripeWebhookSignatureVerifier(t *testing.T) {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	body := `{"id":"evt_1"}`
	if err := verifyStripeSignature(signedStripeEvent(body, "whsec_example", at), []byte(body), "whsec_example", at); err != nil {
		t.Fatal(err)
	}
	if err := verifyStripeSignature("", []byte(body), "whsec_example", at); err == nil {
		t.Fatal("missing signature accepted")
	}
}

func TestWebsitePurchaseBridgeUsesSessionAndRedirects(t *testing.T) {
	s, h := testServer(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.cfg.BillingPurchasesEnabled = true
	s.cfg.BillingProvider = "stripe"
	s.cfg.BillingEnvironment = "test"
	s.cfg.BillingRateCard = "production-nano-v1"
	s.cfg.StripeTestSecret = "sk_test_example"
	s.cfg.StripeWebhookSecret = "whsec_example"
	provider, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	provider.BaseURL = "https://stripe.test"
	provider.Client = &http.Client{Transport: &checkoutTransport{}}
	s.purchaseProvider = provider
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodPost, h.URL+"/account/billing/nano/purchase", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_test"})
	req.Header.Set("Origin", h.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "https://checkout.test/cs_test_1" {
		t.Fatalf("bridge status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	unauth, _ := http.NewRequest(http.MethodPost, h.URL+"/account/billing/nano/purchase", nil)
	resp, err = client.Do(unauth)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauthenticated status=%d", resp.StatusCode)
	}
	cross, _ := http.NewRequest(http.MethodPost, h.URL+"/account/billing/nano/purchase", nil)
	cross.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_test"})
	cross.Header.Set("Origin", "https://evil.example")
	resp, err = client.Do(cross)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", resp.StatusCode)
	}
}
