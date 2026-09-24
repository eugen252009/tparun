package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eugen252009/tpahub/internal/store"
)

func uploadedPublicationArtifacts(ctx context.Context, dir string) ([]store.PublicationArtifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]store.PublicationArtifact, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		identity, err := debIdentity(ctx, path)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(identity, "\x00")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid uploaded package identity")
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		out = append(out, store.PublicationArtifact{Filename: entry.Name(), Package: parts[0], Version: parts[1], Architecture: parts[2], UploadedSize: info.Size(), UploadedSHA256: digest})
	}
	return out, nil
}

// removeReplacedWorkPackages implements the planner's existing identity-based
// replacement contract. Only the disposable baseline copy is changed; versions
// with different identities and all authoritative/historical files are preserved.
func (s *server) removeReplacedWorkPackages(ctx context.Context, work, jobID string) error {
	incoming, err := s.db.PublicationArtifacts(ctx, jobID)
	if err != nil || len(incoming) == 0 {
		return err // legacy/malformed input still goes through normal validation
	}
	identities := make(map[string]bool, len(incoming))
	for _, artifact := range incoming {
		identities[strings.Join([]string{artifact.Package, artifact.Version, artifact.Architecture}, "\x00")] = true
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			continue
		}
		path := filepath.Join(work, entry.Name())
		identity, err := debIdentity(ctx, path)
		if err != nil {
			return err
		}
		if identities[identity] {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *server) verifyPublicationArtifactIdentity(ctx context.Context, root, jobID string) ([]store.PublicationArtifact, error) {
	expected, err := s.db.PublicationArtifacts(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if len(expected) == 0 {
		return nil, nil
	} // malformed legacy uploads fail through normal candidate validation
	if _, err := os.Stat(filepath.Join(root, "pool")); os.IsNotExist(err) {
		for i := range expected {
			expected[i].PublishedFilename = filepath.ToSlash(filepath.Join("pool", expected[i].Filename))
			expected[i].PublishedSize = expected[i].UploadedSize
			expected[i].PublishedSHA256 = expected[i].UploadedSHA256
		}
		return expected, nil
	}
	matched := make([]bool, len(expected))
	err = filepath.WalkDir(filepath.Join(root, "pool"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return err
		}
		for i, artifact := range expected {
			if !matched[i] && artifact.UploadedSize == info.Size() && artifact.UploadedSHA256 == digest {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				expected[i].PublishedFilename = filepath.ToSlash(relative)
				expected[i].PublishedSize = info.Size()
				expected[i].PublishedSHA256 = digest
				matched[i] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, ok := range matched {
		if !ok {
			return nil, fmt.Errorf("uploaded artifact missing from published Packages: %s identity=%s sha=%s size=%d", expected[i].Filename, strings.Join([]string{expected[i].Package, expected[i].Version, expected[i].Architecture}, "\\x00"), expected[i].UploadedSHA256, expected[i].UploadedSize)
		}
	}
	return expected, nil
}
