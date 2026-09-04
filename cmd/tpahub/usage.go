package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/eugen252009/tpahub/internal/store"
)

// PublishedUsage describes package payloads in one canonical published repository.
type PublishedUsage struct {
	PackageCount int64
	StoredBytes  int64
}

type AccountUsage struct {
	RepositoryCount int
	PackageCount    int64
	StoredBytes     int64
}

func publishedUsage(root string) (PublishedUsage, error) {
	var usage PublishedUsage
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var overflow bool
		usage.PackageCount, overflow = addCapacity(usage.PackageCount, 1)
		if overflow {
			return fmt.Errorf("published package count overflow")
		}
		usage.StoredBytes, overflow = addCapacity(usage.StoredBytes, info.Size())
		if overflow {
			return fmt.Errorf("published storage usage overflow")
		}
		return nil
	})
	return usage, err
}

func (s *server) repositoryUsage(repositoryID string) (PublishedUsage, error) {
	root, exists, err := s.repositoryUsageRoot(repositoryID)
	if err != nil {
		return PublishedUsage{}, err
	}
	if !exists {
		return PublishedUsage{}, nil
	}
	return publishedUsage(root)
}

func (s *server) repositoryUsageRoot(repositoryID string) (string, bool, error) {
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
	root, err := filepath.Abs(root)
	if err != nil {
		return "", false, fmt.Errorf("normalize repository root: %w", err)
	}
	rootInfo, err := os.Lstat(root)
	if os.IsNotExist(err) {
		if s.db != nil {
			if _, generationErr := s.db.ActiveGeneration(context.Background(), repositoryID); generationErr == nil {
				return "", false, fmt.Errorf("active repository root is missing")
			} else if !errors.Is(generationErr, store.ErrNotFound) {
				return "", false, fmt.Errorf("active generation lookup: %w", generationErr)
			}
		}
		return root, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect repository root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", false, fmt.Errorf("repository root is not a physical directory")
	}
	pointer := filepath.Join(root, "current")
	pointerInfo, err := os.Lstat(pointer)
	if os.IsNotExist(err) {
		// Pre-generation repositories stored package payloads directly in root.
		// Once durable generation authority exists, a missing pointer is broken
		// publication state and must not be interpreted as zero usage.
		if s.db != nil {
			if _, generationErr := s.db.ActiveGeneration(context.Background(), repositoryID); generationErr == nil {
				return "", false, fmt.Errorf("active repository pointer is missing")
			} else if !errors.Is(generationErr, store.ErrNotFound) {
				return "", false, fmt.Errorf("active generation lookup: %w", generationErr)
			}
		}
		return root, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect active repository pointer: %w", err)
	}
	if pointerInfo.Mode()&os.ModeSymlink == 0 {
		return "", false, fmt.Errorf("active repository pointer is not a symlink")
	}
	if s.db == nil {
		return "", false, fmt.Errorf("active generation authority is unavailable")
	}
	activeGeneration, err := s.db.ActiveGeneration(context.Background(), repositoryID)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, fmt.Errorf("active repository pointer has no durable generation")
	}
	if err != nil {
		return "", false, fmt.Errorf("active generation lookup: %w", err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false, fmt.Errorf("resolve repository root: %w", err)
	}
	generationRoot := filepath.Join(resolvedRoot, "generations")
	generationRootInfo, err := os.Lstat(generationRoot)
	if err != nil {
		return "", false, fmt.Errorf("inspect repository generation root: %w", err)
	}
	if generationRootInfo.Mode()&os.ModeSymlink != 0 || !generationRootInfo.IsDir() {
		return "", false, fmt.Errorf("repository generation root is not a physical directory")
	}

	resolved, err := filepath.EvalSymlinks(pointer)
	if err != nil {
		return "", false, fmt.Errorf("resolve active repository pointer: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", false, fmt.Errorf("normalize active repository target: %w", err)
	}
	if filepath.Dir(resolved) != generationRoot {
		return "", false, fmt.Errorf("active repository pointer escapes generation root")
	}
	if filepath.Base(resolved) != activeGeneration {
		return "", false, fmt.Errorf("active repository pointer disagrees with durable generation")
	}
	targetInfo, err := os.Lstat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("inspect active repository target: %w", err)
	}
	if !targetInfo.IsDir() {
		return "", false, fmt.Errorf("active repository target is not a physical directory")
	}
	return resolved, true, nil
}

func (s *server) accountUsage(ctx context.Context, accountID string) (AccountUsage, error) {
	repositories, err := s.db.Repositories(ctx, accountID)
	if err != nil {
		return AccountUsage{}, err
	}
	usage := AccountUsage{RepositoryCount: len(repositories)}
	for _, repository := range repositories {
		stats, err := s.repositoryUsage(repository.ID)
		if err != nil {
			return AccountUsage{}, err
		}
		var overflow bool
		usage.PackageCount, overflow = addCapacity(usage.PackageCount, stats.PackageCount)
		if overflow {
			return AccountUsage{}, fmt.Errorf("account package count overflow")
		}
		usage.StoredBytes, overflow = addCapacity(usage.StoredBytes, stats.StoredBytes)
		if overflow {
			return AccountUsage{}, fmt.Errorf("account storage usage overflow")
		}
	}
	return usage, nil
}

func (s *server) publishedPackageCount() (int, error) {
	stats, err := s.globalPublishedUsage()
	if err != nil {
		return 0, err
	}
	count := int(stats.PackageCount)
	if int64(count) != stats.PackageCount {
		return 0, fmt.Errorf("published package count exceeds platform integer range")
	}
	return count, nil
}

func (s *server) globalPublishedUsage() (PublishedUsage, error) {
	var usage PublishedUsage
	entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "repos"))
	if os.IsNotExist(err) {
		return usage, nil
	}
	if err != nil {
		return usage, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !idRE.MatchString(entry.Name()) {
			continue
		}
		stats, err := s.repositoryUsage(entry.Name())
		if err != nil {
			return PublishedUsage{}, err
		}
		var overflow bool
		usage.PackageCount, overflow = addCapacity(usage.PackageCount, stats.PackageCount)
		if overflow {
			return PublishedUsage{}, fmt.Errorf("global package count overflow")
		}
		usage.StoredBytes, overflow = addCapacity(usage.StoredBytes, stats.StoredBytes)
		if overflow {
			return PublishedUsage{}, fmt.Errorf("global storage usage overflow")
		}
	}
	return usage, nil
}

func formatBytes(bytes int64) string {
	if bytes < 1000 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(bytes)
	for _, unit := range units {
		value /= 1000
		if value < 1000 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}
