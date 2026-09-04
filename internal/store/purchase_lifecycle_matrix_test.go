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
			if _, err = s.CreatePendingPurchase(ctx, a, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 10, EgressBytes: 20, OperationMetric: "ops", OperationCount: 2}}); err != nil {
				t.Fatal(err)
			}
			if err = s.UpdatePurchaseCheckout(ctx, a.ID, a.ProviderReference, "https://checkout.invalid/"+a.ID, a.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreatePendingPurchase(ctx, b, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 100, EgressBytes: 200, OperationMetric: "ops", OperationCount: 4}}); err != nil {
				t.Fatal(err)
			}
			if err = s.UpdatePurchaseCheckout(ctx, b.ID, b.ProviderReference, "https://checkout.invalid/"+b.ID, b.UpdatedAt); err != nil {
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
