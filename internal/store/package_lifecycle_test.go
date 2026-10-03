package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPackageLifecycleAuditIdempotencyAndDurableActivation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateAccount(ctx, Account{ID: "owner", Email: "owner@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "repo", AccountID: "owner", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES('repo','parent','now')`); err != nil {
		t.Fatal(err)
	}
	makeJob := func(id, key, operation string) (Job, PackageLifecycleJob) {
		job := Job{ID: id, RepositoryID: "repo", PublisherAccountID: "owner", State: "running", IncomingPath: "/incoming/" + id, CreatedAt: "now", StartedAt: "now"}
		lifecycle := PackageLifecycleJob{JobID: id, RepositoryID: "repo", ActorAccountID: "owner", IdempotencyKey: key, Operation: operation, Package: "example", Version: "1.0", Architecture: "amd64", Confirmed: operation == "delete", ArtifactFilename: "pool/main/e/example/example_1.0_amd64.deb", ArtifactSize: 10, ArtifactSHA256: strings.Repeat("a", 64), MetadataState: "pending", ArtifactState: "pending"}
		return job, lifecycle
	}
	job, lifecycle := makeJob("job-one", strings.Repeat("1", 32), "unlist")
	created, existing, err := s.CreatePackageLifecycleJob(ctx, job, lifecycle, "parent", "owner")
	if err != nil || existing || created.JobID != "job-one" {
		t.Fatalf("create job: %+v existing=%v err=%v", created, existing, err)
	}
	replayJob, replay := makeJob("job-replay", lifecycle.IdempotencyKey, "unlist")
	created, existing, err = s.CreatePackageLifecycleJob(ctx, replayJob, replay, "stale-parent", "owner")
	if err != nil || !existing || created.JobID != "job-one" {
		t.Fatalf("idempotent replay: %+v existing=%v err=%v", created, existing, err)
	}
	conflictJob, conflict := makeJob("job-conflict", lifecycle.IdempotencyKey, "delete")
	if _, _, err = s.CreatePackageLifecycleJob(ctx, conflictJob, conflict, "parent", "owner"); !errors.Is(err, ErrLifecycleIdempotencyConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	if err = s.CommitPackageLifecycleActivation(ctx, "repo", "job-one", "owner", "parent", "published", "retained", "package_unlisted", "", "later", true); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveGeneration(ctx, "repo")
	if err != nil || active != "job-one" {
		t.Fatalf("active generation=%q err=%v", active, err)
	}
	first, err := s.PackageLifecycleGeneration(ctx, "repo", "job-one")
	if err != nil || first.MetadataState != "published" || first.ArtifactState != "retained" {
		t.Fatalf("first lifecycle event=%+v err=%v", first, err)
	}
	job2, lifecycle2 := makeJob("job-two", strings.Repeat("2", 32), "delete")
	if _, _, err = s.CreatePackageLifecycleJob(ctx, job2, lifecycle2, "job-one", "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitPackageLifecycleActivation(ctx, "repo", "job-two", "owner", "job-one", "published", "cleanup_failed", "unlisted_cleanup_failed", "cleanup denied", "later-2", false); err != nil {
		t.Fatal(err)
	}
	jobState, err := s.Job(ctx, "job-two")
	if err != nil || jobState.State != "failed" || jobState.Error != "cleanup denied" || jobState.GenerationID != "job-two" {
		t.Fatalf("partial cleanup job=%+v err=%v", jobState, err)
	}
	history, err := s.LifecycleGenerationHistory(ctx, "repo", "job-two")
	if err != nil || len(history) != 2 || history[0].JobID != "job-two" || history[1].JobID != "job-one" {
		t.Fatalf("lifecycle history=%+v err=%v", history, err)
	}
}

func TestInterruptedPackageLifecycleIsAuditedOnRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateAccount(ctx, Account{ID: "owner", Email: "owner@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "repo", AccountID: "owner", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job", RepositoryID: "repo", PublisherAccountID: "owner", State: "running", IncomingPath: "/incoming/job", CreatedAt: "now", StartedAt: "now"}
	lifecycle := PackageLifecycleJob{JobID: "job", RepositoryID: "repo", ActorAccountID: "owner", IdempotencyKey: strings.Repeat("c", 32), Operation: "delete", Package: "example", Version: "1.0", Architecture: "amd64", Confirmed: true, ArtifactFilename: "pool/main/e/example/example.deb", ArtifactSize: 1, ArtifactSHA256: strings.Repeat("d", 64), MetadataState: "pending", ArtifactState: "pending"}
	if _, _, err = s.CreatePackageLifecycleJob(ctx, job, lifecycle, "", "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.PackageLifecycle(ctx, "job")
	jobState, jobErr := s.Job(ctx, "job")
	if err != nil || jobErr != nil || state.Result != "interrupted" || state.MetadataState != "unchanged" || state.ArtifactState != "retained" || jobState.State != "failed" {
		t.Fatalf("recovered lifecycle=%+v job=%+v err=%v/%v", state, jobState, err, jobErr)
	}
	if err = s.ClaimRepositoryPublication(ctx, "repo", "next-publication", "now"); err != nil {
		t.Fatalf("restart left lifecycle slot claimed: %v", err)
	}
}

func TestPackageLifecycleFailureReleasesRepositorySlot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateAccount(ctx, Account{ID: "owner", Email: "owner@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "repo", AccountID: "owner", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job", RepositoryID: "repo", PublisherAccountID: "owner", State: "running", IncomingPath: "/incoming/job", CreatedAt: "now", StartedAt: "now"}
	lifecycle := PackageLifecycleJob{JobID: "job", RepositoryID: "repo", ActorAccountID: "owner", IdempotencyKey: strings.Repeat("a", 32), Operation: "delete", Package: "example", Version: "1.0", Architecture: "amd64", Confirmed: true, ArtifactFilename: "pool/main/e/example/example.deb", ArtifactSize: 1, ArtifactSHA256: strings.Repeat("b", 64), MetadataState: "pending", ArtifactState: "pending"}
	if _, _, err = s.CreatePackageLifecycleJob(ctx, job, lifecycle, "", "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.FailPackageLifecycle(ctx, "job", "repo", "fixture failure", "later"); err != nil {
		t.Fatal(err)
	}
	if err = s.ClaimRepositoryPublication(ctx, "repo", "next-publication", "now"); err != nil {
		t.Fatalf("lifecycle failure left repository slot claimed: %v", err)
	}
	state, err := s.PackageLifecycle(ctx, "job")
	if err != nil || state.MetadataState != "unchanged" || state.ArtifactState != "retained" || state.Result != "failed" {
		t.Fatalf("failed lifecycle audit=%+v err=%v", state, err)
	}
}
