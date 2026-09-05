package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestGenericPersistenceRejectsMissingProductIdentity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err = s.CreatePendingPurchase(ctx, BillingPurchase{ID: "missing-product", AccountID: "account", PlanCode: "offer", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "mock", Status: "pending", EntitlementPeriodStart: "2026-01-01T00:00:00Z", EntitlementPeriodEnd: "2027-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}, "2026-01-01T00:00:00Z"); err == nil || !strings.Contains(err.Error(), "product ID is empty") {
		t.Fatalf("missing purchase product was accepted: %v", err)
	}
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "account", CapabilityID: "capability", OfferID: "offer", Enabled: true, Period: commerce.PeriodAnnual}); err == nil || !strings.Contains(err.Error(), "product ID is empty") {
		t.Fatalf("missing grant product was accepted: %v", err)
	}
	if err = s.SaveProviderSubmission(ctx, BillingProviderSubmission{AccountID: "account", Period: "2026-01", Provider: "mock", Environment: "test", Status: "submitted"}); err == nil || !strings.Contains(err.Error(), "product ID is empty") {
		t.Fatalf("missing submission product was accepted: %v", err)
	}
}

func TestProductAwareMockBillingRejectsMissingProductIdentity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err = s.ProductMockBillingEventPayload(ctx, "account", " ", "2026-01", "period-finalized"); err == nil || !strings.Contains(err.Error(), "product ID is empty") {
		t.Fatalf("missing mock payload product was accepted: %v", err)
	}
	if err = s.RecordProductMockBillingEvent(ctx, "empty-product-event", "account", "", "2026-01", "period-finalized", `{}`, "2026-01-01T00:00:00Z"); err == nil || !strings.Contains(err.Error(), "product ID is empty") {
		t.Fatalf("missing mock event product was accepted: %v", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mock_billing_events WHERE id=?`, "empty-product-event").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid mock event was persisted: %d", count)
	}
}

func TestCreateAccountRejectsMissingGrantProductIdentityAtomically(t *testing.T) {
	ctx := context.Background()
	validGrant := func(product commerce.ProductID) commerce.Grant {
		return commerce.Grant{ProductID: product, CapabilityID: "capability", OfferID: "offer", Enabled: true, ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Period: commerce.PeriodAnnual, Source: "test", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	for _, tc := range []struct {
		name   string
		grants []commerce.Grant
	}{
		{name: "first", grants: []commerce.Grant{validGrant(""), validGrant("product-x"), validGrant("product-y")}},
		{name: "middle", grants: []commerce.Grant{validGrant("product-x"), validGrant(""), validGrant("product-y")}},
		{name: "last", grants: []commerce.Grant{validGrant("product-x"), validGrant("product-y"), validGrant("")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.CreateAccount(ctx, Account{ID: "invalid-" + tc.name, Email: tc.name + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}, tc.grants...); err == nil || !strings.Contains(err.Error(), "grant product ID is empty") {
				t.Fatalf("missing grant product was accepted: %v", err)
			}
			var accounts, grants int
			if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id=?`, "invalid-"+tc.name).Scan(&accounts); err != nil {
				t.Fatal(err)
			}
			if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_grants WHERE customer_id=?`, "invalid-"+tc.name).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if accounts != 0 || grants != 0 {
				t.Fatalf("invalid account mutation: accounts=%d grants=%d", accounts, grants)
			}
		})
	}
}

func TestCreateAccountPreservesExplicitGrantProducts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grants := []commerce.Grant{
		{ProductID: "product-x", CapabilityID: "x", OfferID: "offer-x", Enabled: true, ValidFrom: created, Period: commerce.PeriodAnnual, Source: "test", CreatedAt: created, UpdatedAt: created},
		{ProductID: tpahubproduct.ProductID, CapabilityID: "tpa", OfferID: "offer-tpa", Enabled: true, ValidFrom: created, Period: commerce.PeriodAnnual, Source: "test", CreatedAt: created, UpdatedAt: created},
	}
	if err = s.CreateAccount(ctx, Account{ID: "explicit-products", Email: "explicit@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: created.Format(time.RFC3339Nano)}, grants...); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_grants WHERE customer_id=? AND product_id=?`, "explicit-products", "product-x").Scan(&count); err != nil || count != 1 {
		t.Fatalf("product-x grant count=%d err=%v", count, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_grants WHERE customer_id=? AND product_id=?`, "explicit-products", string(tpahubproduct.ProductID)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("TPA grant count=%d err=%v", count, err)
	}
}

func TestProductAwareMockBillingRoundTripPreservesProductIdentity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "account", Email: "account@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordProductMockBillingEvent(ctx, "product-x-event", "account", "product-x", "2026-01", "period-finalized", `{"product":"x"}`, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	payload, err := s.ProductMockBillingEventPayload(ctx, "account", "product-x", "2026-01", "period-finalized")
	if err != nil || payload != `{"product":"x"}` {
		t.Fatalf("product-aware mock roundtrip payload=%q err=%v", payload, err)
	}
}
