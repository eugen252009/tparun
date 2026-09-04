package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestCombinedGrandfatheringPurchasesConvergeAcrossPaymentAndExpiryOrder(t *testing.T) {
	for _, paymentReverse := range []bool{false, true} {
		for _, expiryReverse := range []bool{false, true} {
			name := map[bool]string{false: "pay-A-B", true: "pay-B-A"}[paymentReverse] + "/" + map[bool]string{false: "expire-A-B", true: "expire-B-A"}[expiryReverse]
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				s, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				now := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
				until := now.AddDate(1, 0, 0)
				legacy := []commerce.Grant{
					{CustomerID: "combined", ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), Limits: commerce.Limits{StorageBytes: 1000}, Source: "legacy", CreatedAt: now, UpdatedAt: now},
					{CustomerID: "combined", ProductID: commerce.ProductTPAHub, CapabilityID: "repository.download", OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), Limits: commerce.Limits{EgressBytes: 50}, Source: "legacy", CreatedAt: now, UpdatedAt: now},
					{CustomerID: "combined", ProductID: commerce.ProductTPAHub, CapabilityID: "repository.read.private", OfferID: "standard", Enabled: true, ValidFrom: now.Add(-time.Hour), Source: "legacy", CreatedAt: now, UpdatedAt: now},
				}
				if err = s.CreateAccount(ctx, Account{ID: "combined", Email: "combined@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}, legacy...); err != nil {
					t.Fatal(err)
				}
				mk := func(id, ref string) BillingPurchase {
					return BillingPurchase{ID: id, AccountID: "combined", ProductID: "tpa-hub", PlanCode: id, RateCardID: id, RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: ref, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
				}
				templates := func(mult int) []commerce.GrantTemplate {
					return []commerce.GrantTemplate{
						{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: int64(10 * mult), OperationMetric: "publish", OperationCount: int64(2 * mult)}},
						{CapabilityID: "repository.download", Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: int64(100 * mult)}},
						{CapabilityID: "repository.read.private", Period: commerce.PeriodAnnual},
					}
				}
				a, b := mk("purchase-a", "ref-a"), mk("purchase-b", "ref-b")
				if _, err = s.CreatePendingPurchase(ctx, a, now.Format(time.RFC3339Nano), templates(1)...); err != nil {
					t.Fatal(err)
				}
				if _, err = s.CreatePendingPurchase(ctx, b, now.Format(time.RFC3339Nano), templates(2)...); err != nil {
					t.Fatal(err)
				}
				pay := func(p BillingPurchase) {
					if err := s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-" + p.ID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: p.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR", ValidFrom: now, ValidUntil: until}); err != nil {
						t.Fatal(err)
					}
				}
				if paymentReverse {
					pay(b)
					pay(a)
				} else {
					pay(a)
					pay(b)
				}
				assertSources := func(activeA, activeB bool) {
					for _, capability := range []string{"repository.publish", "repository.download", "repository.read.private"} {
						var legacyCount, purchasedCount int
						if err := s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id='combined' AND capability_id=? AND source='legacy' AND purchase_id=''`, capability).Scan(&legacyCount); err != nil || legacyCount != 1 {
							t.Fatalf("%s legacy=%d err=%v", capability, legacyCount, err)
						}
						if err := s.db.QueryRow(`SELECT COUNT(*) FROM purchase_grant_instances WHERE customer_id='combined' AND capability_id=? AND purchase_id IN ('purchase-a','purchase-b')`, capability).Scan(&purchasedCount); err != nil || purchasedCount != 2 {
							t.Fatalf("%s purchases=%d err=%v", capability, purchasedCount, err)
						}
					}
					for _, id := range []string{"purchase-a", "purchase-b"} {
						var status string
						if err := s.db.QueryRow(`SELECT status FROM billing_purchases WHERE id=?`, id).Scan(&status); err != nil || status != "paid" {
							t.Fatalf("%s status=%q err=%v", id, status, err)
						}
					}
					wantEgress := int64(50)
					if activeA {
						wantEgress = 100
					}
					if activeB {
						wantEgress = 200
					}
					publish, e := s.GetGrant(ctx, "combined", commerce.ProductTPAHub, "repository.publish", now)
					if e != nil || publish.Limits.StorageBytes != 1000 {
						t.Fatalf("publish=%+v err=%v", publish, e)
					}
					download, e := s.GetGrant(ctx, "combined", commerce.ProductTPAHub, "repository.download", now)
					if e != nil || download.Limits.EgressBytes != wantEgress {
						t.Fatalf("download=%+v want=%d err=%v", download, wantEgress, e)
					}
				}
				assertSources(true, true)
				s.Close()
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				assertSources(true, true)
				expire := func(id string) {
					if _, err := s.db.Exec(`UPDATE purchase_grant_instances SET valid_until=? WHERE purchase_id=?`, now.Add(-time.Minute).Format(time.RFC3339Nano), id); err != nil {
						t.Fatal(err)
					}
				}
				first, second := "purchase-a", "purchase-b"
				activeA, activeB := false, true
				if expiryReverse {
					first, second = "purchase-b", "purchase-a"
					activeA, activeB = true, false
				}
				expire(first)
				assertSources(activeA, activeB)
				s.Close()
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				assertSources(activeA, activeB)
				expire(second)
				assertSources(false, false)
				s.Close()
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				assertSources(false, false)
			})
		}
	}
}
