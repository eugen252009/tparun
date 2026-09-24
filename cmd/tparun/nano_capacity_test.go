package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/tpa"
)

func TestNanoSharedFinalPayloadAndUpgrade(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	s.runner = copyingPublicationRunner{}
	for _, fixture := range []struct {
		repo string
		size int
	}{{"a", 2_000_000}, {"b", 2_000_000}, {"c", 1_000_000}} {
		storageTestRepository(t, s, fixture.repo)
		if job := processTestPackage(t, s, fixture.repo, "job-"+fixture.repo, "package.deb", make([]byte, fixture.size)); job.State != "succeeded" {
			t.Fatalf("shared exact boundary: %+v", job)
		}
	}
	usage, err := s.accountUsage(context.Background(), "a")
	if err != nil || usage.StoredBytes != NanoStorageBytes {
		t.Fatalf("shared payload=%+v %v", usage, err)
	}
	storageTestRepository(t, s, "d")
	if job := processTestPackage(t, s, "d", "over", "extra.deb", []byte("x")); job.State != "failed" || !strings.Contains(job.Error, "storage-capacity-exceeded") {
		t.Fatalf("one extra byte across repositories: %+v", job)
	}
	// Persist a larger entitlement without touching any repository identity,
	// active pointer, URL component, or storage-layout record.
	repos, err := s.db.Repositories(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.db.GetGrant(context.Background(), "a", TPARunProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	grant.Limits.StorageBytes++
	if err := s.db.PutGrant(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if job := processTestPackage(t, s, "d", "upgraded", "extra.deb", []byte("x")); job.State != "succeeded" {
		t.Fatalf("upgraded publication: %+v", job)
	}
	for _, before := range repos {
		after, err := s.db.Repository(context.Background(), before.ID)
		if err != nil || before != after {
			t.Fatalf("upgrade changed repository: before=%+v after=%+v err=%v", before, after, err)
		}
	}
}

func TestNanoQueuedInputUsesOnlyInfrastructureCapacity(t *testing.T) {
	for _, limit := range []string{"account", "global"} {
		t.Run(limit, func(t *testing.T) {
			s, h := admissionFixture(t, NanoStorageBytes)
			for i := 0; i < 2; i++ {
				if code, err := admissionHTTP(h, "raw", 3_000_000); err != nil || code != 202 {
					t.Fatalf("queued input charged to product quota: %d %v", code, err)
				}
			}
			if jobs, bytes := admissionState(t, s); jobs != 2 || bytes != 6_000_000 {
				t.Fatalf("queued files=%d/%d", jobs, bytes)
			}
			candidate := candidateWithSize(t, s, "fits", int(NanoStorageBytes))
			if err := s.checkStorageCapacity(admissionRepo, candidate); err != nil {
				t.Fatalf("queued files charged to candidate entitlement: %v", err)
			}
			s.stagingMu.Lock()
			snapshot, err := s.physicalCapacity(context.Background(), "a")
			if err != nil {
				t.Fatal(err)
			}
			if limit == "account" {
				s.cfg.MaxAccountStagingBytes = snapshot.accountCommitted
			} else {
				s.cfg.MaxStagingBytes = snapshot.committed
			}
			s.stagingMu.Unlock()
			if code, err := admissionHTTP(h, "raw", 1); code != 507 || err == nil || !strings.Contains(err.Error(), "temporary publication capacity unavailable") {
				t.Fatalf("physical limit not enforced: %d %v", code, err)
			}
			if jobs, bytes := admissionState(t, s); jobs != 2 || bytes != 6_000_000 {
				t.Fatalf("denied upload changed queued input=%d/%d", jobs, bytes)
			}
		})
	}
}

// A pathological fixture, not a measurement of real APT metadata. It exercises
// exactly the worker's production guard before any candidate can be promoted.
type metadataPublicationRunner struct {
	copyingPublicationRunner
	metadata int
}

func (r metadataPublicationRunner) PublishBounded(ctx context.Context, request tpa.PublishRequest, limit int64) error {
	if err := r.copyingPublicationRunner.PublishBounded(ctx, request, limit); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(request.Output, "Packages"), make([]byte, r.metadata), 0644)
}

func TestNanoActiveRepositoryHeadroomIsSharedAndSeparate(t *testing.T) {
	s, _ := testServer(t)
	s.ctx = context.Background()
	repo := "headroom"
	storageTestRepository(t, s, repo)
	s.runner = metadataPublicationRunner{metadata: int(activeRepositoryHeadroomBytes)}
	job := processTestPackage(t, s, repo, "fits", "package.deb", make([]byte, int(NanoStorageBytes)))
	if job.State != "succeeded" {
		t.Fatalf("exact active service boundary: %+v", job)
	}
	s.runner = metadataPublicationRunner{metadata: int(activeRepositoryHeadroomBytes + 1)}
	job = processTestPackage(t, s, repo, "over", "package.deb", make([]byte, int(NanoStorageBytes)))
	if job.State != "failed" || !strings.Contains(job.Error, "repository generation exceeds service limits") || strings.Contains(job.Error, "storage-capacity-exceeded") {
		t.Fatalf("metadata error confused with customer payload: %+v", job)
	}
	if active, err := s.db.ActiveGeneration(context.Background(), repo); err != nil || active != "fits" {
		t.Fatalf("rejected overhead activated: %s %v", active, err)
	}
	usage, err := s.accountUsage(context.Background(), "a")
	if err != nil || usage.StoredBytes != NanoStorageBytes {
		t.Fatalf("metadata charged as customer payload: %+v %v", usage, err)
	}
	// A different empty repository must not obtain another 1 MB of headroom.
	storageTestRepository(t, s, "second")
	candidate := candidateWithSize(t, s, "metadata-only", 0)
	if err := os.WriteFile(filepath.Join(candidate, "Release"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	var overhead RepositoryCapacityError
	if err := s.checkStorageCapacity("second", candidate); !errors.As(err, &overhead) || overhead.BudgetBytes != 6_000_000 {
		t.Fatalf("per-repository overhead allowance introduced: %v", err)
	}
	// Creating empty repositories neither consumes headroom nor requires a
	// count entitlement, even at the full active-file budget.
	for i := 0; i < 20; i++ {
		w := provisioningRequest(s, "POST", "/v1/repositories", `{"name":"empty-`+strings.Repeat("x", i+1)+`"}`, "tph_s_test")
		if w.Code != 200 {
			t.Fatalf("empty repository rejected: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestNanoFootprintGrandfathersNonGrowingCorrections(t *testing.T) {
	s, _ := testServer(t)
	root := storageTestRepository(t, s, "legacy-overhead")
	if err := os.WriteFile(filepath.Join(root, "Packages"), make([]byte, 7_000_000), 0644); err != nil {
		t.Fatal(err)
	}
	candidate := candidateWithSize(t, s, "corrected", 0)
	for _, size := range []int{7_000_000, 6_500_000, 7_000_001} {
		if err := os.WriteFile(filepath.Join(candidate, "Packages"), make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
		err := s.checkStorageCapacity("legacy-overhead", candidate)
		if (err != nil) != (size > 7_000_000) {
			t.Fatalf("existing over-budget correction %d: %v", size, err)
		}
	}
}
