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
