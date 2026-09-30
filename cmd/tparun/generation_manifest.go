package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxGenerationFiles = 8192
const maxGenerationManifestBytes = 4 << 20
const maxGenerationPathBytes = 4096
const maxGenerationPathDepth = 64
const maxGenerationDirectories = 65536

type generationManifest struct {
	Version          int                      `json:"version"`
	RepositoryID     string                   `json:"repository_id"`
	GenerationID     string                   `json:"generation_id"`
	ParentGeneration string                   `json:"parent_generation"`
	Files            []generationManifestFile `json:"files"`
}

type generationManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func decodeGenerationManifest(data []byte, repositoryID string) (generationManifest, int64, error) {
	var manifest generationManifest
	if len(data) == 0 || len(data) > maxGenerationManifestBytes {
		return manifest, 0, fmt.Errorf("invalid generation manifest size")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, 0, fmt.Errorf("invalid generation manifest: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return manifest, 0, fmt.Errorf("generation manifest has trailing data")
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || string(append(canonical, '\n')) != string(data) {
		return manifest, 0, fmt.Errorf("generation manifest is not in canonical form")
	}
	if manifest.Version != 1 || manifest.RepositoryID != repositoryID || !idRE.MatchString(manifest.GenerationID) || (manifest.ParentGeneration != "" && !idRE.MatchString(manifest.ParentGeneration)) {
		return manifest, 0, fmt.Errorf("generation identity or version is invalid")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxGenerationFiles {
		return manifest, 0, fmt.Errorf("generation file count is outside supported bounds")
	}
	var total int64
	last := ""
	filePaths := make(map[string]struct{}, len(manifest.Files))
	directories := make(map[string]struct{})
	for _, file := range manifest.Files {
		if err := validateGenerationPath(file.Path); err != nil {
			return manifest, 0, err
		}
		if file.Path <= last {
			return manifest, 0, fmt.Errorf("generation paths must be sorted and unique")
		}
		last = file.Path
		if len(file.Path) > maxGenerationPathBytes || strings.Count(file.Path, "/")+1 > maxGenerationPathDepth {
			return manifest, 0, fmt.Errorf("generation path exceeds supported length or depth")
		}
		filePaths[file.Path] = struct{}{}
		for slash := strings.IndexByte(file.Path, '/'); slash >= 0; {
			directories[file.Path[:slash]] = struct{}{}
			if len(directories) > maxGenerationDirectories {
				return manifest, 0, fmt.Errorf("generation directory count exceeds supported limit")
			}
			next := strings.IndexByte(file.Path[slash+1:], '/')
			if next < 0 {
				break
			}
			slash += next + 1
		}
		if file.Size < 0 || len(file.SHA256) != sha256.Size*2 {
			return manifest, 0, fmt.Errorf("invalid generation file metadata: %s", file.Path)
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil || strings.ToLower(file.SHA256) != file.SHA256 {
			return manifest, 0, fmt.Errorf("invalid generation file digest: %s", file.Path)
		}
		if file.Size > int64(^uint64(0)>>1)-total {
			return manifest, 0, fmt.Errorf("generation size overflow")
		}
		total += file.Size
	}
	for directory := range directories {
		if _, isFile := filePaths[directory]; isFile {
			return manifest, 0, fmt.Errorf("generation path is both file and directory: %s", directory)
		}
	}
	return manifest, total, nil
}

func generationDirectoryCount(manifest generationManifest) int {
	directories := make(map[string]struct{})
	for _, file := range manifest.Files {
		for slash := strings.IndexByte(file.Path, '/'); slash >= 0; {
			directories[file.Path[:slash]] = struct{}{}
			next := strings.IndexByte(file.Path[slash+1:], '/')
			if next < 0 {
				break
			}
			slash += next + 1
		}
	}
	return len(directories)
}

func validateGenerationPath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("invalid generation path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("invalid generation path %q", path)
	}
	return nil
}

func verifyGenerationInventory(root string, manifest generationManifest) error {
	return verifyGenerationInventoryContext(context.Background(), root, manifest)
}

func verifyGenerationInventoryContext(ctx context.Context, root string, manifest generationManifest) error {
	declared := make(map[string]generationManifestFile, len(manifest.Files))
	for _, file := range manifest.Files {
		declared[file.Path] = file
	}
	seen := make(map[string]bool, len(declared))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("generation contains symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("generation contains non-regular file")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		want, ok := declared[rel]
		if !ok {
			return fmt.Errorf("generation contains undeclared file %s", rel)
		}
		if seen[rel] {
			return fmt.Errorf("generation contains duplicate file %s", rel)
		}
		if err := verifyCandidateFile(ctx, path, want.Size, want.SHA256); err != nil {
			return fmt.Errorf("generation file %s: %w", rel, err)
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(declared) {
		for path := range declared {
			if !seen[path] {
				return fmt.Errorf("generation is missing file %s", path)
			}
		}
	}
	return nil
}
