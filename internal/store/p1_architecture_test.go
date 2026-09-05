package store

import (
	"context"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/commerce"
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
