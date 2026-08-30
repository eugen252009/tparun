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
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

type stripeCheckoutEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID            string            `json:"id"`
			PaymentStatus string            `json:"payment_status"`
			AmountTotal   int64             `json:"amount_total"`
			Currency      string            `json:"currency"`
			Metadata      map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

func (s *server) nanoPurchaseAvailable() bool {
	return s.cfg.BillingPurchasesEnabled && ValidateBillingConfiguration(s.cfg) == nil
}
func (s *server) startNanoPurchase(ctx context.Context, account string, businessUse bool) (store.BillingPurchase, CheckoutResult, error) {
	if !s.nanoPurchaseAvailable() {
		return store.BillingPurchase{}, CheckoutResult{}, fmt.Errorf("purchase flow unavailable")
	}
	now := s.currentTime().UTC()
	period, err := entitlementPeriod(now.Format("2006-01-02"), now)
	if err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, fmt.Errorf("entitlement period unavailable")
	}
	card, err := rateCardByID(s.cfg.BillingRateCard)
	if err != nil || validateRateCard(card) != nil {
		return store.BillingPurchase{}, CheckoutResult{}, fmt.Errorf("Nano pricing unavailable")
	}
	purchaseID := newID()
	created := now.Format(time.RFC3339Nano)
	classification := TaxSaaSPersonal
	if businessUse {
		classification = TaxSaaSBusiness
	}
	purchase := store.BillingPurchase{ID: purchaseID, AccountID: account, PlanCode: "nano", RateCardID: card.ID, RateCardMode: string(card.Mode), TaxClassification: string(classification), Currency: card.Currency, AmountMinor: card.FixedPlanAmountsMinor["nano"], BillingEnvironment: string(BillingEnvironmentTest), Provider: "stripe", Status: "pending", EntitlementPeriodStart: period.Start.Format(time.RFC3339Nano), EntitlementPeriodEnd: period.End.Format(time.RFC3339Nano), CreatedAt: created, UpdatedAt: created}
	purchase, err = s.db.CreatePendingPurchase(ctx, purchase, created)
	if err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, err
	}
	if purchase.ProviderReference != "" {
		return purchase, CheckoutResult{Provider: "stripe", Environment: BillingEnvironmentTest, ExternalReference: purchase.ProviderReference}, nil
	}
	var purchaseProvider PurchaseProvider = s.purchaseProvider
	if purchaseProvider == nil {
		provider, pe := NewStripeBillingProvider(s.db, s.cfg.StripeTestSecret, BillingEnvironmentTest)
		if pe != nil {
			return store.BillingPurchase{}, CheckoutResult{}, pe
		}
		provider.SuccessURL = "https://tpa.lupricht.net/"
		provider.CancelURL = "https://tpa.lupricht.net/"
		purchaseProvider = provider
	}
	checkout, err := purchaseProvider.CreateCheckout(ctx, FinalizedPurchase{PurchaseID: purchase.ID, AccountID: purchase.AccountID, PlanCode: purchase.PlanCode, RateCardID: purchase.RateCardID, RateCardMode: RateCardProduction, TaxClassification: ProductTaxClassification(purchase.TaxClassification), Currency: purchase.Currency, AmountMinor: purchase.AmountMinor, BillingEnvironment: BillingEnvironmentTest, IdempotencyKey: "tpahub:purchase:" + purchase.ID + ":stripe:test:checkout", FinalizedAt: now})
	if err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, err
	}
	if err = s.db.UpdatePurchaseCheckout(ctx, purchase.ID, checkout.ExternalReference, now.Format(time.RFC3339Nano)); err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, err
	}
	purchase.ProviderReference = checkout.ExternalReference
	return purchase, checkout, nil
}

func (s *server) createNanoPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	businessUse, parseErr := parseBusinessUseJSON(r)
	if parseErr != nil {
		bad(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	purchase, checkout, err := s.startNanoPurchase(r.Context(), accountID(r), businessUse)
	if err != nil {
		if err.Error() == "purchase-already-active" {
			bad(w, http.StatusConflict, err.Error())
		} else {
			bad(w, http.StatusBadGateway, "checkout creation failed")
		}
		return
	}
	out := map[string]any{"purchase_id": purchase.ID, "status": purchase.Status, "provider": "stripe", "environment": "test"}
	if checkout.CheckoutURL != "" {
		out["checkout_url"] = checkout.CheckoutURL
	} else {
		out["checkout_reference"] = checkout.ExternalReference
	}
	jsonOut(w, out)
}

func (s *server) accountNanoPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.webAuth(w, r) {
		return
	}
	businessUse, parseErr := parseBusinessUseForm(r)
	if parseErr != nil {
		bad(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	_, checkout, err := s.startNanoPurchase(r.Context(), accountID(r), businessUse)
	if err != nil {
		if err.Error() == "purchase-already-active" {
			bad(w, http.StatusConflict, "Nano purchase already active")
		} else {
			bad(w, http.StatusBadGateway, "Sandbox checkout unavailable")
		}
		return
	}
	if checkout.CheckoutURL == "" {
		bad(w, http.StatusBadGateway, "Sandbox checkout unavailable")
		return
	}
	http.Redirect(w, r, checkout.CheckoutURL, http.StatusSeeOther)
}

func parseBusinessUseJSON(r *http.Request) (bool, error) {
	body, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	if e != nil || len(body) > 8192 {
		return false, fmt.Errorf("invalid purchase request")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return false, nil
	}
	var input struct {
		BusinessUse *bool `json:"business_use"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&input); e != nil {
		return false, fmt.Errorf("invalid purchase request")
	}
	var extra any
	if e = dec.Decode(&extra); e != io.EOF {
		return false, fmt.Errorf("invalid purchase request")
	}
	if input.BusinessUse == nil {
		return false, nil
	}
	return *input.BusinessUse, nil
}
func parseBusinessUseForm(r *http.Request) (bool, error) {
	if e := r.ParseForm(); e != nil {
		return false, fmt.Errorf("invalid purchase request")
	}
	value, ok := r.Form["business_use"]
	if !ok || len(value) == 0 || value[0] == "" {
		return false, nil
	}
	if value[0] != "on" && value[0] != "true" && value[0] != "1" {
		return false, fmt.Errorf("invalid business_use")
	}
	return true, nil
}

func (s *server) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.cfg.BillingPurchasesEnabled || s.cfg.BillingProvider != "stripe" || s.cfg.BillingEnvironment != string(BillingEnvironmentTest) || s.cfg.StripeWebhookSecret == "" {
		bad(w, http.StatusNotFound, "webhook unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		bad(w, http.StatusRequestEntityTooLarge, "webhook body too large")
		return
	}
	if err = verifyStripeSignature(r.Header.Get("Stripe-Signature"), body, s.cfg.StripeWebhookSecret, s.currentTime()); err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	var event stripeCheckoutEvent
	if json.Unmarshal(body, &event) != nil || event.ID == "" || event.Data.Object.ID == "" {
		bad(w, http.StatusBadRequest, "invalid provider event")
		return
	}
	obj := event.Data.Object
	purchaseID := obj.Metadata["tpahub_purchase_id"]
	if purchaseID == "" {
		bad(w, http.StatusBadRequest, "purchase reference missing")
		return
	}
	purchase, err := s.db.Purchase(r.Context(), purchaseID)
	if err != nil || purchase.Provider != "stripe" || purchase.BillingEnvironment != "test" || purchase.ProviderReference != obj.ID {
		bad(w, http.StatusBadRequest, "provider reference mismatch")
		return
	}
	kind := ""
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if obj.PaymentStatus != "paid" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if obj.AmountTotal != purchase.AmountMinor || strings.ToUpper(obj.Currency) != strings.ToUpper(purchase.Currency) {
			bad(w, http.StatusBadRequest, "payment verification failed")
			return
		}
		kind = "success"
	case "checkout.session.async_payment_failed":
		kind = "failed"
	case "checkout.session.expired":
		kind = "expired"
	default:
		w.WriteHeader(http.StatusOK)
		return
	}
	activationPeriod, perr := entitlementPeriod(s.currentTime().UTC().Format("2006-01-02"), s.currentTime())
	if perr != nil {
		bad(w, http.StatusInternalServerError, "entitlement activation unavailable")
		return
	}
	received := s.currentTime().UTC().Format(time.RFC3339Nano)
	amount, currency := purchase.AmountMinor, purchase.Currency
	if kind == "success" {
		amount, currency = obj.AmountTotal, strings.ToUpper(obj.Currency)
	}
	eventRecord := store.BillingProviderEvent{Provider: "stripe", Environment: "test", ProviderEventID: event.ID, EventType: event.Type, ReceivedAt: received}
	if err = s.db.ApplyBillingProviderEvent(r.Context(), eventRecord, purchase.ID, obj.ID, kind, amount, currency, received, activationPeriod.Start.Format(time.RFC3339Nano), activationPeriod.End.Format(time.RFC3339Nano)); err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func verifyStripeSignature(header string, body []byte, secret string, now time.Time) error {
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || timestamp == "" {
		return fmt.Errorf("invalid webhook signature")
	}
	if delta := now.Unix() - unix; delta > 300 || delta < -300 {
		return fmt.Errorf("expired webhook signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range signatures {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return fmt.Errorf("invalid webhook signature")
}
