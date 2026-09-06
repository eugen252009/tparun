package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRequiresExplicitMigrationForLegacyDatabase(t *testing.T) {
	dir := historicalFixture(t, nil)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "migration required") {
		t.Fatalf("legacy database was silently migrated: %v", err)
	}
}

func TestOpenRejectsUnsupportedFutureSchema(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tpahub_schema SET version=2`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "unsupported database schema version") {
		t.Fatalf("future schema was accepted: %v", err)
	}
}

func TestSchemaAuthorityIsStructurallySingleton(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var singleton, version int
	if err := s.db.QueryRow(`SELECT singleton,version FROM tpahub_schema`).Scan(&singleton, &version); err != nil {
		t.Fatal(err)
	}
	if singleton != 1 || version != currentSchemaVersion {
		t.Fatalf("schema authority = (%d,%d)", singleton, version)
	}
	if _, err := s.db.Exec(`INSERT INTO tpahub_schema(singleton,version) VALUES(2,1)`); err == nil {
		t.Fatal("non-canonical schema authority row accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO tpahub_schema(singleton,version) VALUES(1,2)`); err == nil {
		t.Fatal("duplicate schema authority row accepted")
	}
}

func TestCurrentStartupRejectsMissingSchemaAuthority(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM tpahub_schema`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(dir); err == nil {
		t.Fatal("missing schema authority accepted")
	}
}

func TestCurrentStartupDoesNotRunHistoricalMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A current database must reopen without invoking migration code.
	migrationFault = func(stage string) error { panic("migration invoked: " + stage) }
	s, err = Open(dir)
	migrationFault = nil
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
