package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestFinalizedPurchaseOwnershipAndInstancesAreImmutable(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"owner-a", "owner-b"} {
		if err = s.CreateAccount(ctx, Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	purchase := BillingPurchase{ID: "immutable-purchase", AccountID: "owner-a", ProductID: "test-product", PlanCode: "offer", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "mock", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(0, 1, 0).Format(time.RFC3339Nano), ProviderReference: "reference", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodMonthly}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, purchase.ID, purchase.ProviderReference, "https://checkout.invalid/immutable", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE billing_purchases SET account_id='owner-b' WHERE id=?`, purchase.ID); err == nil {
		t.Fatal("finalized purchase ownership changed")
	}
	if _, err = s.db.Exec(`UPDATE purchase_grant_snapshots SET operation_limit=99 WHERE purchase_id=?`, purchase.ID); err == nil {
		t.Fatal("finalized purchase snapshot changed")
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "immutable-event", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: purchase.Provider, Environment: purchase.BillingEnvironment, Kind: "success", AmountMinor: purchase.AmountMinor, Currency: purchase.Currency}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE purchase_grant_instances SET offer_id='changed' WHERE purchase_id=?`, purchase.ID); err == nil {
		t.Fatal("purchase grant instance changed")
	}
	if _, err = s.db.Exec(`DELETE FROM purchase_grant_instances WHERE purchase_id=?`, purchase.ID); err == nil {
		t.Fatal("purchase grant instance deleted")
	}
	if _, err = s.db.Exec(`INSERT INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "owner-a", purchase.ProductID, "late", purchase.ID, purchase.PlanCode, 1, purchase.EntitlementPeriodStart, purchase.EntitlementPeriodEnd, commerce.PeriodMonthly, 0, 0, "", 0, purchase.CreatedAt, purchase.UpdatedAt); err == nil {
		t.Fatal("purchase grant instance inserted after activation")
	}
}

func TestConflictingPreexistingPurchaseInstanceAbortsActivation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "conflict-a", Email: "conflict-a@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateAccount(ctx, Account{ID: "conflict-b", Email: "conflict-b@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	purchase := BillingPurchase{ID: "conflict-purchase", AccountID: "conflict-a", ProductID: "conflict-product", PlanCode: "offer", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "mock", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(0, 1, 0).Format(time.RFC3339Nano), ProviderReference: "conflict-reference", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	template := commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodMonthly}
	if _, err = s.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, template); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, purchase.ID, purchase.ProviderReference, "https://checkout.invalid/conflict", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "conflict-b", purchase.ProductID, template.CapabilityID, purchase.ID, "wrong", 1, purchase.EntitlementPeriodStart, purchase.EntitlementPeriodEnd, template.Period, 0, 0, "", 0, purchase.CreatedAt, purchase.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "conflict-event", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: purchase.Provider, Environment: purchase.BillingEnvironment, Kind: "success", AmountMinor: purchase.AmountMinor, Currency: purchase.Currency})
	if err == nil {
		t.Fatal("conflicting purchase instance accepted")
	}
	saved, err := s.Purchase(ctx, purchase.ID)
	if err != nil || saved.Status != "pending" {
		t.Fatalf("conflicting activation changed purchase: %+v, %v", saved, err)
	}
}

func TestSupersededTerminalProviderEventIsPersisted(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "superseded", Email: "superseded@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	makePurchase := func(id, reference string, amount int64) BillingPurchase {
		return BillingPurchase{ID: id, AccountID: "superseded", ProductID: "superseded-product", PlanCode: "offer", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: amount, BillingEnvironment: "test", Provider: "mock", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(0, 1, 0).Format(time.RFC3339Nano), ProviderReference: reference, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	}
	first := makePurchase("first", "first-reference", 1)
	if _, err = s.CreatePendingPurchase(ctx, first, first.CreatedAt, commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodMonthly}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, first.ID, first.ProviderReference, "https://checkout.invalid/first", first.CreatedAt); err != nil {
		t.Fatal(err)
	}
	second := makePurchase("second", "second-reference", 2)
	if _, err = s.CreatePendingPurchase(ctx, second, second.CreatedAt, commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodMonthly}); err != nil {
		t.Fatal(err)
	}
	failed := commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "first-failed", OperationID: first.ID, Type: commerce.PaymentFailed, ProviderReference: first.ProviderReference, AmountMinor: first.AmountMinor, Currency: first.Currency, OccurredAt: now}, Provider: first.Provider, Environment: first.BillingEnvironment, Kind: "failed", AmountMinor: first.AmountMinor, Currency: first.Currency}
	if err = s.ApplyPaymentTransition(ctx, failed); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Purchase(ctx, first.ID)
	if err != nil || saved.Status != "failed" {
		t.Fatalf("superseded terminal state=%+v err=%v", saved, err)
	}
	success := failed
	success.Event = commerce.PaymentEvent{ProviderEventID: "first-success", OperationID: first.ID, Type: commerce.PaymentSucceeded, ProviderReference: first.ProviderReference, AmountMinor: first.AmountMinor, Currency: first.Currency, OccurredAt: now}
	success.Kind = "success"
	if err = s.ApplyPaymentTransition(ctx, success); err != nil {
		t.Fatal(err)
	}
	saved, err = s.Purchase(ctx, first.ID)
	if err != nil || saved.Status != "paid" {
		t.Fatalf("late success state=%+v err=%v", saved, err)
	}
}
