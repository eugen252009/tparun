package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestReverseOrderPurchasesAreIndependentAndDeterministic(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "A-then-B", true: "B-then-A"}[reverse], func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
			until := now.AddDate(1, 0, 0)
			if err = s.CreateAccount(ctx, Account{ID: "orders", Email: "orders@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
				t.Fatal(err)
			}
			mk := func(id, offer, ref string, storage int64) BillingPurchase {
				return BillingPurchase{ID: id, AccountID: "orders", ProductID: "tpa-hub", PlanCode: offer, RateCardID: offer, RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: ref, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
			}
			a := mk("purchase-a", "offer-a", "ref-a", 10)
			b := mk("purchase-b", "offer-b", "ref-b", 100)
			if _, err = s.CreatePendingPurchase(ctx, a, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 10, EgressBytes: 20, OperationMetric: "a", OperationCount: 2}}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreatePendingPurchase(ctx, b, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 100, EgressBytes: 200, OperationMetric: "b", OperationCount: 4}}); err != nil {
				t.Fatal(err)
			}
			pay := func(p BillingPurchase) error {
				return s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-" + p.ID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: now}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency, ValidFrom: now, ValidUntil: until})
			}
			if reverse {
				if err = pay(b); err != nil {
					t.Fatal(err)
				}
				if err = pay(a); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = pay(a); err != nil {
					t.Fatal(err)
				}
				if err = pay(b); err != nil {
					t.Fatal(err)
				}
			}
			var n int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM purchase_grant_instances WHERE customer_id='orders'`).Scan(&n); err != nil || n != 2 {
				t.Fatalf("instances=%d err=%v", n, err)
			}
			g, err := s.GetGrant(ctx, "orders", commerce.ProductTPAHub, "repository.publish", now)
			if err != nil || g.Limits.StorageBytes != 100 || g.Limits.EgressBytes != 200 || g.Limits.OperationCount != 4 {
				t.Fatalf("effective=%+v err=%v", g, err)
			}
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			g, err = s.GetGrant(ctx, "orders", commerce.ProductTPAHub, "repository.publish", now)
			if err != nil || g.Limits.StorageBytes != 100 {
				t.Fatalf("restart effective=%+v err=%v", g, err)
			}
		})
	}
}

func TestGrandfatheredRightsSurvivePurchaseExpiryAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	legacyUntil := (*time.Time)(nil)
	if err = s.CreateAccount(ctx, Account{ID: "grandfather-matrix", Email: "grandfather-matrix@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}, commerce.Grant{CustomerID: "grandfather-matrix", ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), ValidUntil: legacyUntil, Limits: commerce.Limits{StorageBytes: 1000}, Source: "legacy", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	until := now.AddDate(1, 0, 0)
	p := BillingPurchase{ID: "grandfather-matrix-p", AccountID: "grandfather-matrix", ProductID: "tpa-hub", PlanCode: "nano", RateCardID: "nano", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "gf-ref", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.publish", Limits: commerce.Limits{StorageBytes: 10}}); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "gf-event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR", ValidFrom: now, ValidUntil: until}); err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrant(ctx, "grandfather-matrix", commerce.ProductTPAHub, "repository.publish", now)
	if err != nil || g.Limits.StorageBytes != 1000 {
		t.Fatalf("grandfathered effective=%+v err=%v", g, err)
	}
	if _, err = s.db.Exec(`UPDATE purchase_grant_instances SET valid_until=? WHERE purchase_id=?`, now.Add(-time.Minute).Format(time.RFC3339Nano), p.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g, err = s.GetGrant(ctx, "grandfather-matrix", commerce.ProductTPAHub, "repository.publish", now)
	if err != nil || g.Limits.StorageBytes != 1000 {
		t.Fatalf("restart fallback=%+v err=%v", g, err)
	}
}
