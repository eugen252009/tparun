package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
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
	return s
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
	again, _, e := s.startNanoPurchase(ctx, "a", true)
	if e != nil || again.ID != business.ID {
		t.Fatalf("compatible retry=%+v err=%v", again, e)
	}
	if err = s.db.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-superseded", OperationID: old.ID, Type: commerce.PaymentSucceeded, ProviderReference: old.ProviderReference, AmountMinor: old.AmountMinor, Currency: old.Currency, OccurredAt: s.currentTime()}, Provider: old.Provider, Environment: old.BillingEnvironment, Kind: "success", AmountMinor: old.AmountMinor, Currency: old.Currency, ValidFrom: s.currentTime(), ValidUntil: s.currentTime().AddDate(1, 0, 0)}); err != nil {
		t.Fatalf("superseded payable checkout was not reconciliable: %v", err)
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
