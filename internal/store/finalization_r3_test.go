package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func finalizedPurchaseFixture(t *testing.T) (*Store, BillingPurchase, time.Time, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	from := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(1, 0, 0)
	if err = s.CreateAccount(ctx, Account{ID: "finalized", Email: "finalized@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p := BillingPurchase{ID: "purchase", AccountID: "finalized", ProductID: "tpa-hub", PlanCode: "nano", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	p, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, p.ID, "reference-a", "https://checkout", p.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	p.ProviderReference = "reference-a"
	return s, p, from, until
}

func TestDelayedSuccessUsesImmutableFinalizedValidity(t *testing.T) {
	s, p, from, until := finalizedPurchaseFixture(t)
	delayed := until.AddDate(0, 6, 0)
	err := s.ApplyPaymentTransition(context.Background(), commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "delayed", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: delayed}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency, ValidFrom: delayed, ValidUntil: delayed.AddDate(1, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrant(context.Background(), "finalized", commerce.ProductTPAHub, "repository.publish", from)
	if err != nil || !g.ValidFrom.Equal(from) || g.ValidUntil == nil || !g.ValidUntil.Equal(until) {
		t.Fatalf("activated validity=%+v err=%v", g, err)
	}
}

func TestFinalizedProviderReferenceCannotChange(t *testing.T) {
	s, p, _, _ := finalizedPurchaseFixture(t)
	if err := s.UpdatePurchaseCheckout(context.Background(), p.ID, "reference-b", "https://other", p.UpdatedAt); err == nil {
		t.Fatal("second checkout finalization accepted")
	}
	if _, err := s.db.Exec(`UPDATE billing_purchases SET provider_reference='reference-b' WHERE id=?`, p.ID); err == nil {
		t.Fatal("finalized provider reference mutated")
	}
	if _, err := s.db.Exec(`UPDATE billing_purchases SET snapshot_finalized=0 WHERE id=?`, p.ID); err == nil {
		t.Fatal("finalized purchase was reopened")
	}
	err := s.ApplyPaymentTransition(context.Background(), commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "wrong", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "reference-b", AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Now()}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency})
	if err == nil {
		t.Fatal("unrelated provider reference activated purchase")
	}
}

func TestExpiredPurchaseCannotFinalizeCheckout(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	from := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	until := from.Add(time.Hour)
	if err = s.CreateAccount(ctx, Account{ID: "account", Email: "account@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p := BillingPurchase{ID: "expiring-checkout", AccountID: "account", ProductID: "tpa-hub", PlanCode: "nano", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish"}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, p.ID, "reference", "https://checkout.invalid", until.Format(time.RFC3339Nano)); err == nil {
		t.Fatal("expired purchase checkout finalized")
	}
	stored, err := s.Purchase(ctx, p.ID)
	if err != nil || stored.ProviderReference != "" || stored.CheckoutURL != "" {
		t.Fatalf("expired checkout mutated purchase=%+v err=%v", stored, err)
	}
}

func TestRecoveredExpiredCheckoutPreservesCalendarYearValidity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	from := time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(1, 0, 0)
	if err = s.CreateAccount(ctx, Account{ID: "calendar", Email: "calendar@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p := BillingPurchase{ID: "calendar-purchase", AccountID: "calendar", ProductID: "tpa-hub", PlanCode: "nano", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual}); err != nil {
		t.Fatal(err)
	}
	occurred := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	if err = s.RecoverPurchaseCheckout(ctx, p.ID, p.Provider, p.BillingEnvironment, "calendar-reference", occurred, occurred.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Purchase(ctx, p.ID)
	if err != nil || stored.EntitlementPeriodStart != occurred.Format(time.RFC3339Nano) || stored.EntitlementPeriodEnd != occurred.AddDate(1, 0, 0).Format(time.RFC3339Nano) {
		t.Fatalf("recovered calendar validity=%+v err=%v", stored, err)
	}
}

func TestFinalizedPurchaseRejectsWrongProviderAndEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, provider, environment string }{
		{name: "provider", provider: "other", environment: "test"},
		{name: "environment", provider: "stripe", environment: "live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, from, _ := finalizedPurchaseFixture(t)
			err := s.ApplyPaymentTransition(context.Background(), commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "wrong-" + tc.name, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: from}, Provider: tc.provider, Environment: tc.environment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency})
			if err == nil {
				t.Fatalf("wrong %s activated purchase", tc.name)
			}
			stored, storedErr := s.Purchase(context.Background(), p.ID)
			if storedErr != nil || stored.Status != "pending" {
				t.Fatalf("purchase=%+v err=%v", stored, storedErr)
			}
		})
	}
}

func TestUnfinalizedPurchaseCannotActivate(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	from := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(1, 0, 0)
	if err = s.CreateAccount(ctx, Account{ID: "unfinalized", Email: "unfinalized@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p := BillingPurchase{ID: "unfinalized-purchase", AccountID: "unfinalized", ProductID: "tpa-hub", PlanCode: "nano", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "prefilled-reference", CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual}); err != nil {
		t.Fatal(err)
	}
	err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "unfinalized-event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: from}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency})
	if err == nil {
		t.Fatal("unfinalized purchase activated")
	}
	stored, _ := s.Purchase(ctx, p.ID)
	if stored.Status != "pending" {
		t.Fatalf("unfinalized purchase status=%s", stored.Status)
	}
}

func TestMalformedFinalizedValidityRollsBackActivation(t *testing.T) {
	for _, tc := range []struct{ name, from, until string }{{"equal", "2030-01-01T00:00:00Z", "2030-01-01T00:00:00Z"}, {"reversed", "2030-01-02T00:00:00Z", "2030-01-01T00:00:00Z"}, {"empty", "", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, _, _ := finalizedPurchaseFixture(t)
			if _, err := s.db.Exec(`DROP TRIGGER billing_purchase_terms_immutable; UPDATE billing_purchases SET entitlement_period_start=?,entitlement_period_end=? WHERE id=?`, tc.from, tc.until, p.ID); err != nil {
				t.Fatal(err)
			}
			err := s.ApplyPaymentTransition(context.Background(), commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "bad-" + tc.name, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Now()}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency})
			if err == nil {
				t.Fatal("malformed validity activated")
			}
			got, _ := s.Purchase(context.Background(), p.ID)
			if got.Status != "pending" {
				t.Fatalf("status=%s", got.Status)
			}
			n, _ := s.PurchaseGrantInstanceCount(context.Background(), p.AccountID)
			if n != 0 {
				t.Fatalf("instances=%d", n)
			}
			var projection int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM account_entitlements WHERE account_id=?`, p.AccountID).Scan(&projection)
			if projection != 0 {
				t.Fatalf("projection=%d", projection)
			}
		})
	}
}
