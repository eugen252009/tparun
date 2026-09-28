package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPublicationQueueStatusUsesPersistedJobAndSessionStates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, id := range []string{"owner-a", "owner-b"} {
		if err := db.CreateAccount(ctx, Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"repo-a", "repo-b"} {
		owner := []string{"owner-a", "owner-b"}[i]
		if err := db.CreateRepository(ctx, Repository{ID: id, AccountID: owner, Name: id, Visibility: "private", CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() PublicationQueueStatus {
		t.Helper()
		status, err := db.PublicationQueueStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	if got := read(); got != (PublicationQueueStatus{}) {
		t.Fatalf("empty store status=%+v", got)
	}

	if err := db.CreateJob(ctx, Job{ID: "queued-job", RepositoryID: "repo-a", PublisherAccountID: "owner-a", State: "queued", IncomingPath: "/incoming/queued-job", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.QueuedJobs != 1 || got.RunningJobs != 0 || got.IncompleteSessions != 0 {
		t.Fatalf("queued job status=%+v", got)
	}
	if _, err := db.db.Exec(`UPDATE publish_jobs SET state='running',started_at='now' WHERE id='queued-job'`); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateJob(ctx, Job{ID: "succeeded-job", RepositoryID: "repo-a", PublisherAccountID: "owner-a", State: "succeeded", IncomingPath: "/incoming/succeeded-job", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateJob(ctx, Job{ID: "failed-job", RepositoryID: "repo-b", PublisherAccountID: "owner-b", State: "failed", IncomingPath: "/incoming/failed-job", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.QueuedJobs != 0 || got.RunningJobs != 1 || got.IncompleteSessions != 0 {
		t.Fatalf("running plus terminal jobs status=%+v", got)
	}
	if err := db.Finish(ctx, "queued-job", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (PublicationQueueStatus{}) {
		t.Fatalf("terminal jobs blocked idle status=%+v", got)
	}

	artifact := PublicationArtifact{Filename: "fixture.deb", Package: "fixture", Version: "1", Architecture: "all", UploadedSize: 1, UploadedSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := db.CreatePublicationSession(ctx, PublicationSession{ID: "open-session", RepositoryID: "repo-a", OwnerAccountID: "owner-a", IncomingPath: "/incoming/open-session", ExpectedCount: 1, CreatedAt: "now"}, []PublicationArtifact{artifact}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.QueuedJobs != 0 || got.RunningJobs != 0 || got.IncompleteSessions != 1 {
		t.Fatalf("open session status=%+v", got)
	}
	if _, err := db.db.Exec(`UPDATE publication_sessions SET state='finalized' WHERE id='open-session'`); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (PublicationQueueStatus{}) {
		t.Fatalf("finalized session blocked idle status=%+v", got)
	}
	if err := db.CreatePublicationSession(ctx, PublicationSession{ID: "aborted-session", RepositoryID: "repo-b", OwnerAccountID: "owner-b", IncomingPath: "/incoming/aborted-session", ExpectedCount: 1, CreatedAt: "now"}, []PublicationArtifact{artifact}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE publication_sessions SET state='aborted' WHERE id='aborted-session'`); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (PublicationQueueStatus{}) {
		t.Fatalf("aborted session blocked idle status=%+v", got)
	}
}
