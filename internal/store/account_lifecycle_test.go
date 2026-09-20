package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountLifecycleDefaultsAndValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateAccount(ctx, Account{ID: "customer", Email: "customer@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	customer, err := s.AccountByID(ctx, "customer")
	if err != nil {
		t.Fatal(err)
	}
	if customer.LifecycleKind != AccountLifecycleCustomer || customer.MembershipExpiresAt != "" || customer.PurgeAfter != "" {
		t.Fatalf("unexpected customer lifecycle: %+v", customer)
	}
	if err := s.CreateAccount(ctx, Account{ID: "internal", Email: "internal@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleInternal}); err != nil {
		t.Fatal(err)
	}
	internal, err := s.AccountByID(ctx, "internal")
	if err != nil {
		t.Fatal(err)
	}
	if internal.LifecycleKind != AccountLifecycleInternal {
		t.Fatalf("lifecycle kind=%q", internal.LifecycleKind)
	}
	if err := s.CreateAccount(ctx, Account{ID: "ephemeral", Email: "ephemeral@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleEphemeral}); err != nil {
		t.Fatal(err)
	}
	ephemeral, err := s.AccountByID(ctx, "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral.LifecycleKind != AccountLifecycleEphemeral {
		t.Fatalf("lifecycle kind=%q", ephemeral.LifecycleKind)
	}
	if err := s.CreateAccount(ctx, Account{ID: "invalid", Email: "invalid@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleKind("other")}); err == nil {
		t.Fatal("invalid lifecycle kind accepted")
	}
}

func TestSchema7To8LifecycleMigrationPreservesAccountState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tpahub.db")
	legacyDDL := strings.Replace(schemaSQL, ",membership_expires_at TEXT DEFAULT NULL,purge_after TEXT DEFAULT NULL,lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal'))", "", 1)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(legacyDDL); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO tpahub_schema(singleton,version) VALUES(1,7); INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('legacy','legacy@example.invalid','hash','active','2026-01-01T00:00:00Z'); INSERT INTO account_metadata(account_id,account_class) VALUES('legacy','production'); INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES('legacy-repo','legacy','legacy','private','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if CurrentSchemaVersion() != 8 {
		t.Fatalf("schema=%d", CurrentSchemaVersion())
	}
	a, err := s.AccountByID(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "legacy" || a.LifecycleKind != AccountLifecycleCustomer || a.MembershipExpiresAt != "" || a.PurgeAfter != "" {
		t.Fatalf("migrated account=%+v", a)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM repositories WHERE account_id='legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repositories=%d err=%v", count, err)
	}
	var integrity string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
}
