package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Service-owned headroom, shared across ALL active repositories of an account.
// This is not a grant, a per-repository allowance, or customer package capacity.
const activeRepositoryHeadroomBytes int64 = 1_000_000

type RepositoryCapacityError struct {
	CurrentBytes, ResultingBytes, BudgetBytes int64
}

func (e RepositoryCapacityError) Error() string {
	return fmt.Sprintf("repository generation exceeds service limits: account active files would use %d bytes (service budget %d bytes); package entitlement is separate", e.ResultingBytes, e.BudgetBytes)
}

// activeRepositoryFileBytes measures logical regular-file bytes, including APT
// metadata. It does not measure allocated blocks, directories, database records,
// staging, or historical generations. Physical disk/inode guards remain separate.
func activeRepositoryFileBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("repository contains non-regular file: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var overflow bool
		total, overflow = addCapacity(total, info.Size())
		if overflow {
			return fmt.Errorf("active repository size overflow")
		}
		return nil
	})
	return total, err
}

// Called under stagingMu by the worker, after the customer payload check and
// before activation. Empty repository directories consume no active-file budget.
// Existing over-budget state may shrink or remain unchanged, never grow.
func (s *server) checkActiveRepositoryCapacity(accountID, repositoryID, candidate string, payloadCapacity int64) error {
	repositories, err := s.db.Repositories(context.Background(), accountID)
	if err != nil {
		return fmt.Errorf("active repository usage unavailable: %w", err)
	}
	var current, target int64
	for _, repo := range repositories {
		root, exists, err := s.repositoryUsageRoot(repo.ID)
		if err != nil {
			return fmt.Errorf("active repository usage unavailable: %w", err)
		}
		if !exists {
			continue
		}
		n, err := activeRepositoryFileBytes(root)
		if err != nil {
			return fmt.Errorf("active repository usage unavailable: %w", err)
		}
		var overflow bool
		current, overflow = addCapacity(current, n)
		if overflow {
			return fmt.Errorf("account active repository size overflow")
		}
		if repo.ID == repositoryID {
			target = n
		}
	}
	prospective, err := activeRepositoryFileBytes(candidate)
	if err != nil {
		return fmt.Errorf("candidate repository usage unavailable: %w", err)
	}
	resulting, overflow := addCapacity(current-target, prospective)
	if overflow {
		return fmt.Errorf("resulting active repository size overflow")
	}
	budget, overflow := addCapacity(payloadCapacity, activeRepositoryHeadroomBytes)
	if overflow {
		budget = math.MaxInt64
	}
	if resulting > budget && resulting > current {
		return RepositoryCapacityError{CurrentBytes: current, ResultingBytes: resulting, BudgetBytes: budget}
	}
	return nil
}
