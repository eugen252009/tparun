package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	s := &server{cfg: config.Config{DataDir: d, MaxUpload: 1024, RegistrationEnabled: true}, db: db, runner: tpa.Runner{}, verifyCandidate: func(candidate, _ string) error {
		entries, err := os.ReadDir(candidate)
		if err != nil || len(entries) == 0 {
			return fmt.Errorf("invalid test candidate")
		}
		return nil
	}}
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
func (r copyingPublicationRunner) PublishBounded(ctx context.Context, request tpa.PublishRequest, _ int64) error {
	return r.Publish(ctx, request)
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
func (r *blockingPublicationRunner) PublishBounded(ctx context.Context, request tpa.PublishRequest, _ int64) error {
	return r.Publish(ctx, request)
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
			s2 := &server{cfg: s.cfg, db: db, runner: copyingPublicationRunner{}, ctx: context.Background(), verifyCandidate: s.verifyCandidate}
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
					entries, _ := os.ReadDir(filepath.Join(root, "current"))
					t.Fatalf("stage=%s missing %s: %v entries=%v", stage, name, err, entries)
				}
			}
		})
	}
}

func TestConcurrentUploadAndWorkerPathsShareCapacityAuthority(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	s.cfg.MaxUpload = 1024
	s.cfg.MaxStagingBytes = 4 << 20
	s.cfg.MinDiskFreeBytes = 0
	if err := s.db.CreateAccount(context.Background(), store.Account{ID: "b", Email: "worker-b@invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}, defaultTPAGrants("2024-01-01T00:00:00Z")...); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, account string }{{"worker-repo-a", "a"}, {"worker-repo-b", "b"}} {
		if err := s.db.CreateRepository(context.Background(), store.Repository{ID: item.id, AccountID: item.account, Name: item.id, CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	uploadJob := func(account, repo string) {
		r := httptest.NewRequest(http.MethodPost, "/?filename=package.deb", bytes.NewReader([]byte("payload")))
		r.ContentLength = 7
		r = r.WithContext(context.WithValue(r.Context(), accountKey, account))
		w := httptest.NewRecorder()
		s.upload(w, r, repo)
		if w.Code != http.StatusAccepted {
			t.Fatalf("upload %s returned %d", account, w.Code)
		}
	}
	uploadJob("a", "worker-repo-a")
	uploadJob("b", "worker-repo-b")
	first, err := s.db.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.db.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := &blockingPublicationRunner{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	s.runner = runner
	firstDone := make(chan struct{})
	go func() { s.process(first); close(firstDone) }()
	<-runner.started
	physical := int64(0)
	for _, root := range []string{filepath.Join(s.cfg.DataDir, "incoming"), filepath.Join(s.cfg.DataDir, "work")} {
		allocated, allocationErr := fileTreeAllocatedSize(root)
		if allocationErr != nil {
			t.Fatal(allocationErr)
		}
		physical += allocated
	}
	if physical > s.cfg.MaxStagingBytes {
		t.Fatalf("physical staging crossed policy at worker barrier: %d > %d", physical, s.cfg.MaxStagingBytes)
	}
	secondDone := make(chan struct{})
	go func() { s.process(second); close(secondDone) }()
	uploadDone := make(chan int, 2)
	var wg sync.WaitGroup
	for _, item := range []struct{ account, repo string }{{"a", "worker-repo-a"}, {"b", "worker-repo-b"}} {
		wg.Add(1)
		go func(account, repo string) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/?filename=second.deb", bytes.NewReader([]byte("payload")))
			r.ContentLength = 7
			r = r.WithContext(context.WithValue(r.Context(), accountKey, account))
			w := httptest.NewRecorder()
			s.upload(w, r, repo)
			uploadDone <- w.Code
		}(item.account, item.repo)
	}
	select {
	case <-runner.started:
		t.Fatal("second worker bypassed staging serialization")
	case <-time.After(20 * time.Millisecond):
	}
	runner.release <- struct{}{}
	<-firstDone
	<-runner.started
	runner.release <- struct{}{}
	<-secondDone
	wg.Wait()
	close(uploadDone)
	for code := range uploadDone {
		if code != http.StatusAccepted {
			t.Fatalf("concurrent upload lost capacity arbitration: %d", code)
		}
	}
	if len(s.reservations) != 0 {
		t.Fatalf("reservations leaked after workers/uploads: %#v", s.reservations)
	}
}

func TestConcurrentUploadsUseRealReservationPath(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MaxUpload = 10
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxStagingBytes = 5 * block
	s.cfg.MinDiskFreeBytes = 0
	if err := s.db.CreateAccount(context.Background(), store.Account{ID: "b", Email: "b@invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}, defaultTPAGrants("2024-01-01T00:00:00Z")...); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, account string }{{"repo-a", "a"}, {"repo-b", "b"}} {
		if err := s.db.CreateRepository(context.Background(), store.Repository{ID: item.id, AccountID: item.account, Name: item.id, CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan int, 3)
	var wg sync.WaitGroup
	for _, item := range []struct{ account, repo string }{{"a", "repo-a"}, {"a", "repo-a"}, {"b", "repo-b"}} {
		wg.Add(1)
		go func(account, repo string) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/?filename=package.deb", bytes.NewReader([]byte("1234567890")))
			r.ContentLength = 10
			r = r.WithContext(context.WithValue(r.Context(), accountKey, account))
			w := httptest.NewRecorder()
			s.upload(w, r, repo)
			results <- w.Code
		}(item.account, item.repo)
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	codes := []int{}
	for code := range results {
		codes = append(codes, code)
		if code == http.StatusAccepted {
			accepted++
		} else if code == http.StatusRequestEntityTooLarge || code == http.StatusInsufficientStorage {
			rejected++
		}
	}
	if accepted != 2 || rejected != 1 {
		t.Fatalf("concurrent upload results accepted=%d rejected=%d codes=%v", accepted, rejected, codes)
	}
	if count := countJobs(t, s); count != 2 {
		t.Fatalf("rejected upload created a job: %d", count)
	}
}

func countJobs(t *testing.T, s *server) int {
	t.Helper()
	j, err := s.db.Claim(context.Background())
	if err != nil {
		return 0
	}
	if err = s.db.Finish(context.Background(), j.ID, "failed", "test"); err != nil {
		t.Fatal(err)
	}
	return 1 + countJobsIfAny(s)
}
func countJobsIfAny(s *server) int {
	j, err := s.db.Claim(context.Background())
	if err != nil {
		return 0
	}
	_ = s.db.Finish(context.Background(), j.ID, "failed", "test")
	return 1
}

func TestCandidateOutputBeyondReservationCannotActivate(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	s.cfg.PublishTimeout = time.Minute
	s.cfg.MinDiskFreeBytes = 0
	repositoryID := "ffffffffffffffffffffffffffffffff"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "oversized", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "oversized-job")
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "input.deb"), []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "oversized-job", RepositoryID: repositoryID, State: "running", IncomingPath: incoming, CreatedAt: "now"}
	if err := s.db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "oversized-tpa")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nout=''\nwhile [ \"$#\" -gt 0 ]; do [ \"$1\" = '--atomic-publish' ] && { shift; out=$1; }; shift; done\nmkdir -p \"$out\"\nfor n in 1 2 3; do dd if=/dev/zero of=\"$out/blob-$n\" bs=524288 count=1 status=none; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	s.runner = tpa.Runner{Bin: bin}
	s.process(job)
	if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current")); !os.IsNotExist(err) {
		t.Fatalf("oversized candidate became live: %v", err)
	}
	got, err := s.db.Job(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" {
		t.Fatalf("oversized job state=%s", got.State)
	}
	if !strings.Contains(strings.ToLower(got.Error), "space") {
		t.Fatalf("candidate hard bound did not return ENOSPC: %s", got.Error)
	}
	if len(s.reservations) != 0 {
		t.Fatalf("reservation leaked: %#v", s.reservations)
	}
}

func TestCapacityReservationDiskBoundariesAndCandidatePolicy(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MaxStagingBytes = 1 << 20
	s.cfg.MinDiskFreeBytes = 1000
	free := int64(1100)
	s.diskFree = func() (int64, error) { return free, nil }
	s.stagingMu.Lock()
	if err := s.reserveCapacity(context.Background(), "a", "repo", "job-in", "incoming", 100); err != nil {
		t.Fatal(err)
	}
	s.releaseCapacity("job-in")
	if err := s.reserveCapacity(context.Background(), "", "repo", "job-candidate", "candidate", 100); err != nil {
		t.Fatal(err)
	}
	if got := s.reservations["job-candidate"].AccountID; got != "" {
		t.Fatalf("candidate was unexpectedly account-attributed to %q", got)
	}
	s.releaseCapacity("job-candidate")
	free = 1099
	if err := s.reserveCapacity(context.Background(), "a", "repo", "job-short", "work", 100); err == nil {
		t.Fatal("one-byte-short disk reservation accepted")
	}
	s.stagingMu.Unlock()
}

func TestCapacityReservationReconstructsFromFilesystemAfterRestart(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MinDiskFreeBytes = 0
	path := filepath.Join(s.cfg.DataDir, "incoming", "queued-job")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "package.deb"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: "repo-restart", AccountID: "a", Name: "restart", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateJob(context.Background(), store.Job{ID: "queued-job", RepositoryID: "repo-restart", State: "queued", IncomingPath: path, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	s.reservations = map[string]capacityReservation{"queued-job": {AccountID: "a", JobID: "queued-job", Bytes: 999}}
	s.reservations = nil // process death loses identity; filesystem is authoritative
	physical, err := fileTreeAllocatedSize(filepath.Join(s.cfg.DataDir, "incoming"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxStagingBytes = physical
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkStagingCapacity(context.Background(), "a", block, false); err == nil {
		t.Fatal("surviving incoming allocation did not affect restart admission")
	}
	if err := s.db.Finish(context.Background(), "queued-job", "failed", "test interruption"); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("terminal incoming survived reconciliation: %v", err)
	}
}

func TestPerAccountStagingUsesPhysicalAllocation(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MinDiskFreeBytes = 0
	s.cfg.MaxStagingBytes = 1 << 30
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.db.GetGrant(context.Background(), "a", commerce.ProductTPAHub, CapabilityRepositoryPublish, s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	grant.Limits.StorageBytes = 3 * block
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		return grant, nil
	})
	repo := "physical-account-repo"
	if err = s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: repo, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.DataDir, "incoming", "physical-account-job")
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.deb", "two.deb"} {
		if err = os.WriteFile(filepath.Join(path, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.db.CreateJob(context.Background(), store.Job{ID: "physical-account-job", RepositoryID: repo, State: "queued", IncomingPath: path, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.checkStagingCapacity(context.Background(), "a", block, false); err == nil {
		t.Fatal("per-account physical staging overage was accepted")
	}
}

func TestCapacityReservationsAreAccountScopedAndReleased(t *testing.T) {
	s, _ := testServer(t)
	if err := s.db.CreateAccount(context.Background(), store.Account{ID: "b", Email: "b@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2024-01-01T00:00:00Z"}, defaultTPAGrants("2024-01-01T00:00:00Z")...); err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxStagingBytes = 150
	s.cfg.MinDiskFreeBytes = 0
	s.stagingMu.Lock()
	if err := s.reserveCapacity(context.Background(), "a", "repo-a", "job-a", "incoming", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveCapacity(context.Background(), "b", "repo-b", "job-b", "incoming", 50); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveCapacity(context.Background(), "b", "repo-b", "job-c", "incoming", 1); err == nil {
		t.Fatal("global reservation oversubscription accepted")
	}
	s.releaseCapacity("job-a")
	if err := s.reserveCapacity(context.Background(), "b", "repo-b", "job-c", "incoming", 1); err != nil {
		t.Fatalf("released capacity was not reusable: %v", err)
	}
	if s.reservations["job-c"].AccountID != "b" || s.reservations["job-a"].Bytes != 0 {
		t.Fatal("reservation attribution/release corrupted")
	}
	s.stagingMu.Unlock()
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
	if _, err := s.db.ActiveGeneration(context.Background(), repositoryID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("suspended publication became durable: %v", err)
	}
	if stored, err := s.db.Job(context.Background(), job.ID); err != nil || stored.State != "failed" {
		t.Fatalf("suspended job=%+v err=%v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations", job.ID)); !os.IsNotExist(err) {
		t.Fatalf("suspended generation retained: %v", err)
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
		t.Fatalf("expired grant changed pointer: %v", err)
	}
	if _, err := s.db.ActiveGeneration(context.Background(), repositoryID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired publication became durable: %v", err)
	}
	if stored, err := s.db.Job(context.Background(), job.ID); err != nil || stored.State != "failed" {
		t.Fatalf("expired job=%+v err=%v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations", job.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired generation retained: %v", err)
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

func TestCapacityDenialNeverReclaimsProtectedGenerations(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MaxStagingBytes = 1
	s.cfg.MinDiskFreeBytes = 0
	ctx := context.Background()
	repositoryID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repositoryID, AccountID: "a", Name: "protected", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations")
	for _, generation := range []string{"active", "previous", "protected"} {
		if err := os.MkdirAll(filepath.Join(root, generation), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, generation, "package.deb"), []byte("kept"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("generations", "active"), filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current")); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(ctx, repositoryID, "active", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateJob(ctx, store.Job{ID: "protected", RepositoryID: repositoryID, State: "queued", IncomingPath: "incoming/protected", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	s.stagingMu.Lock()
	err := s.reserveCapacity(ctx, "", repositoryID, "denied", "candidate", 2)
	s.stagingMu.Unlock()
	if err == nil {
		t.Fatal("capacity reservation unexpectedly succeeded")
	}
	for _, generation := range []string{"active", "previous", "protected"} {
		if _, statErr := os.Stat(filepath.Join(root, generation)); statErr != nil {
			t.Fatalf("protected generation %s disappeared: %v", generation, statErr)
		}
	}
	target, err := os.Readlink(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current"))
	if err != nil || target != filepath.Join("generations", "active") {
		t.Fatalf("active pointer changed: %q %v", target, err)
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

func testCandidateSigningConfig(t *testing.T) config.Config {
	t.Helper()
	home := filepath.Join(t.TempDir(), "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	identity := "TPA Hub candidate test <candidate@example.invalid>"
	cmd := exec.Command("gpg", "--batch", "--homedir", home, "--passphrase", "", "--quick-generate-key", identity, "ed25519", "sign", "0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate candidate signing key: %v: %s", err, output)
	}
	output, err := exec.Command("gpg", "--batch", "--homedir", home, "--with-colons", "--fingerprint", "--list-secret-keys", identity).Output()
	if err != nil {
		t.Fatal(err)
	}
	var fingerprint string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			fingerprint = fields[9]
			break
		}
	}
	if fingerprint == "" {
		t.Fatal("candidate signing fingerprint unavailable")
	}
	return config.Config{GPGHome: home, GPGFingerprint: fingerprint}
}

func writeSemanticCandidate(t *testing.T, candidate, work string, cfg config.Config) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(candidate, "pool"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(candidate, "dists", "stable", "main", "binary-amd64"), 0755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	var stanzas string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".deb") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(work, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(candidate, "pool", entry.Name())
		if err = os.WriteFile(dst, data, 0644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		stanzas += fmt.Sprintf("Package: test\nVersion: 1\nArchitecture: amd64\nFilename: pool/%s\nSize: %d\nSHA256: %s\n\n", entry.Name(), len(data), hex.EncodeToString(sum[:]))
	}
	packages := filepath.Join(candidate, "dists", "stable", "main", "binary-amd64", "Packages")
	if err = os.WriteFile(packages, []byte(stanzas), 0644); err != nil {
		t.Fatal(err)
	}
	signSemanticCandidate(t, candidate, cfg)
}

func signSemanticCandidate(t *testing.T, candidate string, cfg config.Config) {
	t.Helper()
	packages := filepath.Join(candidate, "dists", "stable", "main", "binary-amd64", "Packages")
	pd, err := os.ReadFile(packages)
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := os.Create(packages + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(compressed)
	if _, err = gz.Write(pd); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pd)
	gzipData, _ := os.ReadFile(packages + ".gz")
	gzipSum := sha256.Sum256(gzipData)
	release := fmt.Sprintf("Origin: TPA Hub test\nLabel: TPA Hub test\nSuite: stable\nArchitectures: amd64\nComponents: main\nCodename: stable\nSHA256:\n %s %d main/binary-amd64/Packages\n %s %d main/binary-amd64/Packages.gz\n", hex.EncodeToString(sum[:]), len(pd), hex.EncodeToString(gzipSum[:]), len(gzipData))
	releasePath := filepath.Join(candidate, "dists", "stable", "Release")
	if err = os.WriteFile(releasePath, []byte(release), 0644); err != nil {
		t.Fatal(err)
	}
	inRelease := filepath.Join(candidate, "dists", "stable", "InRelease")
	cmd := exec.Command("gpg", "--batch", "--yes", "--homedir", cfg.GPGHome, "--local-user", cfg.GPGFingerprint, "--clearsign", "--output", inRelease, releasePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sign candidate Release: %v: %s", err, output)
	}
}

func omitSemanticPackage(t *testing.T, candidate, name string, cfg config.Config) {
	t.Helper()
	packages := filepath.Join(candidate, "dists", "stable", "main", "binary-amd64", "Packages")
	data, err := os.ReadFile(packages)
	if err != nil {
		t.Fatal(err)
	}
	paragraphs := strings.Split(string(data), "\n\n")
	kept := paragraphs[:0]
	for _, paragraph := range paragraphs {
		if paragraph != "" && !strings.Contains(paragraph, "Filename: pool/"+name+"\n") {
			kept = append(kept, paragraph)
		}
	}
	output := ""
	if len(kept) > 0 {
		output = strings.Join(kept, "\n\n") + "\n\n"
	}
	if err = os.WriteFile(packages, []byte(output), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(candidate, "pool", name)); err != nil {
		t.Fatal(err)
	}
	signSemanticCandidate(t, candidate, cfg)
}

func TestPublicationCandidateSemanticVerification(t *testing.T) {
	cfg := testCandidateSigningConfig(t)
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "package.deb"), []byte("deb"), 0644); err != nil {
		t.Fatal(err)
	}
	makeCandidate := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "candidate")
		writeSemanticCandidate(t, p, work, cfg)
		return p
	}
	valid := makeCandidate(t)
	if err := verifyPublicationCandidate(valid, work, cfg); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	wrongKey := testCandidateSigningConfig(t)
	wrongKeyCandidate := makeCandidate(t)
	if err := verifyPublicationCandidate(wrongKeyCandidate, work, wrongKey); err == nil {
		t.Fatal("candidate signed by an unrelated key was accepted")
	}
	cases := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"garbage", func(t *testing.T, p string) {
			os.RemoveAll(p)
			os.MkdirAll(p, 0755)
			os.WriteFile(filepath.Join(p, "Packages"), []byte("ok"), 0644)
		}},
		{"missing-inrelease", func(t *testing.T, p string) { os.Remove(filepath.Join(p, "dists", "stable", "InRelease")) }},
		{"invalid-signing-envelope", func(t *testing.T, p string) {
			os.WriteFile(filepath.Join(p, "dists", "stable", "InRelease"), []byte("unsigned"), 0644)
		}},
		{"invalid-signature", func(t *testing.T, p string) {
			path := filepath.Join(p, "dists", "stable", "InRelease")
			data, _ := os.ReadFile(path)
			marker := []byte("-----BEGIN PGP SIGNATURE-----\n\n")
			at := bytes.Index(data, marker)
			if at < 0 {
				t.Fatal("signature marker missing")
			}
			data[at+len(marker)] = '!'
			os.WriteFile(path, data, 0644)
		}},
		{"truncated-packages", func(t *testing.T, p string) {
			os.WriteFile(filepath.Join(p, "dists", "stable", "main", "binary-amd64", "Packages"), []byte("Package"), 0644)
		}},
		{"missing-payload", func(t *testing.T, p string) { os.Remove(filepath.Join(p, "pool", "package.deb")) }},
		{"bad-package-checksum", func(t *testing.T, p string) {
			os.WriteFile(filepath.Join(p, "pool", "package.deb"), []byte("changed"), 0644)
		}},
		{"missing-new-package", func(t *testing.T, p string) {
			os.WriteFile(filepath.Join(p, "dists", "stable", "main", "binary-amd64", "Packages"), []byte(""), 0644)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := makeCandidate(t)
			tc.mutate(t, p)
			if err := verifyPublicationCandidate(p, work, cfg); err == nil {
				t.Fatal("malformed candidate accepted")
			}
		})
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
