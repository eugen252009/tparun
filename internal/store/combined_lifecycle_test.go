package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestCombinedGrandfatheringTwoPurchasesExpiryAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	until := now.AddDate(1, 0, 0)
	caps := []string{"repository.publish", "repository.download", "repository.read.private"}
	grants := make([]commerce.Grant, 0, 3)
	for _, cap := range caps {
		grants = append(grants, commerce.Grant{CustomerID: "combined", ProductID: commerce.ProductTPAHub, CapabilityID: commerce.CapabilityID(cap), OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), Limits: commerce.Limits{StorageBytes: 1000, EgressBytes: 2000, OperationCount: 100}, Source: "legacy", CreatedAt: now, UpdatedAt: now})
	}
	if err = s.CreateAccount(ctx, Account{ID: "combined", Email: "combined@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}, grants...); err != nil {
		t.Fatal(err)
	}
	mk := func(id, ref string) BillingPurchase {
		return BillingPurchase{ID: id, AccountID: "combined", ProductID: "tpa-hub", PlanCode: id, RateCardID: id, RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: ref, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	}
	templates := func(mult int) []commerce.GrantTemplate {
		return []commerce.GrantTemplate{{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: int64(10 * mult), OperationCount: int64(2 * mult)}}, {CapabilityID: "repository.download", Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: int64(20 * mult)}}, {CapabilityID: "repository.read.private", Period: commerce.PeriodAnnual}}
	}
	a, b := mk("purchase-a", "ref-a"), mk("purchase-b", "ref-b")
	if _, err = s.CreatePendingPurchase(ctx, a, now.Format(time.RFC3339Nano), templates(1)...); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePendingPurchase(ctx, b, now.Format(time.RFC3339Nano), templates(2)...); err != nil {
		t.Fatal(err)
	}
	pay := func(p BillingPurchase) error {
		return s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-" + p.ID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR", ValidFrom: now, ValidUntil: until})
	}
	if err = pay(b); err != nil {
		t.Fatal(err)
	}
	if err = pay(a); err != nil {
		t.Fatal(err)
	}
	assert := func(want int64) {
		for _, cap := range caps {
			var n int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM purchase_grant_instances WHERE customer_id=? AND capability_id=?`, "combined", cap).Scan(&n); err != nil || n != 2 {
				t.Fatalf("%s source count=%d err=%v", cap, n, err)
			}
			g, e := s.GetGrant(ctx, "combined", commerce.ProductTPAHub, commerce.CapabilityID(cap), now)
			if e != nil || (cap == "repository.publish" && g.Limits.StorageBytes < want) {
				t.Fatalf("%s effective=%+v err=%v", cap, g, e)
			}
		}
	}
	assert(1000)
	if _, err = s.db.Exec(`UPDATE purchase_grant_instances SET valid_until=? WHERE purchase_id=?`, now.Add(-time.Minute).Format(time.RFC3339Nano), a.ID); err != nil {
		t.Fatal(err)
	}
	assert(1000)
	if _, err = s.db.Exec(`UPDATE purchase_grant_instances SET valid_until=? WHERE purchase_id=?`, now.Add(-time.Minute).Format(time.RFC3339Nano), b.ID); err != nil {
		t.Fatal(err)
	}
	assert(1000)
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assert(1000)
}
