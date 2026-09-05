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
