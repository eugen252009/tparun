package store

import (
	"context"
	"errors"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestOperationAggregationRequiresCompatibleMetric(t *testing.T) {
	for _, tc := range []struct {
		name, metricB string
		countB, want  int64
		wantErr       bool
	}{
		{"same-metric-different-count", "ops", 20, 20, false},
		{"different-metric-same-count", "other", 10, 0, true},
		{"different-metric-different-count", "other", 20, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			until := now.AddDate(1, 0, 0)
			if err = s.CreateAccount(ctx, Account{ID: "ops", Email: "ops@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
				t.Fatal(err)
			}
			makePurchase := func(id, metric string, count int64) BillingPurchase {
				p := BillingPurchase{ID: id, AccountID: "ops", ProductID: "tpa-hub", PlanCode: id, RateCardID: id, RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "ref-" + id, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
				if _, e := s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{OperationMetric: commerce.MetricID(metric), OperationCount: count}}); e != nil {
					t.Fatal(e)
				}
				if e := s.UpdatePurchaseCheckout(ctx, p.ID, p.ProviderReference, "https://checkout.invalid/"+id, p.UpdatedAt); e != nil {
					t.Fatal(e)
				}
				return p
			}
			a, b := makePurchase("a", "ops", 10), makePurchase("b", tc.metricB, tc.countB)
			pay := func(p BillingPurchase) {
				if e := s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-" + p.ID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); e != nil {
					t.Fatal(e)
				}
			}
			pay(a)
			pay(b)
			g, err := s.GetGrant(ctx, "ops", tpahubproduct.ProductID, "repository.publish", now)
			if tc.wantErr {
				if !errors.Is(err, commerce.ErrIncompatibleGrantMetrics) {
					t.Fatalf("metric conflict err=%v grant=%+v", err, g)
				}
				return
			}
			if err != nil || g.Limits.OperationMetric != "ops" || g.Limits.OperationCount != tc.want || g.Effective == nil || len(g.Effective.Contributors) != 2 || g.OfferID != "" || g.PurchaseID != "" || g.Period != "" {
				t.Fatalf("effective=%+v err=%v", g, err)
			}
		})
	}
}

func TestEffectiveGrantPeriodUsesExplicitLimitContributor(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "period-basis", Email: "period-basis@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	makePurchase := func(id string, cadence commerce.PeriodCadence, from, until time.Time, egress int64) BillingPurchase {
		purchase := BillingPurchase{ID: id, AccountID: "period-basis", ProductID: "tpa-hub", PlanCode: id, RateCardID: id, RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "ref-" + id, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
		if _, createErr := s.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.download", Period: cadence, Limits: commerce.Limits{EgressBytes: egress}}); createErr != nil {
			t.Fatal(createErr)
		}
		if checkoutErr := s.UpdatePurchaseCheckout(ctx, id, purchase.ProviderReference, "https://checkout.invalid/"+id, purchase.UpdatedAt); checkoutErr != nil {
			t.Fatal(checkoutErr)
		}
		return purchase
	}
	pay := func(purchase BillingPurchase) {
		if paymentErr := s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "event-" + purchase.ID, OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); paymentErr != nil {
			t.Fatal(paymentErr)
		}
	}
	annualFrom, annualUntil := now.Add(-24*time.Hour), now.AddDate(1, 0, 0)
	monthlyFrom, monthlyUntil := now.Add(-time.Hour), now.AddDate(0, 1, 0)
	annual := makePurchase("annual", commerce.PeriodAnnual, annualFrom, annualUntil, 100)
	monthly := makePurchase("monthly", commerce.PeriodMonthly, monthlyFrom, monthlyUntil, 200)
	pay(annual)
	pay(monthly)
	grant, err := s.GetGrant(ctx, "period-basis", tpahubproduct.ProductID, "repository.download", now)
	if err != nil || grant.Effective == nil || grant.Period != "" || grant.OfferID != "" || grant.PurchaseID != "" || !grant.ValidFrom.IsZero() || grant.ValidUntil != nil {
		t.Fatalf("mixed commercial metadata=%+v err=%v", grant, err)
	}
	basis := grant.Effective.Contributors[grant.Effective.PeriodContributor]
	if basis.PurchaseID != "monthly" || basis.Limits.EgressBytes != 200 {
		t.Fatalf("period basis=%+v", basis)
	}
	from, until, err := grant.PeriodAt(now)
	if err != nil || !from.Equal(monthlyFrom) || !until.Equal(monthlyUntil) {
		t.Fatalf("effective period=%s..%s err=%v", from, until, err)
	}
}
