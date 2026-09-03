package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
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
		paid, err := s.Entitlement(context.Background(), "paid")
		if err != nil || paid.ValidFrom != "2026-09-15T00:00:00Z" || paid.ValidUntil != "2027-09-15T00:00:00Z" {
			t.Fatalf("restart %d paid=%+v err=%v", restart, paid, err)
		}
		s.Close()
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
	if _, err = s.db.Exec(`DROP TABLE account_entitlements`); err != nil {
		t.Fatal(err)
	}
	now := "2026-09-15T00:00:00Z"
	purchase := BillingPurchase{ID: "guard-purchase", AccountID: "guard", PlanCode: "nano", RateCardID: "production-nano-v1", RateCardMode: "production", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now, EntitlementPeriodEnd: "2027-09-15T00:00:00Z", CreatedAt: now, UpdatedAt: now}
	if _, err = s.CreatePendingPurchase(ctx, purchase, now); err == nil {
		t.Fatal("entitlement query error was discarded")
	}
	if _, lookupErr := s.Purchase(ctx, purchase.ID); lookupErr != sql.ErrNoRows {
		t.Fatalf("purchase persisted after guard error: %v", lookupErr)
	}
}

func TestCreateAccountAndDefaultEntitlementAreAtomic(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.db.Exec(`CREATE TRIGGER reject_default_entitlement BEFORE INSERT ON account_entitlements BEGIN SELECT RAISE(ABORT,'reject entitlement'); END`); err != nil {
		t.Fatal(err)
	}
	err = s.CreateAccount(ctx, Account{ID: "atomic", Email: "atomic@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"})
	if err == nil {
		t.Fatal("expected entitlement insert failure")
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
	if err = s.CreateAccount(context.Background(), Account{ID: "fresh", Email: "fresh@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	entitlement, err := s.Entitlement(context.Background(), "fresh")
	if err != nil || entitlement.ValidFrom != "2026-01-01T00:00:00Z" || entitlement.ValidUntil != "" || entitlement.Source != "legacy" {
		t.Fatalf("entitlement=%+v err=%v", entitlement, err)
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
