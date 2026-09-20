package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestDataDirectoryLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := lockDataDirectory(dir); err == nil {
		second.Close()
		t.Fatal("second Hub process acquired the data directory")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	third.Close()
}

func TestReconcileStagingRemovesUnknownIncomingTrees(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "staging-r4", Email: "staging-r4@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "abababababababababababababababab", AccountID: "staging-r4", Name: "staging", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "incoming", "active")
	orphan := filepath.Join(dir, "incoming", "orphan")
	if err = os.MkdirAll(active, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateJob(ctx, store.Job{ID: "active-job", RepositoryID: "abababababababababababababababab", State: "queued", IncomingPath: active, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config.Config{DataDir: dir}, db: db}
	if err = s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(active); err != nil {
		t.Fatalf("active incoming tree removed: %v", err)
	}
	if _, err = os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan incoming tree remains: %v", err)
	}
}

func TestCopyDebsPreservesConflictingBasenameHistory(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	destination := filepath.Join(root, "work")
	for _, dir := range []string{first, second, destination} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "same.deb"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "same.deb"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyDebs(first, destination, false); err != nil {
		t.Fatal(err)
	}
	if err := copyDebs(second, destination, true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("conflicting basename collapsed history: %v", entries)
	}
}
