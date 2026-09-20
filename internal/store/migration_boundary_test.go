package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func populatedBoundaryFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if err = s.CreateAccount(ctx, Account{ID: "owner", Email: "owner@example.invalid", PasswordHash: "fixture-hash", Status: "active", CreatedAt: stamp}, tpahubproduct.DefaultGrants(now)...); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSession(ctx, Session{ID: "session", AccountID: "owner", TokenPrefix: "fixture"}, HashToken("fixture-session")); err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []string{"public", "private"} {
		if err = s.CreateRepository(ctx, Repository{ID: visibility, AccountID: "owner", Name: visibility, Visibility: visibility, CreatedAt: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CreateCredential(ctx, ReaderCredential{ID: "reader", RepositoryID: "private", Name: "reader", CreatedAt: stamp}, HashToken("fixture-reader")); err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveGeneration(ctx, "public", "committed", stamp); err != nil {
		t.Fatal(err)
	}
	if err = s.AddRepositoryEgressDaily(ctx, "public", now.Format("2006-01-02"), 123, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OperatorUserHandle(ctx); err != nil {
		t.Fatal(err)
	}
	return dir
}
func boundarySnapshot(t *testing.T, dir string) (string, [32]byte, string) {
	t.Helper()
	path := filepath.Join(dir, "tpahub.db")
	digest, err := LogicalDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	err = db.QueryRow(`SELECT group_concat(type||'|'||name||'|'||coalesce(sql,''),char(10)) FROM (SELECT type,name,sql FROM sqlite_master ORDER BY type,name)`).Scan(&schema)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest, sha256.Sum256(data), schema
}
func TestMigrateRejectsUnsupportedMetadataWithoutMutation(t *testing.T) {
	cases := map[string]string{
		"pre-release-1-original":        `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(version INTEGER NOT NULL CHECK(version>0)); INSERT INTO tpahub_schema VALUES(1)`,
		"pre-release-1":                 `UPDATE tpahub_schema SET version=1`,
		"pre-release-2":                 `UPDATE tpahub_schema SET version=2`,
		"pre-release-3":                 `UPDATE tpahub_schema SET version=3`,
		"future-ten":                    `UPDATE tpahub_schema SET version=10`,
		"missing-index":                 `DROP INDEX publish_jobs_queue`,
		"missing-column":                `ALTER TABLE operator_credentials DROP COLUMN label`,
		"missing-foreign-key":           `PRAGMA writable_schema=ON; UPDATE sqlite_master SET sql=replace(sql,' REFERENCES accounts(id)','') WHERE name='sessions'; PRAGMA writable_schema=OFF`,
		"missing-current-table":         `DROP TABLE generation_cleanup_tasks`,
		"missing-current-trigger":       `DROP TRIGGER purchase_grant_snapshots_immutable_update`,
		"missing-authority-constraints": `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER PRIMARY KEY,version INTEGER NOT NULL); INSERT INTO tpahub_schema VALUES(1,5)`,
		"future":                        `UPDATE tpahub_schema SET version=999`,
		"pre-release-4":                 `UPDATE tpahub_schema SET version=4`,
		"zero-version":                  `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER,version INTEGER); INSERT INTO tpahub_schema VALUES(1,0)`,
		"negative-version":              `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER,version INTEGER); INSERT INTO tpahub_schema VALUES(1,-1)`,
		"malformed-authority":           `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER); INSERT INTO tpahub_schema VALUES(1)`,
		"empty-authority":               `DELETE FROM tpahub_schema`,
		"missing-authority":             `DROP TABLE tpahub_schema`,
		"text-version":                  `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER,version TEXT); INSERT INTO tpahub_schema VALUES(1,'5')`,
		"fractional-version":            `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER,version REAL); INSERT INTO tpahub_schema VALUES(1,5.5)`,
		"duplicate-authority":           `DROP TABLE tpahub_schema; CREATE TABLE tpahub_schema(singleton INTEGER,version INTEGER); INSERT INTO tpahub_schema VALUES(1,5),(1,3)`,
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			dir := populatedBoundaryFixture(t)
			db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(mutation)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			before, bytesBefore, schemaBefore := boundarySnapshot(t, dir)
			called := false
			migrationFault = func(string) error { called = true; return errors.New("migration must not begin") }
			defer func() { migrationFault = nil }()
			if err := Migrate(dir); err == nil {
				t.Fatal("unsupported metadata migrated")
			}
			if called {
				t.Fatal("unsupported database entered migration")
			}
			after, bytesAfter, schemaAfter := boundarySnapshot(t, dir)
			if after != before || bytesBefore != bytesAfter || schemaBefore != schemaAfter {
				t.Fatal("rejected migration mutated database")
			}
			if s, err := Open(dir); err == nil {
				s.Close()
				t.Fatal("startup disagrees with rejection")
			}
			after, bytesAfter, schemaAfter = boundarySnapshot(t, dir)
			if after != before || bytesBefore != bytesAfter || schemaBefore != schemaAfter {
				t.Fatal("rejected startup mutated database")
			}
		})
	}
}
func TestMigrateRejectsUnrecognizedUnversionedDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE unrelated(authority TEXT); INSERT INTO unrelated VALUES('preserve')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, bytesBefore, schemaBefore := boundarySnapshot(t, dir)
	if err := Migrate(dir); err == nil {
		t.Fatal("unrecognized database migrated")
	}
	after, bytesAfter, schemaAfter := boundarySnapshot(t, dir)
	if before != after || bytesBefore != bytesAfter || schemaBefore != schemaAfter {
		t.Fatal("unrecognized database mutated")
	}
}

func TestCurrentMigrationIsNoOpOnPopulatedAuthority(t *testing.T) {
	dir := populatedBoundaryFixture(t)
	before, bytesBefore, schema := boundarySnapshot(t, dir)
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	after, bytesAfter, afterSchema := boundarySnapshot(t, dir)
	if before != after || bytesBefore != bytesAfter || schema != afterSchema {
		t.Fatal("current migration changed authority")
	}
	s, err := OpenForAdmin(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if id, err := s.AccountByToken(context.Background(), HashToken("fixture-session")); err != nil || id != "owner" {
		t.Fatal("session changed")
	}
	if ok, err := s.ReaderRepository(context.Background(), "private", HashToken("fixture-reader")); err != nil || !ok {
		t.Fatal("reader changed")
	}
	if generation, err := s.ActiveGeneration(context.Background(), "public"); err != nil || generation != "committed" {
		t.Fatal("publication changed")
	}
}
