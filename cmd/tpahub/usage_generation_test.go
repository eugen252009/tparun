package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

func generationUsageFixture(t *testing.T, s *server, repositoryID string, generations map[string][]int, active string) string {
	t.Helper()
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: repositoryID, Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
	for generation, sizes := range generations {
		path := filepath.Join(root, "generations", generation)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		for i, size := range sizes {
			name := string(rune('a'+i)) + ".deb"
			if err := os.WriteFile(filepath.Join(path, name), make([]byte, size), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join("generations", active), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(context.Background(), repositoryID, active, "now"); err != nil {
		t.Fatal(err)
	}
	return root
}

func replaceCurrentGeneration(t *testing.T, root, generation string) {
	t.Helper()
	next := filepath.Join(root, "current.test")
	if err := os.Symlink(filepath.Join("generations", generation), next); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceSymlink(next, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
}

func setCurrentGeneration(t *testing.T, s *server, repositoryID, root, generation string) {
	t.Helper()
	if err := s.db.SetActiveGeneration(context.Background(), repositoryID, generation, "now"); err != nil {
		t.Fatal(err)
	}
	replaceCurrentGeneration(t, root, generation)
}

func TestRepositoryUsageCountsActiveGenerationExactly(t *testing.T) {
	s, _ := testServer(t)
	generationUsageFixture(t, s, "ababababababababababababababab01", map[string][]int{"gen-1": {17, 29}}, "gen-1")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.DataDir, err = filepath.Rel(cwd, s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}

	usage, err := s.repositoryUsage("ababababababababababababababab01")
	if err != nil || usage.PackageCount != 2 || usage.StoredBytes != 46 {
		t.Fatalf("generation usage=%+v err=%v", usage, err)
	}
}

func TestRepositoryUsageTracksOnlyCurrentGenerationAndPointerSwaps(t *testing.T) {
	s, _ := testServer(t)
	root := generationUsageFixture(t, s, "ababababababababababababababab02", map[string][]int{
		"gen-1": {11},
		"gen-2": {23, 31},
		"gen-3": {47},
	}, "gen-1")

	usage, err := s.repositoryUsage("ababababababababababababababab02")
	if err != nil || usage.PackageCount != 1 || usage.StoredBytes != 11 {
		t.Fatalf("gen-1 usage=%+v err=%v", usage, err)
	}
	setCurrentGeneration(t, s, "ababababababababababababababab02", root, "gen-2")
	usage, err = s.repositoryUsage("ababababababababababababababab02")
	if err != nil || usage.PackageCount != 2 || usage.StoredBytes != 54 {
		t.Fatalf("gen-2 usage=%+v err=%v", usage, err)
	}
}

func TestRepositoryUsageCurrentPointerFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, *server, string)
	}{
		{name: "broken", setup: func(t *testing.T, s *server, root string) {
			if err := os.MkdirAll(filepath.Join(root, "generations"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("generations", "missing"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "missing", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "outside", setup: func(t *testing.T, s *server, root string) {
			outside := filepath.Join(s.cfg.DataDir, "outside")
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "escaped.deb"), []byte("escaped"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "generations"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("..", "..", "outside"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "outside", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "file", setup: func(t *testing.T, s *server, root string) {
			generation := filepath.Join(root, "generations", "not-a-directory")
			if err := os.MkdirAll(filepath.Dir(generation), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(generation, []byte("file"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("generations", "not-a-directory"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "not-a-directory", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "physical-current", setup: func(t *testing.T, s *server, root string) {
			if err := os.MkdirAll(filepath.Join(root, "current"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "current", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing-with-durable-authority", setup: func(t *testing.T, s *server, root string) {
			if err := os.MkdirAll(filepath.Join(root, "generations", "durable"), 0755); err != nil {
				t.Fatal(err)
			}
			repositoryID := filepath.Base(root)
			if err := s.db.SetActiveGeneration(context.Background(), repositoryID, "durable", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "generation-root-symlink", setup: func(t *testing.T, s *server, root string) {
			outside := filepath.Join(s.cfg.DataDir, "outside-generations", "gen-1")
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "generations")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("generations", "gen-1"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "gen-1", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "durable-mismatch", setup: func(t *testing.T, s *server, root string) {
			for _, generation := range []string{"durable", "pointer"} {
				if err := os.MkdirAll(filepath.Join(root, "generations", generation), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join("generations", "pointer"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "durable", "now"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "repository-root-symlink", setup: func(t *testing.T, s *server, root string) {
			outside := filepath.Join(s.cfg.DataDir, "symlinked-repository")
			if err := os.MkdirAll(filepath.Join(outside, "generations", "gen-1"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("generations", "gen-1"), filepath.Join(outside, "current")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(root); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, root); err != nil {
				t.Fatal(err)
			}
			if err := s.db.SetActiveGeneration(context.Background(), filepath.Base(root), "gen-1", "now"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testServer(t)
			repositoryID := "ababababababababababababababab1" + string(rune('0'+i))
			if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: repositoryID, CreatedAt: "now"}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, s, root)
			if usage, err := s.repositoryUsage(repositoryID); err == nil {
				t.Fatalf("unsafe current returned usage=%+v", usage)
			}
		})
	}
}

func TestRepositoryUsagePreservesLegacyRootAndDoesNotFollowNestedSymlinks(t *testing.T) {
	s, _ := testServer(t)
	legacy := storageTestRepository(t, s, "ababababababababababababababab20")
	if err := os.WriteFile(filepath.Join(legacy, "legacy.deb"), make([]byte, 13), 0644); err != nil {
		t.Fatal(err)
	}
	usage, err := s.repositoryUsage("ababababababababababababababab20")
	if err != nil || usage.PackageCount != 1 || usage.StoredBytes != 13 {
		t.Fatalf("legacy usage=%+v err=%v", usage, err)
	}

	root := generationUsageFixture(t, s, "ababababababababababababababab21", map[string][]int{"gen-1": {19}}, "gen-1")
	outside := filepath.Join(s.cfg.DataDir, "nested-outside.deb")
	if err := os.WriteFile(outside, make([]byte, 101), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "generations", "gen-1", "nested.deb")); err != nil {
		t.Fatal(err)
	}
	usage, err = s.repositoryUsage("ababababababababababababababab21")
	if err != nil || usage.PackageCount != 1 || usage.StoredBytes != 19 {
		t.Fatalf("nested symlink usage=%+v err=%v", usage, err)
	}
}

func setStorageLimit(t *testing.T, s *server, bytes int64) {
	t.Helper()
	now := s.currentTime().UTC()
	if err := s.db.PutGrant(context.Background(), commerce.Grant{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryPublish, OfferID: "r8", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: bytes}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationBackedStorageQuotaBoundaries(t *testing.T) {
	s, _ := testServer(t)
	repositoryID := "ababababababababababababababab30"
	generationUsageFixture(t, s, repositoryID, map[string][]int{"gen-1": {100}}, "gen-1")

	setStorageLimit(t, s, 100)
	if err := s.checkStagingCapacity(context.Background(), "a", 0, false); err != nil {
		t.Fatalf("exact existing limit rejected: %v", err)
	}
	setStorageLimit(t, s, 99)
	if err := s.checkStagingCapacity(context.Background(), "a", 0, false); err == nil {
		t.Fatal("existing generation over limit was accepted")
	}

	candidate := candidateWithSize(t, s, "r8-growth", 107)
	setStorageLimit(t, s, 107)
	if err := s.checkStorageCapacity(repositoryID, candidate); err != nil {
		t.Fatalf("exact publication growth boundary rejected: %v", err)
	}
	setStorageLimit(t, s, 106)
	var capacityErr StorageCapacityError
	if err := s.checkStorageCapacity(repositoryID, candidate); !errors.As(err, &capacityErr) {
		t.Fatalf("publication one byte over limit was accepted: %v", err)
	}
}

func TestGenerationBackedLargeBaselineAndTinyUpload(t *testing.T) {
	s, _ := testServer(t)
	generationUsageFixture(t, s, "ababababababababababababababab31", map[string][]int{"gen-1": {1_000_000}}, "gen-1")
	setStorageLimit(t, s, 1_000_001)
	if err := s.checkStagingCapacity(context.Background(), "a", 1, false); err != nil {
		t.Fatalf("exact tiny upload boundary rejected: %v", err)
	}
	setStorageLimit(t, s, 1_000_000)
	if err := s.checkStagingCapacity(context.Background(), "a", 1, false); err == nil {
		t.Fatal("tiny upload ignored large active baseline")
	}
}

func processTestPackage(t *testing.T, s *server, repositoryID, jobID, filename string, payload []byte) store.Job {
	t.Helper()
	incoming := filepath.Join(s.cfg.DataDir, "incoming", jobID)
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, filename), payload, 0600); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: jobID, RepositoryID: repositoryID, State: "running", IncomingPath: incoming, CreatedAt: jobID}
	if err := s.db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	s.process(job)
	stored, err := s.db.Job(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestGenerationBackedPublicationUsesExistingStorageBaseline(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	s.runner = copyingPublicationRunner{}
	repositoryID := "88888888888888888888888888888888"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "r8-production", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.DataDir, "repos", repositoryID), 0755); err != nil {
		t.Fatal(err)
	}

	if job := processTestPackage(t, s, repositoryID, "r8-first", "package-a.deb", make([]byte, 41)); job.State != "succeeded" {
		t.Fatalf("first publication=%+v", job)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
	target, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != "r8-first" {
		t.Fatalf("first current target=%q err=%v", target, err)
	}
	usage, err := s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != 1 || usage.StoredBytes != 41 {
		t.Fatalf("first published usage=%+v err=%v", usage, err)
	}

	setStorageLimit(t, s, 48)
	if job := processTestPackage(t, s, repositoryID, "r8-exact", "package-b.deb", make([]byte, 7)); job.State != "succeeded" {
		t.Fatalf("exact-boundary publication=%+v", job)
	}
	usage, err = s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != 2 || usage.StoredBytes != 48 {
		t.Fatalf("exact-boundary usage=%+v err=%v", usage, err)
	}

	setStorageLimit(t, s, 52)
	if job := processTestPackage(t, s, repositoryID, "r8-over", "package-c.deb", make([]byte, 5)); job.State != "failed" {
		t.Fatalf("over-limit publication=%+v", job)
	}
	target, err = filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != "r8-exact" {
		t.Fatalf("over-limit publication changed authority=%q err=%v", target, err)
	}
	usage, err = s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != 2 || usage.StoredBytes != 48 {
		t.Fatalf("usage changed after rejection=%+v err=%v", usage, err)
	}
}

func TestRepositoryUsageFollowsRollbackAndStartupReconciliation(t *testing.T) {
	s, repositoryID, root, job := workerFailureFixture(t)
	s.durablePublicationFault = func() error { return errors.New("injected durable failure") }
	s.process(job)
	usage, err := s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != 1 || usage.StoredBytes != 3 {
		t.Fatalf("rollback usage=%+v err=%v", usage, err)
	}

	other := filepath.Join(root, "generations", "other")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "other.deb"), make([]byte, 37), 0644); err != nil {
		t.Fatal(err)
	}
	replaceCurrentGeneration(t, root, "other")
	if _, err := s.repositoryUsage(repositoryID); err == nil {
		t.Fatal("pointer/DB disagreement was accepted during reconciliation window")
	}
	if err := s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	usage, err = s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != 1 || usage.StoredBytes != 3 {
		t.Fatalf("reconciled usage=%+v err=%v", usage, err)
	}
}

func TestGenerationBackedSameBasenameHistoryUsageIsIdempotent(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	s.runner = copyingPublicationRunner{}
	repositoryID := "99999999999999999999999999999999"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "r8-history", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.DataDir, "repos", repositoryID), 0755); err != nil {
		t.Fatal(err)
	}
	for i, payload := range [][]byte{[]byte("v1"), []byte("v2"), []byte("v3"), []byte("v2")} {
		jobID := "r8-history-" + string(rune('1'+i))
		if job := processTestPackage(t, s, repositoryID, jobID, "tool.deb", payload); job.State != "succeeded" {
			t.Fatalf("history publication %d=%+v", i, job)
		}
	}
	usage, err := s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != 3 || usage.StoredBytes != 6 {
		t.Fatalf("same-basename history usage=%+v err=%v", usage, err)
	}
}

func TestPublishedUsageMissingRootFailsClosed(t *testing.T) {
	if usage, err := publishedUsage(filepath.Join(t.TempDir(), "vanished")); err == nil {
		t.Fatalf("missing usage root returned %+v", usage)
	}
}
