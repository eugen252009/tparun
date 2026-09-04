package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestStartupCleanupMatrixIsIdempotentAndProtectsAuthoritativeState(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "cleanup", Email: "cleanup@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2027-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "cleanup-repo", AccountID: "cleanup", Name: "cleanup", CreatedAt: "2027-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(dir, "incoming", "terminal")
	work := filepath.Join(dir, "work", "abandoned")
	candidate := filepath.Join(dir, "work", "candidate-published")
	for _, p := range []string{incoming, work, candidate} {
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(p, "payload"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.CreateJob(ctx, store.Job{ID: "terminal", RepositoryID: "cleanup-repo", State: "failed", IncomingPath: incoming, CreatedAt: "2027-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "repos", "cleanup-repo")
	generation := filepath.Join(root, "generations", "authoritative")
	if err = os.MkdirAll(generation, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(generation, "Packages"), []byte("authoritative"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = db.SetActiveGeneration(ctx, "cleanup-repo", "authoritative", "2027-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join("generations", "authoritative"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("generations/authoritative", filepath.Join(root, "current.reconcile")); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config.Config{DataDir: dir}, db: db}
	if err = s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(incoming); !os.IsNotExist(err) {
		t.Fatalf("terminal incoming remains: %v", err)
	}
	if _, err = os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("abandoned work remains: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "current.reconcile")); !os.IsNotExist(err) {
		t.Fatalf("temporary pointer remains: %v", err)
	}
	if _, err = os.Stat(generation); err != nil {
		t.Fatalf("authoritative generation removed: %v", err)
	}
	if err = s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
}
