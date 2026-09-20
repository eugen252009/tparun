package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestSchema5FreshInitializationIsAtomic(t *testing.T) {
	dir := t.TempDir()
	migrationFault = func(stage string) error {
		if stage == "before-schema-commit" {
			return errors.New("injected initialization failure")
		}
		return nil
	}
	defer func() { migrationFault = nil }()
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("initialization failure ignored")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if count != 0 {
		t.Fatalf("partial schema committed: %d objects", count)
	}
	migrationFault = nil
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil || version != 11 || version != CurrentSchemaVersion() {
		t.Fatalf("schema=%d err=%v", version, err)
	}
}

func TestSchema5ValidationIgnoresFormattingNotTokenBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, ddl string
		accept    bool
	}{
		{"formatting", `CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('active','suspended')),created_at TEXT NOT NULL,membership_expires_at TEXT DEFAULT NULL,purge_after TEXT DEFAULT NULL,lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal')));`, false},
		{"different-column-not-whitespace", `CREATE TABLE accounts(idTEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('active','suspended')),created_at TEXT NOT NULL,membership_expires_at TEXT DEFAULT NULL,purge_after TEXT DEFAULT NULL,lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal')));`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := populatedBoundaryFixture(t)
			db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`PRAGMA writable_schema=ON; UPDATE sqlite_master SET sql=? WHERE name='accounts'; PRAGMA writable_schema=OFF`, tc.ddl); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before, bytesBefore, schemaBefore := boundarySnapshot(t, dir)
			err = Migrate(dir)
			if (err == nil) != tc.accept {
				t.Fatalf("accept=%v err=%v", tc.accept, err)
			}
			after, bytesAfter, schemaAfter := boundarySnapshot(t, dir)
			if before != after || bytesBefore != bytesAfter || schemaBefore != schemaAfter {
				t.Fatal("schema validation wrote state")
			}
		})
	}
}

func TestSchema5GrantIdentitySourcesAndPeriods(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if err = s.CreateAccount(ctx, Account{ID: "a", Email: "a@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: start.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	local := commerce.Grant{CustomerID: "a", ProductID: "p", CapabilityID: "cap", OfferID: "local-policy", Enabled: true, ValidFrom: start.AddDate(0, -3, 0), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 10}, Source: "local", CreatedAt: start, UpdatedAt: start}
	if err = s.PutGrant(ctx, local); err != nil {
		t.Fatal(err)
	}
	admin := local
	admin.Source = "administrative"
	admin.OfferID = "operator"
	admin.ValidFrom = start
	admin.ValidUntil = &end
	admin.Period = commerce.PeriodMonthly
	admin.Limits.StorageBytes = 20
	if err = s.PutGrant(ctx, admin); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{start.Add(-time.Nanosecond), start, end} {
		g, err := s.GetGrant(ctx, "a", "p", "cap", at)
		if err != nil {
			t.Fatal(err)
		}
		from, until, err := g.PeriodAt(at)
		if err != nil {
			t.Fatal(err)
		}
		if at.Equal(start) {
			if g.Limits.StorageBytes != 20 || !from.Equal(start) || !until.Equal(end) {
				t.Fatalf("admin period=%+v %v/%v", g, from, until)
			}
		} else {
			if g.Limits.StorageBytes != 10 || !from.Equal(local.ValidFrom) || !until.Equal(local.ValidFrom.AddDate(1, 0, 0)) {
				t.Fatalf("local period=%+v %v/%v", g, from, until)
			}
		}
	}
	for _, source := range []string{"legacy", "migration", "purchase", "unknown"} {
		bad := local
		bad.Source = source
		if err = s.PutGrant(ctx, bad); err == nil {
			t.Fatalf("invalid base provenance %s accepted", source)
		}
	}
	if _, err = s.db.Exec(`UPDATE product_grants SET source='other' WHERE source='local'`); err == nil {
		t.Fatal("identity mutable")
	}
	if err = s.CreateRepository(ctx, Repository{ID: "unowned", Name: "invalid"}); err == nil {
		t.Fatal("unowned repository accepted")
	}
	if err = s.CreateRepository(ctx, Repository{ID: "foreign", AccountID: "absent", Name: "invalid"}); err == nil {
		t.Fatal("foreign key ignored")
	}
	if err = s.CreateRepository(ctx, Repository{ID: "owned", AccountID: "a", Name: "valid"}); err != nil {
		t.Fatal(err)
	}
}
