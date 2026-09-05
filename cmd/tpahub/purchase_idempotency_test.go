package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
)

func configuredPurchaseTestServer(t *testing.T) *server {
	s, _ := testServer(t)
	s.cfg.BillingPurchasesEnabled = true
	s.cfg.BillingProvider = "stripe"
	s.cfg.BillingEnvironment = "test"
	s.cfg.BillingRateCard = "production-nano-v1"
	s.cfg.StripeTestSecret = "sk_test_example"
	s.cfg.StripeWebhookSecret = "whsec_example"
	p, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	p.BaseURL = "https://stripe.test"
	p.Client = &http.Client{Transport: &checkoutTransport{}}
	s.purchaseProvider = p
	s.paymentWebhook = p
	return s
}
func TestHostedPurchaseActivatesWithoutCompatibilityEntitlement(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	h := httptest.NewServer(s.routes())
	defer h.Close()
	response, err := http.Post(h.URL+"/v1/accounts", "application/json", strings.NewReader(`{"email":"hosted-purchase@example.invalid","password":"long-enough-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	var account struct {
		AccountID string `json:"account_id"`
		Token     string `json:"token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&account); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("registration status=%d", response.StatusCode)
	}
	if _, err = s.db.Entitlement(context.Background(), account.AccountID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hosted entitlement=%v", err)
	}
	request, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/purchases/nano", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+account.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var purchase struct {
		PurchaseID string `json:"purchase_id"`
	}
	json.NewDecoder(response.Body).Decode(&purchase)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("purchase status=%d", response.StatusCode)
	}
	p, err := s.db.Purchase(context.Background(), purchase.PurchaseID)
	if err != nil {
		t.Fatal(err)
	}
	event := fmt.Sprintf(`{"id":"evt_hosted_purchase","type":"checkout.session.completed","data":{"object":{"id":"%s","payment_status":"paid","amount_total":500,"currency":"eur","metadata":{"tpahub_purchase_id":"%s"}}}}`, p.ProviderReference, purchase.PurchaseID)
	web, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/providers/stripe/webhook", strings.NewReader(event))
	web.Header.Set("Stripe-Signature", signedStripeEvent(event, "whsec_example", s.currentTime()))
	response, err = http.DefaultClient.Do(web)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("webhook status=%d", response.StatusCode)
	}
	paid, _ := s.db.Purchase(context.Background(), purchase.PurchaseID)
	if paid.Status != "paid" {
		t.Fatalf("status=%s", paid.Status)
	}
	for _, capability := range []commerce.CapabilityID{CapabilityRepositoryCreate, CapabilityRepositoryPublish, CapabilityRepositoryDownload, CapabilityRepositoryReadPrivate} {
		if _, err = s.db.GetGrant(context.Background(), commerce.CustomerID(account.AccountID), commerce.ProductTPAHub, capability, s.currentTime()); err != nil {
			t.Fatalf("grant %s: %v", capability, err)
		}
	}
}

func TestActivePaidEntitlementBlocksPurchaseAndExpiredAllowsIt(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	active := store.AccountEntitlement{AccountID: "a", PlanCode: "nano", StartedAt: "2026-09-15T00:00:00Z", ValidFrom: "2026-09-15T00:00:00Z", ValidUntil: "2027-09-15T00:00:00Z", CreatedAt: "2026-09-15T00:00:00Z", UpdatedAt: "2026-09-15T00:00:00Z", Source: "purchase", PurchaseID: "paid-active"}
	if err := s.db.SetEntitlement(ctx, active); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.startNanoPurchase(ctx, "a", false); err == nil || err.Error() != "purchase-already-active" {
		t.Fatalf("active entitlement purchase error=%v", err)
	}
	active.ValidFrom = "2025-09-15T00:00:00Z"
	active.ValidUntil = "2026-09-15T12:00:00Z"
	if err := s.db.SetEntitlement(ctx, active); err != nil {
		t.Fatal(err)
	}
	purchase, checkout, err := s.startNanoPurchase(ctx, "a", false)
	if err != nil || purchase.ID == "" || checkout.URL == "" {
		t.Fatalf("expired entitlement purchase=%+v checkout=%+v err=%v", purchase, checkout, err)
	}
}

type synchronizedCheckoutTransport struct {
	mu    sync.Mutex
	inner checkoutTransport
}

func (t *synchronizedCheckoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inner.RoundTrip(r)
}

type expiringCheckoutProvider struct {
	now        *time.Time
	operations []string
}

func (p *expiringCheckoutProvider) CreateCheckout(_ context.Context, operation payments.Operation) (payments.CheckoutSession, error) {
	p.operations = append(p.operations, operation.OperationID)
	if len(p.operations) == 1 {
		*p.now = p.now.AddDate(1, 0, 0)
	}
	return payments.CheckoutSession{Provider: "stripe", Environment: payments.EnvironmentTest, ProviderReference: "ref-" + operation.OperationID, URL: "https://checkout.invalid/" + operation.OperationID}, nil
}

func TestPurchaseEndpointRejectsCheckoutCreatedAfterValidityExpiry(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	provider := &expiringCheckoutProvider{now: &now}
	s.purchaseProvider = provider
	h := httptest.NewServer(s.routes())
	defer h.Close()

	requestPurchase := func() (*http.Response, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/purchases/nano", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer tph_s_test")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		return response, body
	}

	first, _ := requestPurchase()
	if first.StatusCode != http.StatusBadGateway {
		t.Fatalf("expired checkout status=%d", first.StatusCode)
	}
	if len(provider.operations) != 1 {
		t.Fatalf("provider operations=%v", provider.operations)
	}
	stale, err := s.db.Purchase(context.Background(), provider.operations[0])
	if err != nil || stale.ProviderReference != "" || stale.CheckoutURL != "" {
		t.Fatalf("expired checkout persisted=%+v err=%v", stale, err)
	}

	second, body := requestPurchase()
	if second.StatusCode != http.StatusOK || len(provider.operations) != 2 || provider.operations[0] == provider.operations[1] {
		t.Fatalf("fresh retry status=%d operations=%v body=%v", second.StatusCode, provider.operations, body)
	}
	stale, err = s.db.Purchase(context.Background(), provider.operations[0])
	if err != nil || stale.Status != "failed" {
		t.Fatalf("stale purchase state=%+v err=%v", stale, err)
	}
	staleReference := "ref-" + stale.ID
	if err = s.db.RecoverPurchaseCheckout(context.Background(), stale.ID, stale.Provider, stale.BillingEnvironment, staleReference, now, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("paid stale checkout recovery: %v", err)
	}
	if err = s.db.ApplyPaymentTransition(context.Background(), commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-stale-expired-checkout", OperationID: stale.ID, Type: commerce.PaymentSucceeded, ProviderReference: staleReference, AmountMinor: stale.AmountMinor, Currency: stale.Currency, OccurredAt: now}, Provider: stale.Provider, Environment: stale.BillingEnvironment, Kind: "success", AmountMinor: stale.AmountMinor, Currency: stale.Currency}); err != nil {
		t.Fatalf("paid stale checkout activation: %v", err)
	}
	grant, err := s.db.GetGrant(context.Background(), "a", commerce.ProductTPAHub, CapabilityRepositoryPublish, now.Add(time.Hour))
	foundStale := grant.PurchaseID == stale.ID
	if grant.Effective != nil {
		for _, contributor := range grant.Effective.Contributors {
			foundStale = foundStale || contributor.PurchaseID == stale.ID
		}
	}
	if err != nil || !foundStale {
		t.Fatalf("paid stale checkout did not receive current grant=%+v err=%v", grant, err)
	}
}

func TestConcurrentChangedIntentLeavesDeterministicPurchaseStates(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	s.now = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
	transport := &synchronizedCheckoutTransport{}
	provider, err := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = "https://stripe.test"
	provider.Client = &http.Client{Transport: transport}
	s.purchaseProvider = provider
	ctx := context.Background()
	type result struct {
		purchase store.BillingPurchase
		err      error
	}
	results := make(chan result, 2)
	for _, businessUse := range []bool{false, true} {
		go func(businessUse bool) {
			purchase, _, err := s.startNanoPurchase(ctx, "a", businessUse)
			results <- result{purchase: purchase, err: err}
		}(businessUse)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent purchases failed: first=%v second=%v", first.err, second.err)
	}
	if first.purchase.ID == second.purchase.ID {
		t.Fatalf("concurrent intents reused one purchase: %s", first.purchase.ID)
	}
	firstSaved, firstErr := s.db.Purchase(ctx, first.purchase.ID)
	secondSaved, secondErr := s.db.Purchase(ctx, second.purchase.ID)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("concurrent purchases were not persisted: first=%v second=%v", firstErr, secondErr)
	}
	states := []string{firstSaved.Status, secondSaved.Status}
	if !(states[0] == "pending" && states[1] == "superseded" || states[0] == "superseded" && states[1] == "pending") {
		t.Fatalf("unexpected concurrent purchase states: %v", states)
	}
}

func TestPendingPurchaseIntentChangeGetsNewIdentity(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	s.now = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	var err error
	personal, _, e := s.startNanoPurchase(ctx, "a", false)
	if e != nil {
		t.Fatal(e)
	}
	business, _, e := s.startNanoPurchase(ctx, "a", true)
	if e != nil {
		t.Fatal(e)
	}
	if personal.ID == business.ID || business.TaxClassification != string(TaxSaaSBusiness) {
		t.Fatalf("identities personal=%+v business=%+v", personal, business)
	}
	old, _ := s.db.Purchase(ctx, personal.ID)
	if old.Status != "superseded" {
		t.Fatalf("old pending status=%s", old.Status)
	}
	if err := s.db.UpdatePurchaseCheckout(ctx, old.ID, "wrong-reference", "https://checkout.invalid", s.currentTime().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("superseded purchase checkout state was silently updated")
	}
	again, _, e := s.startNanoPurchase(ctx, "a", true)
	if e != nil || again.ID != business.ID {
		t.Fatalf("compatible retry=%+v err=%v", again, e)
	}
	if err = s.db.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-superseded", OperationID: old.ID, Type: commerce.PaymentSucceeded, ProviderReference: old.ProviderReference, AmountMinor: old.AmountMinor, Currency: old.Currency, OccurredAt: s.currentTime()}, Provider: old.Provider, Environment: old.BillingEnvironment, Kind: "success", AmountMinor: old.AmountMinor, Currency: old.Currency, ValidFrom: s.currentTime(), ValidUntil: s.currentTime().AddDate(1, 0, 0)}); err != nil {
		t.Fatalf("superseded payable checkout was not reconciliable: %v", err)
	}
	if err = s.db.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-current", OperationID: business.ID, Type: commerce.PaymentSucceeded, ProviderReference: business.ProviderReference, AmountMinor: business.AmountMinor, Currency: business.Currency, OccurredAt: s.currentTime()}, Provider: business.Provider, Environment: business.BillingEnvironment, Kind: "success", AmountMinor: business.AmountMinor, Currency: business.Currency, ValidFrom: s.currentTime(), ValidUntil: s.currentTime().AddDate(1, 0, 0)}); err != nil {
		t.Fatalf("second payable checkout was not reconciliable: %v", err)
	}
	instances, countErr := s.db.PurchaseGrantInstanceCount(ctx, "a")
	if countErr != nil || instances < 8 {
		t.Fatalf("paid purchase grant instances=%d err=%v", instances, countErr)
	}
}
func TestStalePreTaxPurchaseGetsNewCheckoutOperation(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	ctx := context.Background()
	now := "2026-09-15T00:00:00Z"
	old := store.BillingPurchase{ID: "old-purchase", AccountID: "a", PlanCode: "nano", RateCardID: "production-nano-v1", RateCardMode: "production", TaxClassification: string(TaxSaaSPersonal), Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now, EntitlementPeriodEnd: "2027-09-15T00:00:00Z", CreatedAt: now, UpdatedAt: now, CheckoutRequestVersion: "v1"}
	if _, e := s.db.CreatePendingPurchase(ctx, old, now, commerce.GrantTemplate{CapabilityID: CapabilityRepositoryPublish}); e != nil {
		t.Fatal(e)
	}
	fresh, _, e := s.startNanoPurchase(ctx, "a", false)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.ID == old.ID {
		t.Fatal("stale pre-tax purchase reused")
	}
	saved, _ := s.db.Purchase(ctx, old.ID)
	if saved.Status != "failed" {
		t.Fatalf("stale status=%s", saved.Status)
	}
}
