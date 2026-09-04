package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func generationCleanupFixture(t *testing.T) (*server, string, string) {
	t.Helper()
	s, _ := testServer(t)
	s.cfg.MaxGenerations = 1
	repo := "generation-cleanup-repo"
	ctx := context.Background()
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repo, AccountID: "a", Name: "generation-cleanup", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	active := filepath.Join(root, "generations", "N")
	if err := os.MkdirAll(active, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "active.deb"), []byte("active"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "N"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(ctx, repo, "N", "now"); err != nil {
		t.Fatal(err)
	}
	return s, repo, root
}

func assertGenerationAuthority(t *testing.T, s *server, repo, root string) {
	t.Helper()
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil || target != filepath.Join("generations", "N") {
		t.Fatalf("pointer=%q err=%v", target, err)
	}
	active, err := s.db.ActiveGeneration(context.Background(), repo)
	if err != nil || active != "N" {
		t.Fatalf("durable active=%q err=%v", active, err)
	}
	if _, err = os.Stat(filepath.Join(root, "generations", "N")); err != nil {
		t.Fatalf("active generation lost: %v", err)
	}
}

func TestNonAuthoritativeGenerationCleanupFailureRetriesSafely(t *testing.T) {
	s, repo, root := generationCleanupFixture(t)
	old := filepath.Join(root, "generations", "M")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	s.publicationFault = func(stage string) error {
		if stage == "cleanup-old-generation" {
			return errors.New("injected old generation removal failure")
		}
		return nil
	}
	if err := s.collectOldGenerations(repo); err == nil {
		t.Fatal("generation cleanup failure hidden")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("failed deletion removed old generation: %v", err)
	}
	assertGenerationAuthority(t, s, repo, root)
	s.publicationFault = nil
	if err := s.collectOldGenerations(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("retry left old generation: %v", err)
	}
	assertGenerationAuthority(t, s, repo, root)
	if err := s.collectOldGenerations(repo); err != nil {
		t.Fatal(err)
	}
}

func TestPartialGenerationCleanupResumesAndPreservesActive(t *testing.T) {
	s, repo, root := generationCleanupFixture(t)
	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range []string{"M1", "M2", "M3"} {
		p := filepath.Join(root, "generations", name)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(3-i) * time.Hour)
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	s.publicationFault = func(stage string) error {
		if stage == "cleanup-old-generation" {
			calls++
			if calls == 2 {
				return errors.New("injected partial generation cleanup")
			}
		}
		return nil
	}
	if err := s.collectOldGenerations(repo); err == nil {
		t.Fatal("partial generation cleanup failure hidden")
	}
	if _, err := os.Stat(filepath.Join(root, "generations", "M1")); !os.IsNotExist(err) {
		t.Fatalf("first cleanup not retained: %v", err)
	}
	for _, name := range []string{"M2", "M3"} {
		if _, err := os.Stat(filepath.Join(root, "generations", name)); err != nil {
			t.Fatalf("%s unexpectedly removed: %v", name, err)
		}
	}
	assertGenerationAuthority(t, s, repo, root)
	s.publicationFault = nil
	if err := s.collectOldGenerations(repo); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"M1", "M2", "M3"} {
		if _, err := os.Stat(filepath.Join(root, "generations", name)); !os.IsNotExist(err) {
			t.Fatalf("retry left %s: %v", name, err)
		}
	}
	assertGenerationAuthority(t, s, repo, root)
}

func TestFailedGenerationCleanupCannotBypassDiskAdmission(t *testing.T) {
	s, repo, root := generationCleanupFixture(t)
	old := filepath.Join(root, "generations", "M")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "old.deb"), make([]byte, 100), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.MinDiskFreeBytes = 10
	s.diskFree = func() (int64, error) {
		if _, err := os.Stat(old); err == nil {
			return 10, nil
		}
		return 100, nil
	}
	s.publicationFault = func(stage string) error {
		if stage == "cleanup-old-generation" {
			return errors.New("injected cleanup failure")
		}
		return nil
	}
	if err := s.collectOldGenerations(repo); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	s.stagingMu.Lock()
	err := s.reserveCapacity(context.Background(), "", repo, "capacity-before-cleanup", "candidate", 1)
	s.stagingMu.Unlock()
	if err == nil {
		t.Fatal("surviving garbage bypassed disk admission")
	}
	s.publicationFault = nil
	if err = s.collectOldGenerations(repo); err != nil {
		t.Fatal(err)
	}
	s.stagingMu.Lock()
	err = s.reserveCapacity(context.Background(), "", repo, "capacity-after-cleanup", "candidate", 1)
	s.stagingMu.Unlock()
	if err != nil {
		t.Fatalf("capacity did not recover after cleanup: %v", err)
	}
	s.releaseCapacity("capacity-after-cleanup")
	assertGenerationAuthority(t, s, repo, root)
}
