package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationSessionBatchIdempotencyAndAtomicFinalization(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-09-27T00:00:00Z"
	if err = s.CreateAccount(ctx, Account{ID: "session-owner", Email: "session-owner@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "session-repo", AccountID: "session-owner", Name: "session", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	const sessionID = "abababababababababababababababab"
	expected := []PublicationArtifact{
		{Filename: "a.deb", Package: "a", Version: "1", Architecture: "amd64", UploadedSize: 10, UploadedSHA256: strings.Repeat("a", 64)},
		{Filename: "b.deb", Package: "b", Version: "1", Architecture: "amd64", UploadedSize: 20, UploadedSHA256: strings.Repeat("b", 64)},
	}
	if err = s.CreatePublicationSession(ctx, PublicationSession{ID: sessionID, RepositoryID: "session-repo", OwnerAccountID: "session-owner", IncomingPath: "/data/incoming/" + sessionID, ExpectedCount: 2, CreatedAt: now}, expected); err != nil {
		t.Fatal(err)
	}
	firstDigest := strings.Repeat("1", 64)
	if err = s.RecordPublicationSessionBatch(ctx, sessionID, "session-owner", 0, firstDigest, now, expected[:1]); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.PublicationSessionBatchReplay(ctx, sessionID, "session-owner", 0, firstDigest); err != nil || !replay {
		t.Fatalf("same batch retry replay=%t err=%v", replay, err)
	}
	if _, err = s.PublicationSessionBatchReplay(ctx, sessionID, "session-owner", 0, strings.Repeat("2", 64)); err == nil {
		t.Fatal("different batch retry accepted")
	}
	if err = s.FinalizePublicationSession(ctx, sessionID, "session-owner", now); err == nil {
		t.Fatal("incomplete session finalized")
	}
	if err = s.RecordPublicationSessionBatch(ctx, sessionID, "session-owner", 1, strings.Repeat("3", 64), now, expected[1:]); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizePublicationSession(ctx, sessionID, "session-owner", now); err != nil {
		t.Fatal(err)
	}
	job, err := s.Job(ctx, sessionID)
	if err != nil || job.State != "queued" || job.IncomingPath != "/data/incoming/"+sessionID {
		t.Fatalf("final job=%+v err=%v", job, err)
	}
	artifacts, err := s.PublicationArtifacts(ctx, sessionID)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("final artifacts=%d err=%v", len(artifacts), err)
	}
	session, err := s.PublicationSession(ctx, sessionID)
	if err != nil || session.State != "finalized" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
}

func TestSchema11To12MigrationPreservesExistingState(t *testing.T) {
	dir := populatedBoundaryFixture(t)
	path := filepath.Join(dir, "tpahub.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE publication_session_batches; DROP TABLE publication_session_artifacts; DROP TABLE publication_sessions; UPDATE tpahub_schema SET version=11 WHERE singleton=1`); err != nil {
		db.Close()
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
	if CurrentSchemaVersion() != 12 {
		t.Fatalf("schema version=%d", CurrentSchemaVersion())
	}
	if _, err = s.AccountByID(context.Background(), "owner"); err != nil {
		t.Fatalf("account lost across migration: %v", err)
	}
	generation, err := s.ActiveGeneration(context.Background(), "public")
	if err != nil || generation != "committed" {
		t.Fatalf("active generation=%q err=%v", generation, err)
	}
	if _, err = s.db.Exec(`INSERT INTO publication_sessions(id,repository_id,owner_account_id,incoming_path,state,expected_count,created_at) VALUES(?,?,?,?,?,?,?)`, "abababababababababababababababab", "public", "owner", "/incoming/session", "open", 1, "now"); err != nil {
		t.Fatalf("Schema 12 session table unavailable: %v", err)
	}
	if !strings.Contains(strings.ToLower(schemaSQL), "publication_session_batches") {
		t.Fatal("canonical schema omitted session batch table")
	}
}
