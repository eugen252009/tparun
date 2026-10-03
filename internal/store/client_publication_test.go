package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestSchema13To15PublicationStateMigrationsAreAtomic(t *testing.T) {
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
	if _, err = db.Exec(`DROP INDEX package_lifecycle_history; DROP INDEX package_lifecycle_generation; DROP TABLE package_lifecycle_jobs; DROP TABLE repository_publication_slots; UPDATE tpahub_schema SET version=13 WHERE singleton=1`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrationFault = func(stage string) error {
		if stage == "before-schema-14-version" {
			return errors.New("injected Schema 14 migration failure")
		}
		return nil
	}
	if err = Migrate(dir); err == nil {
		t.Fatal("injected Schema 13 to 14 failure was ignored")
	}
	migrationFault = nil
	db, err = sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil || version != 13 {
		t.Fatalf("failed migration version=%d err=%v", version, err)
	}
	var tables int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='repository_publication_slots'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("failed migration slot table count=%d err=%v", tables, err)
	}
	_ = db.Close()
	if err = Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil || version != 15 {
		t.Fatalf("migrated schema=%d err=%v", version, err)
	}
}

func TestSchema14To15LifecycleMigrationIsAtomic(t *testing.T) {
	dir := t.TempDir()
	dbStore, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = dbStore.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP INDEX package_lifecycle_history; DROP INDEX package_lifecycle_generation; DROP TABLE package_lifecycle_jobs; UPDATE tpahub_schema SET version=14 WHERE singleton=1`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrationFault = func(stage string) error {
		if stage == "before-schema-15-version" {
			return errors.New("injected Schema 15 migration failure")
		}
		return nil
	}
	if err = Migrate(dir); err == nil {
		t.Fatal("injected Schema 14 to 15 failure was ignored")
	}
	migrationFault = nil
	db, err = sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	var version, tables int
	if err = db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil || version != 14 {
		t.Fatalf("failed migration version=%d err=%v", version, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='package_lifecycle_jobs'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("failed migration lifecycle table count=%d err=%v", tables, err)
	}
	_ = db.Close()
	if err = Migrate(dir); err != nil {
		t.Fatal(err)
	}
	dbStore, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()
	if CurrentSchemaVersion() != 15 {
		t.Fatalf("migrated schema=%d", CurrentSchemaVersion())
	}
}

func TestRepositoryPublicationSlotIsDurablePerRepositoryAcrossStoreHandles(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.db.Exec(`INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('owner','owner@example.invalid','x','active','now')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"repo-a", "repo-b"} {
		if err := first.CreateRepository(context.Background(), Repository{ID: id, AccountID: "owner", Name: id, CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	second, err := OpenForAdmin(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	type claimResult struct {
		id  string
		err error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, claim := range []struct {
		store *Store
		id    string
	}{{first, "publisher-one"}, {second, "publisher-two"}} {
		wg.Add(1)
		go func(claim struct {
			store *Store
			id    string
		}) {
			defer wg.Done()
			<-start
			results <- claimResult{id: claim.id, err: claim.store.ClaimRepositoryPublication(context.Background(), "repo-a", claim.id, "now")}
		}(claim)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	var winningID string
	for result := range results {
		if result.err == nil {
			winners++
			winningID = result.id
		} else if !errors.Is(result.err, ErrRepositoryBusy) {
			t.Fatalf("unexpected concurrent claim error: %v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claim winners=%d, want 1", winners)
	}
	if err = first.ReleaseRepositoryPublication(context.Background(), "repo-a", winningID); err != nil {
		t.Fatal(err)
	}
	if err = first.ClaimRepositoryPublication(context.Background(), "repo-a", "owner", "now"); err != nil {
		t.Fatal(err)
	}
	if err = second.ReleaseRepositoryPublication(context.Background(), "repo-a", "not-owner"); err != nil {
		t.Fatal(err)
	}
	if err = first.ClaimRepositoryPublication(context.Background(), "repo-a", "intruder", "now"); !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("conflicting claim error=%v", err)
	}
	if err = second.ClaimRepositoryPublication(context.Background(), "repo-b", "independent", "now"); err != nil {
		t.Fatalf("different repository blocked: %v", err)
	}
	if err = first.ReleaseRepositoryPublication(context.Background(), "repo-a", "owner"); err != nil {
		t.Fatal(err)
	}
	if err = second.ClaimRepositoryPublication(context.Background(), "repo-a", "next", "now"); err != nil {
		t.Fatalf("claim after owner release: %v", err)
	}
}

func TestRepositoryPublicationSlotIsReleasedOnRestartAndAtomicActivation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('owner','owner@example.invalid','x','active','now')`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, Repository{ID: "repo", AccountID: "owner", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimRepositoryPublication(ctx, "repo", "interrupted", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ClaimRepositoryPublication(ctx, "repo", "recovered", "now"); err != nil {
		t.Fatalf("restart did not release abandoned claim: %v", err)
	}
	if err := s.CreateJob(ctx, Job{ID: "generation", RepositoryID: "repo", State: "running", IncomingPath: "incoming", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	at := "2030-01-01T00:00:00Z"
	if err := s.CommitAuthorizedPublication(ctx, AuthorizedPublication{RepositoryID: "repo", JobID: "generation", OwnerAccountID: "owner", ExpectedParent: "", CheckParent: true, PublicationID: "recovered"}, at, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimRepositoryPublication(ctx, "repo", "after-activation", "now"); err != nil {
		t.Fatalf("activation transaction did not release its exact slot: %v", err)
	}
}

func TestManagedWorkerSkipsRepositoryWithClientPublicationSlot(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('owner','owner@example.invalid','x','active','now')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"repo-a", "repo-b"} {
		if err := s.CreateRepository(ctx, Repository{ID: id, AccountID: "owner", Name: id, CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClaimRepositoryPublication(ctx, "repo-a", "client-upload", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateJob(ctx, Job{ID: "job-a", RepositoryID: "repo-a", State: "queued", IncomingPath: "incoming-a", CreatedAt: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateJob(ctx, Job{ID: "job-b", RepositoryID: "repo-b", State: "queued", IncomingPath: "incoming-b", CreatedAt: "2"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx)
	if err != nil || job.ID != "job-b" {
		t.Fatalf("claimed job=%+v err=%v, want independent repo-b job", job, err)
	}
}
