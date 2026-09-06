package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func r11PurchaseFixture(t *testing.T, dir, id, account, environment string) (*Store, BillingPurchase) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: account, Email: account + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	p := BillingPurchase{ID: id, AccountID: account, ProductID: string(tpahubproduct.ProductID), PlanCode: "nano", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: environment, Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: from.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	templates := []commerce.GrantTemplate{
		{CapabilityID: tpahubproduct.CapabilityRepositoryCreate, Period: commerce.PeriodAnnual},
		{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 5_000_000}},
		{CapabilityID: tpahubproduct.CapabilityRepositoryDownload, Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: 12_000_000_000}},
	}
	if _, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, templates...); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, p
}

func r11DropSnapshotMutationTriggers(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`DROP TRIGGER IF EXISTS purchase_grant_snapshots_immutable_update; DROP TRIGGER IF EXISTS purchase_grant_snapshots_immutable_delete; DROP TRIGGER IF EXISTS purchase_grant_snapshot_sets_immutable_update; DROP TRIGGER IF EXISTS purchase_grant_snapshot_sets_immutable_delete`); err != nil {
		t.Fatal(err)
	}
}

func r11AssertReconciledPayment(t *testing.T, s *Store, p BillingPurchase, reference, eventID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.RecoverPurchaseCheckout(ctx, p.ID, p.Provider, p.BillingEnvironment, reference, "2040-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Purchase(ctx, p.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderReference != reference || stored.ReconciliationReason == "" {
		t.Fatalf("recovery state=%+v err=%v", stored, err)
	}
	var finalized int
	if err = s.db.QueryRow(`SELECT snapshot_finalized FROM billing_purchases WHERE id=?`, p.ID).Scan(&finalized); err != nil || finalized != 0 {
		t.Fatalf("incomplete authority finalized=%d err=%v", finalized, err)
	}
	transition := commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: eventID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: reference, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency}
	if err = s.ApplyPaymentTransition(ctx, transition); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Purchase(ctx, p.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderState != "provider_success_unreconciled" || stored.EntitlementPeriodStart != p.EntitlementPeriodStart || stored.EntitlementPeriodEnd != p.EntitlementPeriodEnd || stored.PlanCode != p.PlanCode {
		t.Fatalf("payment reconciliation state=%+v err=%v", stored, err)
	}
	if _, err = s.db.Exec(`UPDATE billing_purchases SET reconciliation_reason='changed' WHERE id=?`, p.ID); err == nil {
		t.Fatal("reconciliation reason was mutable")
	}
	if _, err = s.db.Exec(`INSERT INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence) VALUES(?, 'attempted.repair', 'annual')`, p.ID); err == nil {
		t.Fatal("reconciliation snapshot authority accepted a late insert")
	}
	var status, evidencePurchase, evidenceReference, evidenceError string
	if err = s.db.QueryRow(`SELECT status,purchase_id,provider_reference,error FROM billing_provider_events WHERE provider=? AND environment=? AND provider_event_id=?`, p.Provider, p.BillingEnvironment, eventID).Scan(&status, &evidencePurchase, &evidenceReference, &evidenceError); err != nil || status != "reconciliation_required" || evidencePurchase != p.ID || evidenceReference != reference || evidenceError == "" {
		t.Fatalf("provider evidence status=%q purchase=%q reference=%q error=%q err=%v", status, evidencePurchase, evidenceReference, evidenceError, err)
	}
	if count, countErr := s.PurchaseGrantInstanceCount(ctx, p.AccountID); countErr != nil || count != 0 {
		t.Fatalf("grant instances=%d err=%v", count, countErr)
	}
	for _, capability := range []commerce.CapabilityID{tpahubproduct.CapabilityRepositoryCreate, tpahubproduct.CapabilityRepositoryPublish, tpahubproduct.CapabilityRepositoryDownload, tpahubproduct.CapabilityRepositoryReadPrivate} {
		if _, grantErr := s.GetGrant(ctx, commerce.CustomerID(p.AccountID), tpahubproduct.ProductID, capability, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(grantErr, commerce.ErrGrantNotFound) {
			t.Fatalf("reconciliation capability %s access=%v", capability, grantErr)
		}
	}
	var projection int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM account_entitlements WHERE account_id=? AND source='purchase'`, p.AccountID).Scan(&projection); err != nil || projection != 0 {
		t.Fatalf("compatibility projection=%d err=%v", projection, err)
	}
	for i := 0; i < 2; i++ {
		fresh := p
		fresh.ID = p.ID + "-blocked-" + string(rune('a'+i))
		fresh.CreatedAt, fresh.UpdatedAt = "2030-01-01T00:00:00Z", "2030-01-01T00:00:00Z"
		fresh.EntitlementPeriodStart, fresh.EntitlementPeriodEnd = fresh.CreatedAt, "2031-01-01T00:00:00Z"
		if _, createErr := s.CreatePendingPurchase(ctx, fresh, fresh.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual}); createErr == nil || !strings.Contains(createErr.Error(), "purchase-reconciliation-required") {
			t.Fatalf("reconciliation did not block retry %d: %v", i, createErr)
		}
	}
}

func TestRecoveryIncompleteCommercialAuthorityMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *Store, BillingPurchase)
	}{
		{"missing-snapshot-set", func(t *testing.T, s *Store, p BillingPurchase) {
			r11DropSnapshotMutationTriggers(t, s)
			_, err := s.db.Exec(`DELETE FROM purchase_grant_snapshot_sets WHERE purchase_id=?`, p.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"missing-snapshot-row", func(t *testing.T, s *Store, p BillingPurchase) {
			r11DropSnapshotMutationTriggers(t, s)
			_, err := s.db.Exec(`DELETE FROM purchase_grant_snapshots WHERE purchase_id=? AND capability_id=?`, p.ID, tpahubproduct.CapabilityRepositoryDownload)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"extra-snapshot-row", func(t *testing.T, s *Store, p BillingPurchase) {
			_, err := s.db.Exec(`INSERT INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence) VALUES(?,?,?)`, p.ID, tpahubproduct.CapabilityRepositoryReadPrivate, commerce.PeriodAnnual)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"snapshot-digest-mismatch", func(t *testing.T, s *Store, p BillingPurchase) {
			r11DropSnapshotMutationTriggers(t, s)
			_, err := s.db.Exec(`UPDATE purchase_grant_snapshot_sets SET capability_digest='wrong' WHERE purchase_id=?`, p.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong-purchase-snapshot-rows", func(t *testing.T, s *Store, p BillingPurchase) {
			r11PurchaseFixtureOnStore(t, s, p.ID+"-other", p.AccountID+"-other", p.BillingEnvironment)
			r11DropSnapshotMutationTriggers(t, s)
			_, err := s.db.Exec(`DELETE FROM purchase_grant_snapshots WHERE purchase_id=?`, p.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},

		{"wrong-product-capability", func(t *testing.T, s *Store, p BillingPurchase) {
			r11DropSnapshotMutationTriggers(t, s)
			if _, err := s.db.Exec(`UPDATE purchase_grant_snapshots SET capability_id='other.product.execute' WHERE purchase_id=? AND capability_id=?`, p.ID, tpahubproduct.CapabilityRepositoryCreate); err != nil {
				t.Fatal(err)
			}
			var templates []commerce.GrantTemplate
			rows, err := s.db.Query(`SELECT capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit FROM purchase_grant_snapshots WHERE purchase_id=?`, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var x commerce.GrantTemplate
				if err = rows.Scan(&x.CapabilityID, &x.Period, &x.Limits.StorageBytes, &x.Limits.EgressBytes, &x.Limits.OperationMetric, &x.Limits.OperationCount); err != nil {
					t.Fatal(err)
				}
				templates = append(templates, x)
			}
			rows.Close()
			if _, err = s.db.Exec(`UPDATE purchase_grant_snapshot_sets SET capability_digest=? WHERE purchase_id=?`, snapshotDigest(templates), p.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid-cadence", func(t *testing.T, s *Store, p BillingPurchase) {
			r11DropSnapshotMutationTriggers(t, s)
			if _, err := s.db.Exec(`UPDATE purchase_grant_snapshots SET period_cadence='fortnightly' WHERE purchase_id=? AND capability_id=?`, p.ID, tpahubproduct.CapabilityRepositoryCreate); err != nil {
				t.Fatal(err)
			}
			r11RefreshSnapshotDigest(t, s, p.ID)
		}},
		{"invalid-operation-metric", func(t *testing.T, s *Store, p BillingPurchase) {
			r11DropSnapshotMutationTriggers(t, s)
			if _, err := s.db.Exec(`UPDATE purchase_grant_snapshots SET operation_metric='',operation_limit=1 WHERE purchase_id=? AND capability_id=?`, p.ID, tpahubproduct.CapabilityRepositoryPublish); err != nil {
				t.Fatal(err)
			}
			r11RefreshSnapshotDigest(t, s, p.ID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p := r11PurchaseFixture(t, t.TempDir(), "purchase-"+tc.name, "account-"+tc.name, "test")
			defer s.Close()
			tc.mutate(t, s, p)
			r11AssertReconciledPayment(t, s, p, "ref-"+tc.name, "evt-"+tc.name)
		})
	}
}

func r11RefreshSnapshotDigest(t *testing.T, s *Store, purchaseID string) {
	t.Helper()
	var templates []commerce.GrantTemplate
	rows, err := s.db.Query(`SELECT capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit FROM purchase_grant_snapshots WHERE purchase_id=?`, purchaseID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var x commerce.GrantTemplate
		if err = rows.Scan(&x.CapabilityID, &x.Period, &x.Limits.StorageBytes, &x.Limits.EgressBytes, &x.Limits.OperationMetric, &x.Limits.OperationCount); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		templates = append(templates, x)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE purchase_grant_snapshot_sets SET capability_digest=? WHERE purchase_id=?`, snapshotDigest(templates), purchaseID); err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationEvidencePrecedenceReplayAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, p := r11PurchaseFixture(t, dir, "reconcile-restart", "reconcile-restart-account", "test")
	r11DropSnapshotMutationTriggers(t, s)
	if _, err := s.db.Exec(`DELETE FROM purchase_grant_snapshot_sets WHERE purchase_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	r11AssertReconciledPayment(t, s, p, "ref-reconcile-restart", "evt-success")
	transition := func(id, kind string, typ commerce.PaymentEventType) commerce.PaymentTransition {
		return commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: id, OperationID: p.ID, Type: typ, ProviderReference: "ref-reconcile-restart", AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Date(2045, 1, 1, 0, 0, 0, 0, time.UTC)}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: kind, AmountMinor: p.AmountMinor, Currency: p.Currency}
	}
	if err := s.ApplyPaymentTransition(context.Background(), transition("evt-success", "success", commerce.PaymentSucceeded)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPaymentTransition(context.Background(), transition("evt-failure", "failed", commerce.PaymentFailed)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPaymentTransition(context.Background(), transition("evt-expiry", "expired", commerce.PaymentExpired)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPaymentTransition(context.Background(), transition("evt-equivalent-success", "success", commerce.PaymentSucceeded)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stored, err := s.Purchase(context.Background(), p.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderState != "provider_success_unreconciled" {
		t.Fatalf("restart state=%+v err=%v", stored, err)
	}
	var events int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM billing_provider_events WHERE purchase_id=?`, p.ID).Scan(&events); err != nil || events != 4 {
		t.Fatalf("events=%d err=%v", events, err)
	}
	if count, countErr := s.PurchaseGrantInstanceCount(context.Background(), p.AccountID); countErr != nil || count != 0 {
		t.Fatalf("instances=%d err=%v", count, countErr)
	}
	fresh := p
	fresh.ID = "blocked-after-restart"
	fresh.CreatedAt = "2030-01-01T00:00:00Z"
	fresh.UpdatedAt = fresh.CreatedAt
	fresh.EntitlementPeriodStart = fresh.CreatedAt
	fresh.EntitlementPeriodEnd = "2031-01-01T00:00:00Z"
	if _, err = s.CreatePendingPurchase(context.Background(), fresh, fresh.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual}); err == nil || !strings.Contains(err.Error(), "purchase-reconciliation-required") {
		t.Fatalf("restart checkout block=%v", err)
	}
}

func TestProviderReferenceUniquenessAndRecoveryConflict(t *testing.T) {
	dir := t.TempDir()
	s, a := r11PurchaseFixture(t, dir, "reference-a", "reference-account-a", "test")
	defer s.Close()
	ctx := context.Background()
	_, b := r11PurchaseFixtureOnStore(t, s, "reference-b", "reference-account-b", "test")
	if err := s.UpdatePurchaseCheckout(ctx, a.ID, "shared-reference", "https://checkout.invalid/a", a.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePurchaseCheckout(ctx, b.ID, "shared-reference", "https://checkout.invalid/b", b.UpdatedAt); err == nil {
		t.Fatal("duplicate provider reference update succeeded")
	}
	storedB, _ := s.Purchase(ctx, b.ID)
	if storedB.ProviderReference != "" {
		t.Fatalf("conflicting purchase linked reference: %+v", storedB)
	}
	if err := s.RecoverPurchaseCheckout(ctx, b.ID, b.Provider, b.BillingEnvironment, "shared-reference", b.UpdatedAt); err == nil {
		t.Fatal("recovery stole provider reference")
	}
	storedA, _ := s.Purchase(ctx, a.ID)
	if storedA.ProviderReference != "shared-reference" {
		t.Fatalf("owner changed: %+v", storedA)
	}
	if err := s.RecoverPurchaseCheckout(ctx, a.ID, a.Provider, a.BillingEnvironment, "shared-reference", a.UpdatedAt); err != nil {
		t.Fatalf("same-purchase replay: %v", err)
	}
	_, live := r11PurchaseFixtureOnStore(t, s, "reference-live", "reference-account-live", "live")
	if err := s.UpdatePurchaseCheckout(ctx, live.ID, "shared-reference", "https://checkout.invalid/live", live.UpdatedAt); err != nil {
		t.Fatalf("environment-scoped reference collided: %v", err)
	}
	_, otherProvider := r11PurchaseFixtureOnStore(t, s, "reference-other-provider", "reference-account-other-provider", "test")
	if _, err := s.db.Exec(`UPDATE billing_purchases SET provider='other-provider' WHERE id=?`, otherProvider.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePurchaseCheckout(ctx, otherProvider.ID, "shared-reference", "https://checkout.invalid/other-provider", otherProvider.UpdatedAt); err != nil {
		t.Fatalf("provider-scoped reference collided: %v", err)
	}
}

func r11PurchaseFixtureOnStore(t *testing.T, s *Store, id, account, environment string) (*Store, BillingPurchase) {
	t.Helper()
	ctx := context.Background()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.CreateAccount(ctx, Account{ID: account, Email: account + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p := BillingPurchase{ID: id, AccountID: account, ProductID: string(tpahubproduct.ProductID), PlanCode: "nano", RateCardID: "rate", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: environment, Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: from.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	if _, err := s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual}); err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestProviderReferenceUniqueIndexUpgradeMatrix(t *testing.T) {
	t.Run("nullable-historical-references", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "nullable.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err = db.Exec(`CREATE TABLE billing_purchases(provider TEXT NOT NULL,billing_environment TEXT NOT NULL,provider_reference TEXT); INSERT INTO billing_purchases VALUES('stripe','test',NULL),('stripe','test',NULL),('stripe','test',''),('stripe','test','')`); err != nil {
			t.Fatal(err)
		}
		if err = enforceUniquePurchaseProviderReferences(db); err != nil {
			t.Fatalf("nullable/empty references failed migration: %v", err)
		}
		if _, err = db.Exec(`INSERT INTO billing_purchases VALUES('stripe','test',NULL),('stripe','test','')`); err != nil {
			t.Fatalf("nullable/empty reference rejected after migration: %v", err)
		}
	})
	t.Run("empty-references-and-restart", func(t *testing.T) {
		dir := t.TempDir()
		s, p := r11PurchaseFixture(t, dir, "empty-a", "empty-account-a", "test")
		_ = p
		r11PurchaseFixtureOnStore(t, s, "empty-b", "empty-account-b", "test")
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(dir); err != nil {
			t.Fatal(err)
		}
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var indexSQL string
		if err = s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='billing_purchases_provider_reference'`).Scan(&indexSQL); err != nil || !strings.Contains(indexSQL, "provider_reference<>") {
			t.Fatalf("provider reference index=%q err=%v", indexSQL, err)
		}
	})
	t.Run("valid-non-empty-restart", func(t *testing.T) {
		dir := t.TempDir()
		s, p := r11PurchaseFixture(t, dir, "valid-reference", "valid-reference-account", "test")
		if err := s.UpdatePurchaseCheckout(context.Background(), p.ID, "valid-reference", "https://checkout.invalid/valid", p.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(dir); err != nil {
			t.Fatal(err)
		}
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	})
	t.Run("historical-duplicate-non-empty", func(t *testing.T) {
		dir := t.TempDir()
		s, a := r11PurchaseFixture(t, dir, "duplicate-a", "duplicate-account-a", "test")
		_, b := r11PurchaseFixtureOnStore(t, s, "duplicate-b", "duplicate-account-b", "test")
		if _, err := s.db.Exec(`DROP INDEX billing_purchases_provider_reference; UPDATE billing_purchases SET provider_reference='duplicate-reference' WHERE id IN (?,?)`, a.ID, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		err := Migrate(dir)
		if err == nil || !strings.Contains(err.Error(), "duplicate billing purchase provider reference") {
			t.Fatalf("duplicate upgrade error=%v", err)
		}
		db, openErr := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer db.Close()
		var count int
		if queryErr := db.QueryRow(`SELECT COUNT(*) FROM billing_purchases WHERE provider_reference='duplicate-reference'`).Scan(&count); queryErr != nil || count != 2 {
			t.Fatalf("duplicates changed count=%d err=%v", count, queryErr)
		}
	})
}

func TestFinalizedCommercialAuthorityRemainsImmutableR11(t *testing.T) {
	s, p, _, _ := finalizedPurchaseFixture(t)
	for name, statement := range map[string]string{
		"snapshot set count":  `UPDATE purchase_grant_snapshot_sets SET expected_count=2 WHERE purchase_id='purchase'`,
		"snapshot set digest": `UPDATE purchase_grant_snapshot_sets SET capability_digest='wrong' WHERE purchase_id='purchase'`,
		"snapshot row":        `UPDATE purchase_grant_snapshots SET period_cadence='monthly' WHERE purchase_id='purchase'`,
		"ownership":           `UPDATE billing_purchases SET account_id='other' WHERE id='purchase'`,
		"product":             `UPDATE billing_purchases SET product_id='other' WHERE id='purchase'`,
		"offer":               `UPDATE billing_purchases SET plan_code='other' WHERE id='purchase'`,
		"validity":            `UPDATE billing_purchases SET entitlement_period_end='2040-01-01T00:00:00Z' WHERE id='purchase'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.db.Exec(statement); err == nil {
				t.Fatalf("immutable authority changed for %s", p.ID)
			}
		})
	}
}

func TestIncompleteAuthorityProviderEventIsAtomicAtFormerRecoveryBoundary(t *testing.T) {
	dir := t.TempDir()
	s, p := r11PurchaseFixture(t, dir, "atomic-boundary", "atomic-boundary-account", "test")
	r11DropSnapshotMutationTriggers(t, s)
	if _, err := s.db.Exec(`DELETE FROM purchase_grant_snapshot_sets WHERE purchase_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	providerEventFault = func(stage string) error {
		if stage == "after-reconciliation" {
			return errors.New("injected former-boundary failure")
		}
		return nil
	}
	defer func() { providerEventFault = nil }()
	event := commerce.PaymentEvent{ProviderEventID: "atomic-boundary-event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "atomic-boundary-reference", AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := s.ApplyProviderPaymentEvent(context.Background(), p.Provider, p.BillingEnvironment, event); err == nil {
		t.Fatal("injected transaction failure was ignored")
	}
	stored, err := s.Purchase(context.Background(), p.ID)
	if err != nil || stored.Status != "pending" || stored.ProviderReference != "" || stored.ProviderState == "provider_success_unreconciled" {
		t.Fatalf("partial state committed: purchase=%+v err=%v", stored, err)
	}
	var events int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM billing_provider_events WHERE purchase_id=?`, p.ID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("partial provider evidence events=%d err=%v", events, err)
	}
	providerEventFault = nil
	if err = s.ApplyProviderPaymentEvent(context.Background(), p.Provider, p.BillingEnvironment, event); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Purchase(context.Background(), p.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderReference != event.ProviderReference || stored.ProviderState != "provider_success_unreconciled" || stored.ReconciliationReason == "" {
		t.Fatalf("replayed reconciliation state=%+v err=%v", stored, err)
	}
	var evidencePurchase, evidenceReference, evidenceType, evidenceStatus string
	if err = s.db.QueryRow(`SELECT purchase_id,provider_reference,event_type,status FROM billing_provider_events WHERE provider_event_id=?`, event.ProviderEventID).Scan(&evidencePurchase, &evidenceReference, &evidenceType, &evidenceStatus); err != nil || evidencePurchase != p.ID || evidenceReference != event.ProviderReference || evidenceType != string(event.Type) || evidenceStatus != "reconciliation_required" {
		t.Fatalf("durable evidence purchase=%q reference=%q type=%q status=%q err=%v", evidencePurchase, evidenceReference, evidenceType, evidenceStatus, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ApplyProviderPaymentEvent(context.Background(), p.Provider, p.BillingEnvironment, event); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Purchase(context.Background(), p.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderState != "provider_success_unreconciled" {
		t.Fatalf("restart replay state=%+v err=%v", stored, err)
	}
}

func TestProviderEvidenceColumnsExistOnUpgradedDatabase(t *testing.T) {
	s, p := r11PurchaseFixture(t, t.TempDir(), "evidence-columns", "evidence-columns-account", "test")
	defer s.Close()
	if err := s.UpdatePurchaseCheckout(context.Background(), p.ID, "evidence-reference", "https://checkout.invalid", p.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPaymentTransition(context.Background(), commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evidence-event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "evidence-reference", AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Now()}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency}); err != nil {
		t.Fatal(err)
	}
	var purchaseID, reference string
	if err := s.db.QueryRow(`SELECT purchase_id,provider_reference FROM billing_provider_events WHERE provider_event_id='evidence-event'`).Scan(&purchaseID, &reference); err != nil || purchaseID != p.ID || reference != "evidence-reference" {
		t.Fatalf("evidence purchase=%q reference=%q err=%v", purchaseID, reference, err)
	}
	if _, err := s.GetGrant(context.Background(), commerce.CustomerID(p.AccountID), tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}
