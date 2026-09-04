package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestRestartMarksRunningJobsFailed(t *testing.T) {
	d := t.TempDir()
	s, e := Open(d)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = s.CreateRepository(ctx, Repository{ID: "r", Name: "r", CreatedAt: "now"}); e != nil {
		t.Fatal(e)
	}
	if e = s.CreateJob(ctx, Job{ID: "j", RepositoryID: "r", State: "running", IncomingPath: "x", CreatedAt: "now"}); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	j, e := s.Job(ctx, "j")
	if e != nil || j.State != "failed" || j.Error != "interrupted by service restart" {
		t.Fatalf("%+v %v", j, e)
	}
}
func TestEntitlementValidityBoundaries(t *testing.T) {
	from := "2026-09-15T00:00:00Z"
	until := "2027-09-15T00:00:00Z"
	paid := AccountEntitlement{Source: "purchase", ValidFrom: from, ValidUntil: until}
	for _, tc := range []struct {
		at     time.Time
		active bool
	}{
		{time.Date(2026, 9, 14, 23, 59, 59, 999999999, time.UTC), false},
		{time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), true},
		{time.Date(2027, 9, 14, 23, 59, 59, 999999999, time.UTC), true},
		{time.Date(2027, 9, 15, 0, 0, 0, 0, time.UTC), false},
	} {
		active, err := paid.ActiveAt(tc.at)
		if err != nil || active != tc.active {
			t.Fatalf("at %s active=%v err=%v", tc.at, active, err)
		}
	}
	legacy := AccountEntitlement{Source: "legacy", ValidFrom: from}
	active, err := legacy.ActiveAt(time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !active {
		t.Fatalf("legacy active=%v err=%v", active, err)
	}
	paid.ValidUntil = ""
	if active, err = paid.ActiveAt(time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)); err != nil || active {
		t.Fatalf("open-ended paid active=%v err=%v", active, err)
	}
}

func TestUnknownLegacyPlanFailsClosedDuringMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,created_at,updated_at,source) VALUES('unknown-plan','mystery','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','legacy')`); err != nil {
		// The foreign key is intentionally exercised with a real account below.
		_, _ = s.db.Exec(`INSERT INTO accounts VALUES('unknown-plan','unknown-plan@example.invalid','x','active','2026-01-01T00:00:00Z')`)
		if _, err = s.db.Exec(`INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,created_at,updated_at,source) VALUES('unknown-plan','mystery','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','legacy')`); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if _, err = Open(dir); err == nil {
		t.Fatal("unknown legacy plan migrated successfully")
	}
	if _, err = os.Stat(filepath.Join(dir, "tpahub.db")); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanedPurchaseGrantFailsClosedDuringMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO accounts VALUES('orphan-grant','orphan@example.invalid','x','active','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,source,purchase_id,created_at,updated_at) VALUES('orphan-grant','tpa-hub','repository.publish','offer',1,'2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','annual','purchase','missing-purchase','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = Open(dir); err == nil {
		t.Fatal("orphaned purchase grant migrated successfully")
	}
}

func TestEntitlementValidityMigrationAndRestart(t *testing.T) {
	d := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(d, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE account_entitlements(account_id TEXT PRIMARY KEY REFERENCES accounts(id),plan_code TEXT NOT NULL,started_at TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,source TEXT NOT NULL DEFAULT 'legacy',purchase_id TEXT NOT NULL DEFAULT '');
CREATE TABLE billing_purchases(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),plan_code TEXT NOT NULL,rate_card_id TEXT NOT NULL,rate_card_mode TEXT NOT NULL,tax_classification TEXT NOT NULL DEFAULT 'saas-personal',currency TEXT NOT NULL,amount_minor INTEGER NOT NULL,billing_environment TEXT NOT NULL,provider TEXT NOT NULL,status TEXT NOT NULL,entitlement_period_start TEXT NOT NULL,entitlement_period_end TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',checkout_url TEXT NOT NULL DEFAULT '',checkout_request_version TEXT NOT NULL DEFAULT 'v1',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',failed_at TEXT NOT NULL DEFAULT '',expired_at TEXT NOT NULL DEFAULT '');
INSERT INTO accounts VALUES('legacy','legacy@example.invalid','x','active','2025-01-01T00:00:00Z'),('paid','paid@example.invalid','x','active','2026-01-01T00:00:00Z');
INSERT INTO account_entitlements VALUES('legacy','nano','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z','legacy',''),('paid','nano','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','purchase','p1');
INSERT INTO billing_purchases VALUES('p1','paid','nano','production-nano-v1','production','saas-personal','EUR',500,'test','stripe','paid','2026-09-15T00:00:00Z','2027-09-15T00:00:00Z','','','v2','2026-09-15T00:00:00Z','2026-09-15T00:00:00Z','2026-09-15T00:00:00Z','','');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for restart := 0; restart < 2; restart++ {
		s, err := Open(d)
		if err != nil {
			t.Fatal(err)
		}
		legacy, err := s.Entitlement(context.Background(), "legacy")
		if err != nil || legacy.ValidFrom != "2025-01-01T00:00:00Z" || legacy.ValidUntil != "" {
			t.Fatalf("restart %d legacy=%+v err=%v", restart, legacy, err)
		}
		legacyGrant, err := s.GetGrant(context.Background(), "legacy", commerce.ProductTPAHub, "repository.publish", time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil || legacyGrant.Source != "legacy" || legacyGrant.PurchaseID != "" || legacyGrant.ValidUntil != nil {
			t.Fatalf("restart %d migrated grandfather grant=%+v err=%v", restart, legacyGrant, err)
		}
		paid, err := s.Entitlement(context.Background(), "paid")
		if err != nil || paid.ValidFrom != "2026-09-15T00:00:00Z" || paid.ValidUntil != "2027-09-15T00:00:00Z" {
			t.Fatalf("restart %d paid=%+v err=%v", restart, paid, err)
		}
		purchase, err := s.Purchase(context.Background(), "p1")
		if err != nil || purchase.ProductID != string(commerce.ProductTPAHub) {
			t.Fatalf("restart %d purchase=%+v err=%v", restart, purchase, err)
		}
		grant, err := s.GetGrant(context.Background(), "paid", commerce.ProductTPAHub, "repository.download", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		if err != nil || grant.Limits.EgressBytes != 12_000_000_000 || grant.Source != "purchase" || grant.PurchaseID != "p1" {
			t.Fatalf("restart %d grant=%+v err=%v", restart, grant, err)
		}
		s.Close()
	}
}

func TestProviderCommercialRowsMigrateToProductScope(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE billing_provider_submissions(account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,period,provider,environment));
CREATE TABLE mock_billing_events(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(account_id,period,event_type));
INSERT INTO accounts VALUES('commercial','commercial@example.invalid','x','active','2025-01-01T00:00:00Z');
INSERT INTO billing_provider_submissions VALUES('commercial','2026-01','stripe','test','legacy-final','submitted','ii_legacy','2026-01-01T00:00:00Z','','');
INSERT INTO mock_billing_events VALUES('legacy-event','commercial','2026-01','period-finalized','{}','2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for restart := 0; restart < 2; restart++ {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		submission, err := s.ProviderSubmission(context.Background(), "commercial", string(commerce.ProductTPAHub), "2026-01", "stripe", "test")
		if err != nil || submission.ProductID != string(commerce.ProductTPAHub) {
			t.Fatalf("restart %d submission=%+v err=%v", restart, submission, err)
		}
		payload, err := s.ProductMockBillingEventPayload(context.Background(), "commercial", string(commerce.ProductTPAHub), "2026-01", "period-finalized")
		if err != nil || payload != "{}" {
			t.Fatalf("restart %d payload=%q err=%v", restart, payload, err)
		}
		if restart == 0 {
			if err = s.SaveProviderSubmission(context.Background(), BillingProviderSubmission{AccountID: "commercial", ProductID: "test-product", Period: "2026-01", Provider: "stripe", Environment: "test", FinalizationID: "test-final", Status: "submitted", AttemptedAt: "2026-01-01T00:00:00Z"}); err != nil {
				t.Fatalf("cross-product submission collision: %v", err)
			}
			if err = s.RecordProductMockBillingEvent(context.Background(), "legacy-event", "commercial", "test-product", "2026-01", "period-finalized", `{"test":true}`, "2026-01-01T00:00:00Z"); err != nil {
				t.Fatalf("cross-product event collision: %v", err)
			}
		}
		s.Close()
	}
}

func TestLegacyGrantMigrationDoesNotGrantFutureNonTPAAccounts(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateAccount(context.Background(), Account{ID: "other-only", Email: "other-only@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.GetGrant(context.Background(), "other-only", commerce.ProductTPAHub, "repository.create", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, commerce.ErrGrantNotFound) {
		t.Fatalf("restart granted TPA product to non-TPA account: %v", err)
	}
}

func TestProductScopedGrantsAndPendingPurchasesAreIsolated(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "shared", Email: "shared@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	testProduct := commerce.ProductID("test-product")
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "shared", ProductID: testProduct, CapabilityID: "test.execute", OfferID: "test-offer", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodMonthly, Limits: commerce.Limits{OperationMetric: commerce.MetricID("test_operations"), OperationCount: 77}, Source: "legacy", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	testGrant, err := s.GetGrant(ctx, "shared", testProduct, "test.execute", now)
	if err != nil || testGrant.Limits.OperationMetric != commerce.MetricID("test_operations") || testGrant.Limits.OperationCount != 77 {
		t.Fatalf("test grant=%+v err=%v", testGrant, err)
	}
	if _, err = s.GetGrant(ctx, "shared", commerce.ProductTPAHub, "test.execute", now); !errors.Is(err, commerce.ErrGrantNotFound) {
		t.Fatalf("cross-product grant leaked: %v", err)
	}
	if _, err = s.GetGrant(ctx, "shared", testProduct, "repository.publish", now); !errors.Is(err, commerce.ErrGrantNotFound) {
		t.Fatalf("cross-capability grant leaked: %v", err)
	}

	purchase := func(id string, product commerce.ProductID) BillingPurchase {
		return BillingPurchase{ID: id, AccountID: "shared", ProductID: string(product), PlanCode: "offer", RateCardID: "rate", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.Add(time.Hour).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	}
	if _, err = s.CreatePendingPurchase(ctx, purchase("tpa-pending", commerce.ProductTPAHub), now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "test.execute"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePendingPurchase(ctx, purchase("test-pending", testProduct), now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "test.execute"}); err != nil {
		t.Fatalf("cross-product pending collision: %v", err)
	}
}

func TestPurchasePreservesGrandfatheredGrant(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	until := now.AddDate(1, 0, 0)
	if err = s.CreateAccount(ctx, Account{ID: "grandfathered", Email: "grandfathered@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}, commerce.Grant{CustomerID: "grandfathered", ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "nano", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Source: "legacy", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	purchase := BillingPurchase{ID: "grandfathered-purchase", AccountID: "grandfathered", ProductID: string(commerce.ProductTPAHub), PlanCode: "offer", RateCardID: "rate", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "cs_grandfathered", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, purchase, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "repository.download", Period: commerce.PeriodAnnual}); err != nil {
		t.Fatal(err)
	}
	transition := commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-grandfathered", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: purchase.Provider, Environment: purchase.BillingEnvironment, Kind: "success", AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, ValidFrom: now, ValidUntil: until}
	if err = s.ApplyPaymentTransition(ctx, transition); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.GetGrant(ctx, "grandfathered", commerce.ProductTPAHub, "repository.publish", now)
	if err != nil || legacy.Source != "legacy" || legacy.PurchaseID != "" {
		t.Fatalf("grandfathered grant changed after purchase: %+v err=%v", legacy, err)
	}
	purchased, err := s.GetGrant(ctx, "grandfathered", commerce.ProductTPAHub, "repository.download", now)
	if err != nil || purchased.Source != "purchase" || purchased.PurchaseID != purchase.ID || purchased.ValidUntil == nil || !purchased.ValidUntil.Equal(until) {
		t.Fatalf("purchased grant validity/provenance=%+v err=%v", purchased, err)
	}
}

func TestActivePurchaseGrantBlocksOnlyItsProduct(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "paid-products", Email: "paid-products@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	until := now.AddDate(1, 0, 0)
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "paid-products", ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "nano", Enabled: true, ValidFrom: now, ValidUntil: &until, Period: commerce.PeriodAnnual, Source: "purchase", PurchaseID: "paid-tpa", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	purchase := func(id string, product commerce.ProductID) BillingPurchase {
		return BillingPurchase{ID: id, AccountID: "paid-products", ProductID: string(product), PlanCode: "offer", RateCardID: "rate", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	}
	if _, err = s.CreatePendingPurchase(ctx, purchase("blocked", commerce.ProductTPAHub), now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "test.execute"}); err == nil || err.Error() != "purchase-already-active" {
		t.Fatalf("active product purchase error=%v", err)
	}
	if _, err = s.CreatePendingPurchase(ctx, purchase("other", "test-product"), now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "test.execute"}); err != nil {
		t.Fatalf("active TPA grant blocked other product: %v", err)
	}
}

func TestPurchaseGrantSnapshotIsPersistedAndActivatesExactTerms(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "snapshot", Email: "snapshot@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	until := now.AddDate(1, 0, 0)
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "snapshot", ProductID: "test-product", CapabilityID: "test.execute", OfferID: "old-offer", Enabled: true, ValidFrom: now.Add(-time.Hour), ValidUntil: &until, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 999, EgressBytes: 999, OperationMetric: "old_metric", OperationCount: 99}, Source: "legacy", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	purchase := BillingPurchase{ID: "snapshot-purchase", AccountID: "snapshot", ProductID: "test-product", PlanCode: "test-offer-v2", RateCardID: "test-rate-v2", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 725, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "cs_snapshot", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	template := commerce.GrantTemplate{CapabilityID: "test.execute", Period: commerce.PeriodMonthly, Limits: commerce.Limits{StorageBytes: 123, EgressBytes: 456, OperationMetric: "test_operations", OperationCount: 7}}
	if _, err = s.CreatePendingPurchase(ctx, purchase, now.Format(time.RFC3339Nano), template); err != nil {
		t.Fatal(err)
	}
	var snapshotCount int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM purchase_grant_snapshots WHERE purchase_id=?`, purchase.ID).Scan(&snapshotCount); err != nil || snapshotCount != 1 {
		t.Fatalf("snapshot count=%d err=%v", snapshotCount, err)
	}
	if _, err = s.db.Exec(`UPDATE purchase_grant_snapshots SET operation_limit=999 WHERE purchase_id=?`, purchase.ID); err == nil {
		t.Fatal("purchase grant snapshot was mutable")
	}
	// Webhook-time grants are deliberately changed; activation must use the
	// immutable purchase snapshot instead of this mutable live state.
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "snapshot", ProductID: "test-product", CapabilityID: "test.execute", OfferID: "webhook-time-offer", Enabled: true, ValidFrom: now.Add(-time.Hour), ValidUntil: &until, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 1, EgressBytes: 2, OperationMetric: "wrong_metric", OperationCount: 3}, Source: "legacy", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A same-product unrelated capability must not be activated by this purchase.
	unrelated := commerce.Grant{CustomerID: "snapshot", ProductID: "test-product", CapabilityID: "unrelated", OfferID: "old", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 999}, Source: "legacy", CreatedAt: now, UpdatedAt: now}
	if err = s.PutGrant(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt_snapshot", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, ValidFrom: now, ValidUntil: until}); err != nil {
		t.Fatal(err)
	}
	grant, err := s.GetGrant(ctx, "snapshot", "test-product", "test.execute", now)
	if err != nil || grant.OfferID != purchase.PlanCode || grant.Period != template.Period || grant.Limits != template.Limits || grant.PurchaseID != purchase.ID {
		t.Fatalf("activated grant=%+v err=%v", grant, err)
	}
	unchanged, err := s.GetGrant(ctx, "snapshot", "test-product", "unrelated", now)
	if err != nil || unchanged != unrelated {
		t.Fatalf("unrelated grant changed: before=%+v after=%+v err=%v", unrelated, unchanged, err)
	}
}

func TestPurchaseGrantSnapshotsAreScopedToPurchase(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "snapshot-scope", Email: "snapshot-scope@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	purchase := func(id, product string) BillingPurchase {
		return BillingPurchase{ID: id, AccountID: "snapshot-scope", ProductID: product, PlanCode: id + "-offer", RateCardID: "rate", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	}
	if _, err = s.CreatePendingPurchase(ctx, purchase("purchase-a", "product-a"), now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodMonthly, Limits: commerce.Limits{OperationMetric: "a_operations", OperationCount: 10}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePendingPurchase(ctx, purchase("purchase-b", "product-b"), now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodAnnual, Limits: commerce.Limits{OperationMetric: "b_operations", OperationCount: 20}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM purchase_grant_snapshots WHERE capability_id=?`, "execute").Scan(&count); err != nil || count != 2 {
		t.Fatalf("snapshot count=%d err=%v", count, err)
	}
	var aPeriod, aMetric string
	var aLimit int64
	if err = s.db.QueryRow(`SELECT period_cadence,operation_metric,operation_limit FROM purchase_grant_snapshots WHERE purchase_id=? AND capability_id=?`, "purchase-a", "execute").Scan(&aPeriod, &aMetric, &aLimit); err != nil {
		t.Fatal(err)
	}
	if aPeriod != string(commerce.PeriodMonthly) || aMetric != "a_operations" || aLimit != 10 {
		t.Fatalf("purchase-a snapshot=%s/%s/%d", aPeriod, aMetric, aLimit)
	}
	var bPeriod, bMetric string
	var bLimit int64
	if err = s.db.QueryRow(`SELECT period_cadence,operation_metric,operation_limit FROM purchase_grant_snapshots WHERE purchase_id=? AND capability_id=?`, "purchase-b", "execute").Scan(&bPeriod, &bMetric, &bLimit); err != nil {
		t.Fatal(err)
	}
	if bPeriod != string(commerce.PeriodAnnual) || bMetric != "b_operations" || bLimit != 20 {
		t.Fatalf("purchase-b snapshot=%s/%s/%d", bPeriod, bMetric, bLimit)
	}
}

func TestPaymentGrantActivationRollsBackPartialGrantSet(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "partial", Email: "partial@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	purchase := BillingPurchase{ID: "partial-purchase", AccountID: "partial", ProductID: "partial-product", PlanCode: "partial-offer", RateCardID: "rate", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), ProviderReference: "cs_partial", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	templates := []commerce.GrantTemplate{{CapabilityID: "good", Period: commerce.PeriodMonthly}, {CapabilityID: "reject", Period: commerce.PeriodMonthly}}
	if _, err = s.CreatePendingPurchase(ctx, purchase, now.Format(time.RFC3339Nano), templates...); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_partial_grant BEFORE INSERT ON product_grants WHEN NEW.capability_id='reject' BEGIN SELECT RAISE(ABORT,'injected grant failure'); END`); err != nil {
		t.Fatal(err)
	}
	until := now.AddDate(1, 0, 0)
	err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt_partial", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, ValidFrom: now, ValidUntil: until})
	if err == nil {
		t.Fatal("partial grant activation unexpectedly succeeded")
	}
	saved, err := s.Purchase(ctx, purchase.ID)
	if err != nil || saved.Status != "pending" {
		t.Fatalf("purchase after rollback=%+v err=%v", saved, err)
	}
	if _, err = s.GetGrant(ctx, "partial", "partial-product", "good", now); !errors.Is(err, commerce.ErrGrantNotFound) {
		t.Fatalf("first grant survived partial activation: %v", err)
	}
}

func TestPaymentSuccessWinsOverEarlierFailedEventForPayablePurchase(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "late-success", Email: "late-success@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	until := now.AddDate(1, 0, 0)
	purchase := BillingPurchase{ID: "late-success-purchase", AccountID: "late-success", ProductID: "late-product", PlanCode: "late-offer", RateCardID: "rate", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), ProviderReference: "cs_late_success", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, purchase, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodMonthly}); err != nil {
		t.Fatal(err)
	}
	base := func(id string, kind commerce.PaymentEventType) commerce.PaymentTransition {
		return commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: id, OperationID: purchase.ID, Type: kind, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: purchase.Provider, Environment: purchase.BillingEnvironment, Kind: map[commerce.PaymentEventType]string{commerce.PaymentFailed: "failed", commerce.PaymentSucceeded: "success"}[kind], AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, ValidFrom: now, ValidUntil: until}
	}
	if err = s.ApplyPaymentTransition(ctx, base("evt-late-failed", commerce.PaymentFailed)); err != nil {
		t.Fatal(err)
	}
	failed, err := s.Purchase(ctx, purchase.ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("failed state=%+v err=%v", failed, err)
	}
	if err = s.ApplyPaymentTransition(ctx, base("evt-late-success", commerce.PaymentSucceeded)); err != nil {
		t.Fatalf("payable late success rejected: %v", err)
	}
	paid, err := s.Purchase(ctx, purchase.ID)
	if err != nil || paid.Status != "paid" {
		t.Fatalf("paid state=%+v err=%v", paid, err)
	}
	if _, err = s.GetGrant(ctx, commerce.CustomerID(purchase.AccountID), commerce.ProductID(purchase.ProductID), "execute", now); err != nil {
		t.Fatalf("late success did not activate grant: %v", err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-after-paid", OperationID: purchase.ID, Type: commerce.PaymentFailed, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, OccurredAt: now}, Provider: purchase.Provider, Environment: purchase.BillingEnvironment, Kind: "failed", AmountMinor: purchase.AmountMinor, Currency: purchase.Currency, ValidFrom: now, ValidUntil: until}); err != nil {
		t.Fatalf("terminal event after paid was rejected: %v", err)
	}
	stillPaid, err := s.Purchase(ctx, purchase.ID)
	if err != nil || stillPaid.Status != "paid" {
		t.Fatalf("terminal event revoked paid purchase: %+v err=%v", stillPaid, err)
	}

	expiredPurchase := purchase
	expiredPurchase.ID = "late-expired-purchase"
	expiredPurchase.ProductID = "late-expired-product"
	expiredPurchase.ProviderReference = "cs_late_expired"
	if _, err = s.CreatePendingPurchase(ctx, expiredPurchase, now.Format(time.RFC3339Nano), commerce.GrantTemplate{CapabilityID: "execute", Period: commerce.PeriodAnnual}); err != nil {
		t.Fatal(err)
	}
	expiredEvent := commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-late-expired", OperationID: expiredPurchase.ID, Type: commerce.PaymentExpired, ProviderReference: expiredPurchase.ProviderReference, AmountMinor: expiredPurchase.AmountMinor, Currency: expiredPurchase.Currency, OccurredAt: now}, Provider: expiredPurchase.Provider, Environment: expiredPurchase.BillingEnvironment, Kind: "expired", AmountMinor: expiredPurchase.AmountMinor, Currency: expiredPurchase.Currency, ValidFrom: now, ValidUntil: until}
	if err = s.ApplyPaymentTransition(ctx, expiredEvent); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt-late-expired-success", OperationID: expiredPurchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: expiredPurchase.ProviderReference, AmountMinor: expiredPurchase.AmountMinor, Currency: expiredPurchase.Currency, OccurredAt: now}, Provider: expiredPurchase.Provider, Environment: expiredPurchase.BillingEnvironment, Kind: "success", AmountMinor: expiredPurchase.AmountMinor, Currency: expiredPurchase.Currency, ValidFrom: now, ValidUntil: until}); err != nil {
		t.Fatalf("payable expired checkout success rejected: %v", err)
	}
	expiredPaid, err := s.Purchase(ctx, expiredPurchase.ID)
	if err != nil || expiredPaid.Status != "paid" {
		t.Fatalf("expired checkout final state=%+v err=%v", expiredPaid, err)
	}
}

func TestGrantValidityDisabledAndPurchaseLinkage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "grants", Email: "grants@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	grant := commerce.Grant{CustomerID: "grants", ProductID: "test-product", CapabilityID: "test.execute", OfferID: "test", Enabled: true, ValidFrom: past, ValidUntil: &future, Period: commerce.PeriodMonthly, Limits: commerce.Limits{StorageBytes: 42}, Source: "purchase", PurchaseID: "purchase-link", CreatedAt: now, UpdatedAt: now}
	if err = s.PutGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGrant(ctx, grant.CustomerID, grant.ProductID, grant.CapabilityID, now)
	if err != nil || got.PurchaseID != "purchase-link" || got.Limits.StorageBytes != 42 {
		t.Fatalf("grant=%+v err=%v", got, err)
	}
	if _, err = s.GetGrant(ctx, grant.CustomerID, grant.ProductID, grant.CapabilityID, future); !errors.Is(err, commerce.ErrGrantInactive) {
		t.Fatalf("expired grant error=%v", err)
	}
	grant.Enabled = false
	grant.ValidUntil = nil
	if err = s.PutGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetGrant(ctx, grant.CustomerID, grant.ProductID, grant.CapabilityID, now); !errors.Is(err, commerce.ErrGrantDisabled) {
		t.Fatalf("disabled grant error=%v", err)
	}
}

func TestCreatePendingPurchasePropagatesEntitlementQueryError(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "guard", Email: "guard@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE product_grants`); err != nil {
		t.Fatal(err)
	}
	now := "2026-09-15T00:00:00Z"
	purchase := BillingPurchase{ID: "guard-purchase", AccountID: "guard", PlanCode: "nano", RateCardID: "production-nano-v1", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now, EntitlementPeriodEnd: "2027-09-15T00:00:00Z", CreatedAt: now, UpdatedAt: now}
	if _, err = s.CreatePendingPurchase(ctx, purchase, now, commerce.GrantTemplate{CapabilityID: "test.execute"}); err == nil {
		t.Fatal("grant query error was discarded")
	}
	if _, lookupErr := s.Purchase(ctx, purchase.ID); lookupErr != sql.ErrNoRows {
		t.Fatalf("purchase persisted after guard error: %v", lookupErr)
	}
}

func TestCreateAccountEntitlementAndRequiredGrantsAreAtomic(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.db.Exec(`CREATE TRIGGER reject_required_grant BEFORE INSERT ON product_grants BEGIN SELECT RAISE(ABORT,'reject grant'); END`); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	required := commerce.Grant{ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "nano", Enabled: true, ValidFrom: created, Period: commerce.PeriodAnnual, Source: "legacy", CreatedAt: created, UpdatedAt: created}
	err = s.CreateAccount(ctx, Account{ID: "atomic", Email: "atomic@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}, required)
	if err == nil {
		t.Fatal("expected grant insert failure")
	}
	if _, lookupErr := s.AccountByID(ctx, "atomic"); lookupErr != sql.ErrNoRows {
		t.Fatalf("half-created account remains: %v", lookupErr)
	}
}

func TestFreshSchemaContainsEntitlementValidity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grantInput := commerce.Grant{ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "nano", Enabled: true, ValidFrom: created, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 5_000_000}, Source: "legacy", CreatedAt: created, UpdatedAt: created}
	if err = s.CreateAccount(context.Background(), Account{ID: "fresh", Email: "fresh@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}, grantInput); err != nil {
		t.Fatal(err)
	}
	entitlement, err := s.Entitlement(context.Background(), "fresh")
	if err != nil || entitlement.ValidFrom != "2026-01-01T00:00:00Z" || entitlement.ValidUntil != "" || entitlement.Source != "legacy" {
		t.Fatalf("entitlement=%+v err=%v", entitlement, err)
	}
	grant, err := s.GetGrant(context.Background(), "fresh", commerce.ProductTPAHub, "repository.publish", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || grant.Limits.StorageBytes != 5_000_000 || grant.OfferID != "nano" {
		t.Fatalf("fresh grant=%+v err=%v", grant, err)
	}
}

func TestRepositoryAndJobPersistence(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	r := Repository{ID: "r", Name: "demo", CreatedAt: "now"}
	if e = s.CreateRepository(ctx, r); e != nil {
		t.Fatal(e)
	}
	j := Job{ID: "j", RepositoryID: "r", State: "queued", IncomingPath: "/tmp/j", CreatedAt: "now"}
	if e = s.CreateJob(ctx, j); e != nil {
		t.Fatal(e)
	}
	got, e := s.Claim(ctx)
	if e != nil || got.ID != "j" || got.State != "running" {
		t.Fatalf("claim: %+v %v", got, e)
	}
	if e = s.Finish(ctx, "j", "failed", "bad"); e != nil {
		t.Fatal(e)
	}
	got, e = s.Job(ctx, "j")
	if e != nil || got.State != "failed" || got.Error != "bad" {
		t.Fatalf("job: %+v %v", got, e)
	}
}
