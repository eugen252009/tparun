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

func storageTestRepository(t *testing.T, s *server, id string) string {
	t.Helper()
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: id, Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", id)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func candidateWithSize(t *testing.T, s *server, name string, size int) string {
	t.Helper()
	path := filepath.Join(s.cfg.DataDir, name)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "candidate.deb")
	if err := os.WriteFile(file, make([]byte, size), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

type grantServiceFunc func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error)

func (f grantServiceFunc) GetGrant(ctx context.Context, customer commerce.CustomerID, product commerce.ProductID, capability commerce.CapabilityID, at time.Time) (commerce.Grant, error) {
	return f(ctx, customer, product, capability, at)
}

func TestStorageCapacityUsesGrantLimitAndFailsClosed(t *testing.T) {
	s, _ := testServer(t)
	root := storageTestRepository(t, s, "abababababababababababababababab")
	if err := os.WriteFile(filepath.Join(root, "current.deb"), make([]byte, 99), 0644); err != nil {
		t.Fatal(err)
	}
	candidate := candidateWithSize(t, s, "grant-limit", 101)
	now := s.currentTime().UTC()
	if err := s.db.PutGrant(context.Background(), commerce.Grant{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryPublish, OfferID: "custom", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 100}, Source: "local", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var capacityErr StorageCapacityError
	if err := s.checkStorageCapacity("abababababababababababababababab", candidate); !errors.As(err, &capacityErr) || capacityErr.CapacityBytes != 100 {
		t.Fatalf("custom grant limit not enforced: %+v err=%v", capacityErr, err)
	}
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		return commerce.Grant{}, commerce.ErrGrantNotFound
	})
	if err := s.checkStorageCapacity("abababababababababababababababab", candidate); !errors.Is(err, commerce.ErrGrantNotFound) {
		t.Fatalf("missing grant did not fail closed: %v", err)
	}
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		return commerce.Grant{}, commerce.ErrGrantInactive
	})
	if err := s.checkStorageCapacity("abababababababababababababababab", candidate); !errors.Is(err, commerce.ErrGrantInactive) {
		t.Fatalf("expired grant did not fail closed: %v", err)
	}
}

func TestStorageCapacityAllowsExactLimitAndRejectsOneByteOver(t *testing.T) {
	s, _ := testServer(t)
	root := storageTestRepository(t, s, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if err := os.WriteFile(filepath.Join(root, "current.deb"), make([]byte, int(NanoStorageBytes-1)), 0644); err != nil {
		t.Fatal(err)
	}
	candidate := candidateWithSize(t, s, "candidate-exact", int(NanoStorageBytes))
	if err := s.checkStorageCapacity("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", candidate); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	candidate = candidateWithSize(t, s, "candidate-over", int(NanoStorageBytes+1))
	var capacityErr StorageCapacityError
	if err := s.checkStorageCapacity("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", candidate); !errors.As(err, &capacityErr) {
		t.Fatalf("expected capacity error, got %v", err)
	} else if capacityErr.CurrentBytes != NanoStorageBytes-1 || capacityErr.ResultingBytes != NanoStorageBytes+1 || capacityErr.CapacityBytes != NanoStorageBytes {
		t.Fatalf("unexpected capacity error: %+v", capacityErr)
	}
}

func TestStorageCapacityUsesReplacementDeltaAndWholeCandidate(t *testing.T) {
	s, _ := testServer(t)
	root := storageTestRepository(t, s, "ffffffffffffffffffffffffffffffff")
	if err := os.WriteFile(filepath.Join(root, "same.deb"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.deb"), make([]byte, NanoStorageBytes-200), 0644); err != nil {
		t.Fatal(err)
	}
	candidate := candidateWithSize(t, s, "candidate-replacement", 101)
	if err := os.WriteFile(filepath.Join(candidate, "other.deb"), make([]byte, NanoStorageBytes-200), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.checkStorageCapacity("ffffffffffffffffffffffffffffffff", candidate); err != nil {
		t.Fatalf("smaller replacement rejected: %v", err)
	}
	candidate = candidateWithSize(t, s, "candidate-batch", 101)
	if err := os.WriteFile(filepath.Join(candidate, "other.deb"), make([]byte, NanoStorageBytes-200), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "second.deb"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.checkStorageCapacity("ffffffffffffffffffffffffffffffff", candidate); err == nil {
		t.Fatal("batch over capacity accepted")
	}
}

func TestStorageCapacityGrandfathersExistingOverageButBlocksGrowth(t *testing.T) {
	s, _ := testServer(t)
	root := storageTestRepository(t, s, "11111111111111111111111111111111")
	if err := os.WriteFile(filepath.Join(root, "existing.deb"), make([]byte, 5_500_000), 0644); err != nil {
		t.Fatal(err)
	}
	candidate := candidateWithSize(t, s, "candidate-reduce", 5_300_000)
	if err := s.checkStorageCapacity("11111111111111111111111111111111", candidate); err != nil {
		t.Fatalf("storage-reducing replacement rejected: %v", err)
	}
	candidate = candidateWithSize(t, s, "candidate-grow", 5_700_000)
	if err := s.checkStorageCapacity("11111111111111111111111111111111", candidate); err == nil {
		t.Fatal("storage-increasing replacement accepted")
	}
}
