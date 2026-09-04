package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
	"golang.org/x/sys/unix"
)

func testServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	d := t.TempDir()
	db, e := store.Open(d)
	if e != nil {
		t.Fatal(e)
	}
	_ = db.CreateAccount(context.Background(), store.Account{ID: "a", Email: "a@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2024-01-01T00:00:00Z"}, defaultTPAGrants("2024-01-01T00:00:00Z")...)
	_ = db.CreateSession(context.Background(), store.Session{ID: "s", AccountID: "a", TokenPrefix: "tph_s_test"}, store.HashToken("tph_s_test"))
	s := &server{cfg: config.Config{DataDir: d, MaxUpload: 1024, RegistrationEnabled: true}, db: db, runner: tpa.Runner{}}
	h := httptest.NewServer(s.routes())
	t.Cleanup(func() { h.Close(); db.Close() })
	return s, h
}

type copyingPublicationRunner struct{}

func (copyingPublicationRunner) Check(context.Context) error { return nil }
func (copyingPublicationRunner) Publish(_ context.Context, request tpa.PublishRequest) error {
	if err := os.MkdirAll(request.Output, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(request.Input)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		in, err := os.Open(filepath.Join(request.Input, entry.Name()))
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(request.Output, entry.Name()))
		if err == nil {
			_, err = io.Copy(out, in)
			out.Close()
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type blockingPublicationRunner struct{ started, release chan struct{} }

func (r *blockingPublicationRunner) Check(context.Context) error { return nil }
func (r *blockingPublicationRunner) Publish(_ context.Context, request tpa.PublishRequest) error {
	if err := os.MkdirAll(request.Output, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(request.Output, "Packages"), []byte("valid"), 0644); err != nil {
		return err
	}
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return nil
}

func TestPublicationPanicRestartMatrixPreservesNextPublish(t *testing.T) {
	for _, stage := range []string{"candidate-built", "candidate-verified", "before-live-swap", "after-live-swap", "after-durable-record", "before-job-success"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := testServer(t)
			s.ctx = context.Background()
			s.runner = copyingPublicationRunner{}
			ctx := context.Background()
			repositoryID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
			if err := s.db.CreateRepository(ctx, store.Repository{ID: repositoryID, AccountID: "a", Name: stage, CreatedAt: "now"}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
			old := filepath.Join(root, "generations", "job-old")
			if err := os.MkdirAll(old, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(old, "old.deb"), []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("generations", "job-old"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(ctx, repositoryID, "job-old", "now"); err != nil {
				t.Fatal(err)
			}
			incoming := filepath.Join(s.cfg.DataDir, "incoming", "job-new")
			if err := os.MkdirAll(incoming, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(incoming, "new.deb"), []byte("new"), 0644); err != nil {
				t.Fatal(err)
			}
			job := store.Job{ID: "job-new", RepositoryID: repositoryID, State: "running", IncomingPath: incoming, CreatedAt: "now"}
			if err := s.db.CreateJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			s.publicationFault = func(found string) error {
				if found == stage {
					panic("simulated process death")
				}
				return nil
			}
			func() { defer func() { _ = recover() }(); s.process(job) }()
			s.db.Close()
			db, err := store.Open(s.cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s2 := &server{cfg: s.cfg, db: db, runner: copyingPublicationRunner{}, ctx: context.Background()}
			if err = s2.reconcilePublicationState(); err != nil {
				t.Fatal(err)
			}
			if err = s2.reconcileStagingState(); err != nil {
				t.Fatal(err)
			}
			target, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
			if err != nil {
				t.Fatal(err)
			}
			expected := "job-old"
			if stage == "after-durable-record" || stage == "before-job-success" {
				expected = "job-new"
			}
			if filepath.Base(target) != expected {
				t.Fatalf("stage=%s target=%s expected=%s", stage, filepath.Base(target), expected)
			}
			nextIncoming := filepath.Join(s.cfg.DataDir, "incoming", "job-next")
			if err := os.MkdirAll(nextIncoming, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nextIncoming, "next.deb"), []byte("next"), 0644); err != nil {
				t.Fatal(err)
			}
			next := store.Job{ID: "job-next", RepositoryID: repositoryID, State: "running", IncomingPath: nextIncoming, CreatedAt: "now2"}
			if err := db.CreateJob(ctx, next); err != nil {
				t.Fatal(err)
			}
			s2.process(next)
			for _, name := range []string{"old.deb", "next.deb"} {
				if _, err := os.Stat(filepath.Join(root, "current", name)); err != nil {
					t.Fatalf("stage=%s missing %s: %v", stage, name, err)
				}
			}
		})
	}
}

func TestSuspensionImmediatelyBeforeLiveSwapBlocksPublication(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	repositoryID := "abababababababababababababababab"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "blocked", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "job-blocked")
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "input.deb"), []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "job-blocked", RepositoryID: repositoryID, State: "running", IncomingPath: incoming, CreatedAt: "now"}
	if err := s.db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	runner := &blockingPublicationRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	s.runner = runner
	done := make(chan struct{})
	go func() { s.process(job); close(done) }()
	<-runner.started
	if err := s.db.SetAccountStatus(context.Background(), "a", "suspended"); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	<-done
	if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current")); !os.IsNotExist(err) {
		t.Fatalf("suspended publication changed pointer: %v", err)
	}
}

func TestGrantRevocationImmediatelyBeforeLiveSwapBlocksPublication(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	expires := clock.Add(time.Hour)
	repositoryID := "adadadadadadadadadadadadadadadad"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "revoked", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "job-revoked")
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "input.deb"), []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "job-revoked", RepositoryID: repositoryID, State: "running", IncomingPath: incoming, CreatedAt: "now"}
	if err := s.db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	grant := commerce.Grant{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryPublish, OfferID: "nano", Enabled: true, ValidFrom: clock.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 1000000}}
	calls := 0
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		calls++
		if calls >= 3 && !clock.Before(expires) {
			return commerce.Grant{}, commerce.ErrGrantInactive
		}
		return grant, nil
	})
	runner := &blockingPublicationRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	s.runner = runner
	done := make(chan struct{})
	go func() { s.process(job); close(done) }()
	<-runner.started
	clock = expires.Add(time.Nanosecond)
	close(runner.release)
	<-done
	if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current")); !os.IsNotExist(err) {
		t.Fatalf("revoked grant changed pointer: %v", err)
	}
}

func TestDiskFullPublicationCheckpointPreservesPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, "generations", "job-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "job-1"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	s, _ := testServer(t)
	s.publicationFault = func(string) error { return unix.ENOSPC }
	if err := s.publicationCheckpoint("before-live-swap"); err != unix.ENOSPC {
		t.Fatalf("disk-full checkpoint error=%v", err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != "job-1" {
		t.Fatalf("previous generation target=%q err=%v", target, err)
	}
}

func TestSigningFailurePreservesPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "failing-tpa")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, "generations", "job-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "job-1"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := (tpa.Runner{Bin: bin}).Publish(context.Background(), tpa.PublishRequest{Input: "input", Output: filepath.Join(root, "generations", "job-2")}); err == nil {
		t.Fatal("signing failure was accepted")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != "job-1" {
		t.Fatalf("previous generation target=%q err=%v", target, err)
	}
}

func TestRenameFailurePreservesPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, "generations", "job-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "job-1"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceSymlink(filepath.Join(root, "missing-next"), filepath.Join(root, "current")); err == nil {
		t.Fatal("rename failure was accepted")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != "job-1" {
		t.Fatalf("previous generation target=%q err=%v", target, err)
	}
}

func TestGenerationRetentionPreservesActiveAndRecentHistory(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MaxGenerations = 2
	ctx := context.Background()
	repositoryID := "cccccccccccccccccccccccccccccccc"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repositoryID, AccountID: "a", Name: "retention", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations")
	for i, generation := range []string{"job-1", "job-2", "job-3"} {
		if err := os.MkdirAll(filepath.Join(root, generation), 0755); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(filepath.Join(root, generation), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.SetActiveGeneration(ctx, repositoryID, "job-3", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.collectOldGenerations(repositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "job-3")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "job-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "job-1")); !os.IsNotExist(err) {
		t.Fatalf("old generation retained: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "job-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateJob(ctx, store.Job{ID: "job-1", RepositoryID: repositoryID, State: "queued", IncomingPath: "incoming", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.collectOldGenerations(repositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "job-1")); err != nil {
		t.Fatalf("nonterminal generation collected: %v", err)
	}
}

func TestNextGenerationRetainsPreviousArtifacts(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	for _, generation := range []string{"job-1", "job-2"} {
		if err := os.MkdirAll(filepath.Join(root, "generations", generation), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("generations", "job-1"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(root, "current.next")
	if err := os.Symlink(filepath.Join("generations", "job-2"), next); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceSymlink(next, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "generations", "job-1")); err != nil {
		t.Fatalf("previous generation removed: %v", err)
	}
}

func TestPublicationFaultInjectionCheckpoints(t *testing.T) {
	s, _ := testServer(t)
	seen := ""
	s.publicationFault = func(stage string) error {
		seen = stage
		return errors.New("injected publication interruption")
	}
	if err := s.publicationCheckpoint("before-live-swap"); err == nil || seen != "before-live-swap" {
		t.Fatalf("fault checkpoint err=%v stage=%q", err, seen)
	}
}

func TestDiskHeadroomGuardFailsClosed(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MinDiskFreeBytes = 1 << 60
	if err := s.checkDiskHeadroom(); err == nil {
		t.Fatal("disk headroom guard accepted impossible reserve")
	}
}

func TestUploadStagingRespectsStorageGrant(t *testing.T) {
	s, _ := testServer(t)
	now := time.Now().UTC()
	until := now.AddDate(1, 0, 0)
	s.cfg.MaxStagingBytes = 3
	if err := s.db.PutGrant(context.Background(), commerce.Grant{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryPublish, OfferID: "nano", Enabled: true, ValidFrom: now.Add(-time.Minute), ValidUntil: &until, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 100}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.DataDir, "incoming", "queued"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"queued", "queued-2", "queued-3"} {
		dir := filepath.Join(s.cfg.DataDir, "incoming", job)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.deb"), []byte("123"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.checkStagingCapacity(context.Background(), "a", 1, false); err == nil {
		t.Fatal("staging overage was accepted")
	}
}

func TestPublicationCandidateMustBeComplete(t *testing.T) {
	dir := t.TempDir()
	if err := verifyPublicationCandidate(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing candidate accepted")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0755); err != nil {
		t.Fatal(err)
	}
	if err := verifyPublicationCandidate(empty); err == nil {
		t.Fatal("empty candidate accepted")
	}
	if err := os.WriteFile(filepath.Join(empty, "Packages"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyPublicationCandidate(empty); err != nil {
		t.Fatalf("complete candidate rejected: %v", err)
	}
}

func TestRepositoryGenerationsUseActivePointer(t *testing.T) {
	s, _ := testServer(t)
	root := filepath.Join(s.cfg.DataDir, "repos", "generation-test")
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: "generation-test", AccountID: "a", Name: "generation-test", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(root, "generations", "job-1")
	if err := os.MkdirAll(generation, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "Packages"), []byte("generation-1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "job-1"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if got := s.activeRepositoryRoot(root); got != filepath.Join(root, "current") {
		t.Fatalf("active root=%q", got)
	}
	if err := s.db.SetActiveGeneration(context.Background(), "generation-test", "job-1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.db.ActiveGeneration(context.Background(), "generation-test"); err != nil || got != "job-1" {
		t.Fatalf("durable generation=%q err=%v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(s.activeRepositoryRoot(root), "Packages"))
	if err != nil || string(data) != "generation-1" {
		t.Fatalf("active generation data=%q err=%v", data, err)
	}
}

func TestStartupCleansAbandonedStagingWorkspaces(t *testing.T) {
	s, _ := testServer(t)
	work := filepath.Join(s.cfg.DataDir, "work", "abandoned-job")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "partial"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("abandoned workspace remains: %v", err)
	}
}

func TestStartupReconcilesPublicationPointerFromDurableState(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	root := filepath.Join(s.cfg.DataDir, "repos", "reconcile-test")
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "reconcile-test", AccountID: "a", Name: "reconcile-test", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"job-1", "job-2"} {
		if err := os.MkdirAll(filepath.Join(root, "generations", generation), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("generations", "job-2"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(ctx, "reconcile-test", "job-1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != "job-1" {
		t.Fatalf("reconciled target=%q err=%v", target, err)
	}
	if err = os.Remove(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	if target, err = filepath.EvalSymlinks(filepath.Join(root, "current")); err != nil || filepath.Base(target) != "job-1" {
		t.Fatalf("missing pointer target=%q err=%v", target, err)
	}
	if err = os.Remove(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join("generations", "missing"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	if target, err = filepath.EvalSymlinks(filepath.Join(root, "current")); err != nil || filepath.Base(target) != "job-1" {
		t.Fatalf("broken pointer target=%q err=%v", target, err)
	}
}

func TestPublicationJobSuccessRequiresActiveGeneration(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "bound-repo", AccountID: "a", Name: "bound-repo", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(ctx, "bound-repo", "other-job", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.finishPublishedJob(store.Job{ID: "job-1", RepositoryID: "bound-repo"}); err == nil {
		t.Fatal("job with non-active generation was marked successful")
	}
}

func TestSuspendedAccountCannotRunQueuedPublication(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	repositoryID := "suspended-publish"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repositoryID, AccountID: "a", Name: "suspended-publish", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetAccountStatus(ctx, "a", "suspended"); err != nil {
		t.Fatal(err)
	}
	if err := s.ensurePublishAccountActive(repositoryID); err == nil {
		t.Fatal("suspended account was allowed to publish")
	}
}

func TestHTTPRepositoryAndServing(t *testing.T) {
	s, h := testServer(t)
	req, _ := http.NewRequest("POST", h.URL+"/v1/repositories", strings.NewReader(`{"name":"demo","visibility":"public"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tph_s_test")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	var r map[string]string
	json.NewDecoder(resp.Body).Decode(&r)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	id := r["id"]
	if _, e = os.Stat(filepath.Join(s.cfg.DataDir, "repos", id)); e != nil {
		t.Fatal(e)
	}
	req, _ = http.NewRequest("GET", h.URL+"/v1/repositories/"+id, nil)
	req.Header.Set("Authorization", "Bearer tph_s_test")
	resp, e = http.DefaultClient.Do(req)
	if e != nil || resp.StatusCode != 200 {
		t.Fatalf("lookup: %v %v", resp, e)
	}
	os.WriteFile(filepath.Join(s.cfg.DataDir, "repos", id, "file"), []byte("ok"), 0644)
	resp, e = http.Get(h.URL + "/r/" + id + "/file")
	if e != nil || resp.StatusCode != 200 {
		t.Fatalf("file: %v %v", resp, e)
	}
	resp, e = http.Get(h.URL + "/r/" + id + "/../tpahub.db")
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode == 200 {
		t.Fatal("path escaped repository")
	}
}
func TestHTTPInvalidUpload(t *testing.T) {
	_, h := testServer(t)
	resp, _ := http.Post(h.URL+"/v1/repositories/not-an-id/publishes?filename=x.deb", "application/octet-stream", strings.NewReader("x"))
	if resp.StatusCode != 401 && resp.StatusCode != 404 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
