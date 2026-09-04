package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestCorruptedFinalizedSnapshotCannotActivatePartially(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Store) error
		blocked bool
	}{
		{"extra row", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_no_late_insert; INSERT INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence) VALUES('snapshot-matrix-p','unexpected','annual')`)
			return e
		}, false},
		{"missing row", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_immutable_delete; DELETE FROM purchase_grant_snapshots WHERE capability_id='repository.download'`)
			return e
		}, false},
		{"altered capability", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_immutable_update; UPDATE purchase_grant_snapshots SET capability_id='unexpected' WHERE capability_id='repository.download'`)
			return e
		}, false},
		{"altered storage", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_immutable_update; UPDATE purchase_grant_snapshots SET storage_limit_bytes=999 WHERE capability_id='repository.publish'`)
			return e
		}, false},
		{"altered egress", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_immutable_update; UPDATE purchase_grant_snapshots SET egress_limit_bytes=999 WHERE capability_id='repository.download'`)
			return e
		}, false},
		{"altered metric/count", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_immutable_update; UPDATE purchase_grant_snapshots SET operation_metric='wrong',operation_limit=999 WHERE capability_id='repository.publish'`)
			return e
		}, false},
		{"altered cadence", func(s *Store) error {
			_, e := s.db.Exec(`DROP TRIGGER purchase_grant_snapshots_immutable_update; UPDATE purchase_grant_snapshots SET period_cadence='monthly' WHERE capability_id='repository.publish'`)
			return e
		}, false},
		{"expected count mismatch", func(s *Store) error {
			_, e := s.db.Exec(`UPDATE purchase_grant_snapshot_sets SET expected_count=99`)
			return e
		}, false},
		{"expected digest mismatch", func(s *Store) error {
			_, e := s.db.Exec(`UPDATE purchase_grant_snapshot_sets SET capability_digest='bad'`)
			return e
		}, false},
		{"offer identity finalized", func(s *Store) error { _, e := s.db.Exec(`UPDATE billing_purchases SET plan_code='other'`); return e }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, purchase, now := snapshotActivationFixture(t)
			defer s.Close()
			mutationErr := tc.mutate(s)
			if tc.blocked {
				if mutationErr == nil {
					t.Fatal("finalized commercial identity mutation unexpectedly succeeded")
				}
				return
			}
			if mutationErr != nil {
				t.Fatal(mutationErr)
			}
			if err := s.ApplyPaymentTransition(context.Background(), paymentForSnapshot(purchase, now)); err == nil {
				t.Fatal("corrupt snapshot activated")
			}
			var n int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM purchase_grant_instances WHERE purchase_id=?`, purchase.ID).Scan(&n); err != nil || n != 0 {
				t.Fatalf("partial instances=%d err=%v", n, err)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM billing_purchases WHERE id=?`, purchase.ID).Scan(&status); err != nil || status != "pending" {
				t.Fatalf("purchase status=%s err=%v", status, err)
			}
		})
	}
}

func snapshotActivationFixture(t *testing.T) (*Store, BillingPurchase, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	until := now.AddDate(1, 0, 0)
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "snapshot-matrix", Email: "snapshot-matrix@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p := BillingPurchase{ID: "snapshot-matrix-p", AccountID: "snapshot-matrix", ProductID: "tpa-hub", PlanCode: "offer", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 10, OperationMetric: "ops", OperationCount: 2}}, commerce.GrantTemplate{CapabilityID: "repository.download", Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: 20}}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, p.ID, "provider-ref", "https://checkout", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return s, p, now
}
func paymentForSnapshot(p BillingPurchase, now time.Time) commerce.PaymentTransition {
	return commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "snapshot-event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "provider-ref", AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR", ValidFrom: now, ValidUntil: now.AddDate(1, 0, 0)}
}
