package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestLifecycleEvaluationAndRenewal(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateAccount(ctx, Account{ID: "customer-life", Email: "customer-life@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembershipExpiry(ctx, "customer-life", "2026-01-31T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.AccountByID(ctx, "customer-life")
	if a.PurgeAfter != "2026-03-31T00:00:00Z" {
		t.Fatalf("purge_after=%q", a.PurgeAfter)
	}
	state, err := EvaluateAccountLifecycle(a, mustLifecycleTime(t, "2026-03-01T00:00:00Z"))
	if err != nil || state != AccountLifecycleMembershipExpired {
		t.Fatalf("state=%q err=%v", state, err)
	}
	state, err = EvaluateAccountLifecycle(a, mustLifecycleTime(t, "2026-04-01T00:00:00Z"))
	if err != nil || state != AccountLifecyclePurgeDue {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if err := s.SetMembershipExpiry(ctx, "customer-life", "2026-02-28T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	a, _ = s.AccountByID(ctx, "customer-life")
	if a.PurgeAfter != "2026-04-28T00:00:00Z" {
		t.Fatalf("renewed purge_after=%q", a.PurgeAfter)
	}
	if err := s.CreateAccount(ctx, Account{ID: "ephemeral-life", Email: "ephemeral-life@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleEphemeral, MembershipExpiresAt: "2026-01-01T00:00:00Z", PurgeAfter: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	a, _ = s.AccountByID(ctx, "ephemeral-life")
	state, err = EvaluateAccountLifecycle(a, mustLifecycleTime(t, "2026-01-01T00:00:00Z"))
	if err != nil || state == AccountLifecycleActive {
		t.Fatalf("expired ephemeral state=%q err=%v", state, err)
	}
	if err := s.CreateAccount(ctx, Account{ID: "internal-life", Email: "internal-life@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleInternal}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembershipExpiry(ctx, "internal-life", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("internal lifecycle update accepted")
	}
}

func mustLifecycleTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestExpiredEphemeralSessionIsRejected(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateAccount(ctx, Account{ID: "session-ephemeral", Email: "session-ephemeral@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleEphemeral, MembershipExpiresAt: "2020-01-01T00:00:00Z", PurgeAfter: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, Session{ID: "session", AccountID: "session-ephemeral", TokenPrefix: "fixture"}, HashToken("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionPrincipalByToken(ctx, HashToken("fixture")); err == nil {
		t.Fatal("expired ephemeral session remained authorized")
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
