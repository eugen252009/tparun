package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
		usage.PackageCount++
		usage.StoredBytes += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return PublishedUsage{}, nil
	}
	return usage, err
}

func (s *server) repositoryUsage(repositoryID string) (PublishedUsage, error) {
	return publishedUsage(filepath.Join(s.cfg.DataDir, "repos", repositoryID))
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
		usage.PackageCount += stats.PackageCount
		usage.StoredBytes += stats.StoredBytes
	}
	return usage, nil
}

func (s *server) publishedPackageCount() (int, error) {
	stats, err := s.globalPublishedUsage()
	return int(stats.PackageCount), err
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
		usage.PackageCount += stats.PackageCount
		usage.StoredBytes += stats.StoredBytes
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
