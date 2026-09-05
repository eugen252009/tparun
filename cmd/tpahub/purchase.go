package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
)

func (s *server) nanoPurchaseAvailable() bool {
	return s.cfg.BillingPurchasesEnabled && ValidateBillingConfiguration(s.cfg) == nil
}
func (s *server) startNanoPurchase(ctx context.Context, account string, businessUse bool) (store.BillingPurchase, CheckoutResult, error) {
	s.purchaseMu.Lock()
	defer s.purchaseMu.Unlock()
	if !s.nanoPurchaseAvailable() {
		return store.BillingPurchase{}, CheckoutResult{}, fmt.Errorf("purchase flow unavailable")
	}
	now := s.currentTime().UTC()
	period, err := entitlementPeriod(now.Format("2006-01-02"), now)
	if err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, fmt.Errorf("entitlement period unavailable")
	}
	offer := tpaNanoOffer()
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
	purchase := store.BillingPurchase{ID: purchaseID, AccountID: account, ProductID: string(offer.ProductID), PlanCode: offer.ID, RateCardID: card.ID, RateCardMode: string(card.Mode), TaxClassification: string(classification), Currency: card.Currency, CheckoutRequestVersion: "v2", AmountMinor: card.FixedPlanAmountsMinor[offer.ID], BillingEnvironment: string(BillingEnvironmentTest), Provider: "stripe", Status: "pending", EntitlementPeriodStart: period.Start.Format(time.RFC3339Nano), EntitlementPeriodEnd: period.End.Format(time.RFC3339Nano), CreatedAt: created, UpdatedAt: created}
	purchase, err = s.db.CreatePendingPurchase(ctx, purchase, created, offer.Grants...)
	if err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, err
	}
	if purchase.ProviderReference != "" {
		return purchase, CheckoutResult{Provider: "stripe", Environment: BillingEnvironmentTest, ProviderReference: purchase.ProviderReference, URL: purchase.CheckoutURL}, nil
	}
	purchaseProvider := s.purchaseProvider
	if purchaseProvider == nil {
		return purchase, CheckoutResult{}, fmt.Errorf("payment provider unavailable")
	}
	idempotencyKey := "commerce:" + purchase.ProductID + ":purchase:" + purchase.ID + ":stripe:test:checkout:v2"
	if purchase.ProductID == string(commerce.ProductTPAHub) {
		idempotencyKey = "tpahub:purchase:" + purchase.ID + ":stripe:test:checkout:v2"
	}
	checkout, err := purchaseProvider.CreateCheckout(ctx, payments.Operation{OperationID: purchase.ID, IdempotencyKey: idempotencyKey, CustomerID: commerce.CustomerID(purchase.AccountID), ProductID: commerce.ProductID(purchase.ProductID), Description: offer.Description, CustomerDescription: "TPA Hub account " + purchase.AccountID, CustomerMetadata: map[string]string{"tpahub_account_id": purchase.AccountID}, Currency: purchase.Currency, AmountMinor: purchase.AmountMinor, TaxClass: payments.TaxClass(purchase.TaxClassification), Metadata: map[string]string{"tpahub_purchase_id": purchase.ID}, SuccessURL: "https://tpa.lupricht.net/", CancelURL: "https://tpa.lupricht.net/", Environment: payments.EnvironmentTest})
	if err != nil {
		var pe providerError
		if errors.As(err, &pe) && pe.Code == "provider-rejected-4xx" {
			_ = s.db.FailPendingPurchase(ctx, purchase.ID, now.Format(time.RFC3339Nano))
		}
		return purchase, CheckoutResult{}, err
	}
	if err = s.db.UpdatePurchaseCheckout(ctx, purchase.ID, checkout.ProviderReference, checkout.URL, s.currentTime().UTC().Format(time.RFC3339Nano)); err != nil {
		return store.BillingPurchase{}, CheckoutResult{}, err
	}
	purchase.ProviderReference = checkout.ProviderReference
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
	if checkout.URL != "" {
		out["checkout_url"] = checkout.URL
	} else {
		out["checkout_reference"] = checkout.ProviderReference
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
	if checkout.URL == "" {
		bad(w, http.StatusBadGateway, "Sandbox checkout unavailable")
		return
	}
	http.Redirect(w, r, checkout.URL, http.StatusSeeOther)
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
	if s.paymentWebhook == nil {
		bad(w, http.StatusNotFound, "webhook unavailable")
		return
	}
	paymentEvent, err := s.paymentWebhook.ParseWebhook(body, r.Header.Get("Stripe-Signature"), s.cfg.StripeWebhookSecret, s.currentTime())
	if err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	if paymentEvent.Type == commerce.PaymentIgnored {
		w.WriteHeader(http.StatusOK)
		return
	}
	if state, stateErr := s.db.PurchasePaymentState(r.Context(), paymentEvent.OperationID); stateErr == nil && state.ProviderReference == "" {
		if recoverErr := s.db.RecoverPurchaseCheckout(r.Context(), paymentEvent.OperationID, "stripe", string(payments.EnvironmentTest), paymentEvent.ProviderReference, paymentEvent.OccurredAt, s.currentTime().UTC().Format(time.RFC3339Nano)); recoverErr != nil {
			bad(w, http.StatusBadRequest, recoverErr.Error())
			return
		}
	}
	// Provider events prove payment state; the store activates the immutable
	// commercial validity finalized with the checkout.
	paymentService := commerce.PaymentService{Repository: s.db}
	if err = paymentService.Process(r.Context(), "stripe", string(payments.EnvironmentTest), paymentEvent, time.Time{}, time.Time{}); err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}
