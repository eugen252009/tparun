package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestCleanupFailureIsFailClosedAndRetryable(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "cleanup-fault", Email: "cleanup-fault@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "cleanup-fault-repo", AccountID: "cleanup-fault", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(dir, "incoming", "terminal")
	if err = os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(incoming, "payload"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateJob(ctx, store.Job{ID: "cleanup-fault-job", RepositoryID: "cleanup-fault-repo", State: "failed", IncomingPath: incoming, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config.Config{DataDir: dir}, db: db, publicationFault: func(stage string) error {
		if stage == "cleanup-terminal-incoming" {
			return errors.New("injected cleanup failure")
		}
		return nil
	}}
	if err = s.reconcileStagingState(); err == nil {
		t.Fatal("correctness-threatening cleanup failure continued")
	}
	if _, err = os.Stat(incoming); err != nil {
		t.Fatalf("failed cleanup changed state: %v", err)
	}
	s.publicationFault = nil
	if err = s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(incoming); !os.IsNotExist(err) {
		t.Fatalf("retry left incoming: %v", err)
	}
	db.Close()
}

func TestPartialCleanupResumesAfterRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	work := filepath.Join(dir, "work")
	for _, name := range []string{"a-first", "b-second"} {
		p := filepath.Join(work, name)
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(p, "x"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	s := &server{cfg: config.Config{DataDir: dir}, db: db, publicationFault: func(stage string) error {
		if stage == "cleanup-abandoned-work" {
			calls++
			if calls == 2 {
				return errors.New("stop after first cleanup")
			}
		}
		return nil
	}}
	if err = s.reconcileStagingState(); err == nil {
		t.Fatal("partial cleanup failure was hidden")
	}
	if _, err = os.Stat(filepath.Join(work, "a-first")); !os.IsNotExist(err) {
		t.Fatalf("first cleanup not durable: %v", err)
	}
	if _, err = os.Stat(filepath.Join(work, "b-second")); err != nil {
		t.Fatalf("remaining cleanup disappeared: %v", err)
	}
	s.publicationFault = nil
	if err = s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(work)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("restart retry left work entries: %v", entries)
	}
}

func TestStartupCleanupFailureIsDeferredToLiveRetry(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "startup-retry", Email: "startup-retry@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "startup-retry-repo", AccountID: "startup-retry", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(dir, "incoming", "startup-retry")
	if err = os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateJob(ctx, store.Job{ID: "startup-retry-job", RepositoryID: "startup-retry-repo", State: "running", IncomingPath: incoming, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(ctx, "startup-retry-job", "failed", "startup cleanup test"); err != nil {
		t.Fatal(err)
	}
	removeErr := true
	s := &server{cfg: config.Config{DataDir: dir}, db: db, removeAll: func(path string) error {
		if removeErr {
			return errors.New("temporary cleanup failure")
		}
		return os.RemoveAll(path)
	}}
	if err = s.reconcileStagingState(); err != nil {
		t.Fatalf("startup cleanup failure stopped service: %v", err)
	}
	job, err := db.Job(ctx, "startup-retry-job")
	if err != nil || job.CleanupState != "pending" {
		t.Fatalf("cleanup retry was not persisted: %+v err=%v", job, err)
	}
	cleanupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ctx = cleanupCtx
	s.cleanupWake = make(chan struct{}, 1)
	go s.cleanupRetryLoop()
	removeErr = false
	s.signalCleanupRetry()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, err = db.Job(ctx, "startup-retry-job")
		if err != nil {
			t.Fatal(err)
		}
		if job.CleanupState == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live cleanup retry did not complete: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTemporaryPointerCleanupFailureIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "pointer-fault", Email: "pointer-fault@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "pointer-fault-repo", AccountID: "pointer-fault", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "repos", "pointer-fault-repo")
	gen := filepath.Join(root, "generations", "g1")
	if err = os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(gen, "Packages"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = db.SetActiveGeneration(ctx, "pointer-fault-repo", "g1", "now"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("generations/g1", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("generations/g1", filepath.Join(root, "current.reconcile")); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config.Config{DataDir: dir}, db: db, publicationFault: func(stage string) error {
		if stage == "cleanup-temporary-pointer" {
			return errors.New("injected pointer failure")
		}
		return nil
	}}
	if err = s.reconcilePublicationState(); err == nil {
		t.Fatal("pointer cleanup failure continued")
	}
	if _, err = os.Stat(gen); err != nil {
		t.Fatal(err)
	}
	s.publicationFault = nil
	if err = s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
}
