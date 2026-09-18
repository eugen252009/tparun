package store

import (
	"context"
	"errors"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"testing"
	"time"
)

func TestSchema5CanonicalGrantResolution(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                     string
		local, paid, admin, want int64
		at                       time.Time
		disabled                 bool
	}{
		{"local-only", 10, -1, -1, 10, start, false},
		{"paid-only", -1, 5, -1, 5, start, false},
		{"local-independent-of-smaller-paid", 10, 5, -1, 10, start, false},
		{"larger-paid", 5, 10, -1, 10, start, false},
		{"expired-paid", 10, 5, -1, 10, start.AddDate(1, 0, 0), false},
		{"disabled-local", 10, 5, -1, 5, start, true},
		{"multiple-sources", 10, 5, 20, 20, start, false},
		{"future-grants", 10, 5, -1, -1, start.Add(-time.Nanosecond), false},
		{"expired-paid-only", -1, 5, -1, -1, start.AddDate(1, 0, 0), false},
		{"disabled-only", 10, -1, -1, -1, start, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			ctx := context.Background()
			stamp := start.Format(time.RFC3339Nano)
			if err = s.CreateAccount(ctx, Account{ID: "owner", Email: "owner@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: stamp}); err != nil {
				t.Fatal(err)
			}
			for source, limit := range map[string]int64{"local": tc.local, "administrative": tc.admin} {
				if limit >= 0 {
					g := commerce.Grant{CustomerID: "owner", ProductID: tpahubproduct.ProductID, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: source, Enabled: !(source == "local" && tc.disabled), ValidFrom: start, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: limit}, Source: source, CreatedAt: start, UpdatedAt: start}
					if err = s.PutGrant(ctx, g); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.paid >= 0 {
				p := BillingPurchase{ID: "paid", AccountID: "owner", ProductID: "tpa-hub", PlanCode: "paid", RateCardID: "paid", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: stamp, EntitlementPeriodEnd: start.AddDate(1, 0, 0).Format(time.RFC3339Nano), ProviderReference: "reference", CreatedAt: stamp, UpdatedAt: stamp}
				_, err = s.CreatePendingPurchase(ctx, p, stamp, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: tc.paid}})
				if err != nil {
					t.Fatal(err)
				}
				if err = s.UpdatePurchaseCheckout(ctx, p.ID, p.ProviderReference, "https://checkout.invalid/fixture", stamp); err != nil {
					t.Fatal(err)
				}
				if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: start}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				var service commerce.GrantService = s
				g, err := service.GetGrant(ctx, "owner", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, tc.at)
				if tc.want < 0 {
					if !errors.Is(err, commerce.ErrGrantInactive) && !errors.Is(err, commerce.ErrGrantDisabled) {
						t.Fatalf("unexpected denial: %v", err)
					}
				} else {
					if err != nil || g.Limits.StorageBytes != tc.want {
						t.Fatalf("grant=%+v err=%v", g, err)
					}
					from, until, err := g.PeriodAt(tc.at)
					if err != nil || tc.at.Before(from) || !tc.at.Before(until) {
						t.Fatalf("invalid effective period %v %v %v", from, until, err)
					}
				}
				s.Close()
				if err = Migrate(dir); err != nil {
					t.Fatal(err)
				}
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSchema5HasNoCompatibilityProjection(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('account_entitlements','schema_migrations')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("obsolete authority count=%d err=%v", count, err)
	}
}
