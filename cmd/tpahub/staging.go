package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const defaultStagingRoot = "/tmp/tpahub/staging"

type stagedArtifact struct {
	OriginalPath string `json:"original_path"`
	StagedPath   string `json:"staged_path"`
	Filename     string `json:"filename"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	StagedAt     string `json:"staged_at"`
}

type stagingManifest struct {
	Version   int              `json:"version"`
	Artifacts []stagedArtifact `json:"artifacts"`
}

func stagingRoot() string {
	if value := strings.TrimSpace(os.Getenv("TPAHUB_STAGING_DIR")); value != "" {
		return value
	}
	return defaultStagingRoot
}
func stagingArtifactsRoot() string { return filepath.Join(stagingRoot(), "artifacts") }
func stagingMetadataPath() string  { return filepath.Join(stagingRoot(), "metadata.json") }
func stagingLockPath() string      { return filepath.Join(stagingRoot(), ".lock") }

func withStagingLock(fn func() error) error {
	if err := os.MkdirAll(stagingArtifactsRoot(), 0700); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	if err := os.Chmod(stagingRoot(), 0700); err != nil {
		return fmt.Errorf("restrict staging directory: %w", err)
	}
	lock, err := os.OpenFile(stagingLockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open staging lock: %w", err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock staging area: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return fn()
}

func loadStagingManifest() (stagingManifest, error) {
	data, err := os.ReadFile(stagingMetadataPath())
	if errors.Is(err, os.ErrNotExist) {
		return stagingManifest{Version: 1}, nil
	}
	if err != nil {
		return stagingManifest{}, fmt.Errorf("read staging metadata: %w", err)
	}
	var manifest stagingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return stagingManifest{}, fmt.Errorf("parse staging metadata: %w", err)
	}
	if manifest.Version != 1 {
		return stagingManifest{}, fmt.Errorf("unsupported staging metadata version %d", manifest.Version)
	}
	return manifest, nil
}

func saveStagingManifest(manifest stagingManifest) error {
	manifest.Version = 1
	sort.Slice(manifest.Artifacts, func(i, j int) bool {
		return manifest.Artifacts[i].Filename < manifest.Artifacts[j].Filename
	})
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stagingRoot(), ".metadata-*.tmp")
	if err != nil {
		return fmt.Errorf("create staging metadata: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write staging metadata: %w", err)
	}
	if err = os.Rename(name, stagingMetadataPath()); err != nil {
		return fmt.Errorf("commit staging metadata: %w", err)
	}
	return nil
}

func parseDebIdentity(path string) (string, string, string, error) {
	cmd := exec.Command("dpkg-deb", "--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", path)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return "", "", "", fmt.Errorf("invalid Debian package: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
		return "", "", "", fmt.Errorf("invalid Debian package metadata")
	}
	return fields[0], fields[1], fields[2], nil
}

func snapshotPackage(source, temporaryDir string) (stagedArtifact, string, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return stagedArtifact{}, "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return stagedArtifact{}, "", fmt.Errorf("inspect %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return stagedArtifact{}, "", fmt.Errorf("%s is not a regular file", source)
	}
	if info.Size() <= 0 {
		return stagedArtifact{}, "", fmt.Errorf("%s is empty", source)
	}
	input, err := os.Open(absolute)
	if err != nil {
		return stagedArtifact{}, "", err
	}
	defer input.Close()
	tmp, err := os.CreateTemp(temporaryDir, ".artifact-*.tmp")
	if err != nil {
		return stagedArtifact{}, "", fmt.Errorf("create staged snapshot: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0600); err != nil {
		return stagedArtifact{}, "", err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), input)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return stagedArtifact{}, "", fmt.Errorf("snapshot %s: %w", source, err)
	}
	if written == 0 {
		return stagedArtifact{}, "", fmt.Errorf("%s is empty", source)
	}
	packageName, version, architecture, err := parseDebIdentity(tmpName)
	if err != nil {
		return stagedArtifact{}, "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	artifact := stagedArtifact{OriginalPath: absolute, Filename: filepath.Base(absolute), Package: packageName, Version: version, Architecture: architecture, Size: written, SHA256: digest, StagedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	keep = true
	return artifact, tmpName, nil
}

func stageCLI(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tpahub stage <package.deb>...")
	}
	return withStagingLock(func() error {
		manifest, err := loadStagingManifest()
		if err != nil {
			return err
		}
		temporary, err := os.MkdirTemp(stagingArtifactsRoot(), ".stage-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		incoming := make([]stagedArtifact, 0, len(args))
		temps := make([]string, 0, len(args))
		seen := map[string]string{}
		for _, path := range args {
			artifact, temp, snapshotErr := snapshotPackage(path, temporary)
			if snapshotErr != nil {
				return snapshotErr
			}
			identity := artifact.Package + "\x00" + artifact.Version + "\x00" + artifact.Architecture
			if prior, ok := seen[identity]; ok && prior != artifact.SHA256 {
				return fmt.Errorf("conflicting packages staged in one invocation: %s", identity)
			}
			seen[identity] = artifact.SHA256
			incoming = append(incoming, artifact)
			temps = append(temps, temp)
		}
		for i, artifact := range incoming {
			identity := artifact.Package + "\x00" + artifact.Version + "\x00" + artifact.Architecture
			index := -1
			for j := range manifest.Artifacts {
				if manifest.Artifacts[j].Package+"\x00"+manifest.Artifacts[j].Version+"\x00"+manifest.Artifacts[j].Architecture == identity {
					index = j
					break
				}
			}
			if index >= 0 && manifest.Artifacts[index].SHA256 == artifact.SHA256 && manifest.Artifacts[index].Size == artifact.Size {
				fmt.Printf("Already staged: %s %s %s\n", artifact.Package, artifact.Version, artifact.Architecture)
				continue
			}
			finalName := artifact.SHA256 + ".deb"
			finalPath := filepath.Join(stagingArtifactsRoot(), finalName)
			if err := os.Rename(temps[i], finalPath); err != nil {
				return fmt.Errorf("commit staged snapshot: %w", err)
			}
			artifact.StagedPath = finalPath
			if index >= 0 {
				old := manifest.Artifacts[index].StagedPath
				manifest.Artifacts[index] = artifact
				if old != "" && old != finalPath {
					_ = os.Remove(old)
				}
			} else {
				manifest.Artifacts = append(manifest.Artifacts, artifact)
			}
			fmt.Printf("Staged: %s %s %s\n", artifact.Package, artifact.Version, artifact.Architecture)
		}
		return saveStagingManifest(manifest)
	})
}

func unstageCLI(args []string) error {
	return withStagingLock(func() error {
		manifest, err := loadStagingManifest()
		if err != nil {
			return err
		}
		if len(args) == 1 && args[0] == "--all" {
			for _, artifact := range manifest.Artifacts {
				_ = os.Remove(artifact.StagedPath)
			}
			manifest.Artifacts = nil
			if err := saveStagingManifest(manifest); err != nil {
				return err
			}
			fmt.Println("Staging cleared.")
			return nil
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: tpahub unstage <package.deb> | tpahub unstage --all")
		}
		value, _ := filepath.Abs(args[0])
		matches := make([]int, 0, 1)
		for i, artifact := range manifest.Artifacts {
			if artifact.OriginalPath == value || artifact.Filename == filepath.Base(args[0]) {
				matches = append(matches, i)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("package is not staged: %s", args[0])
		}
		if len(matches) > 1 {
			return fmt.Errorf("package name is ambiguous; use the original path: %s", args[0])
		}
		artifact := manifest.Artifacts[matches[0]]
		_ = os.Remove(artifact.StagedPath)
		manifest.Artifacts = append(manifest.Artifacts[:matches[0]], manifest.Artifacts[matches[0]+1:]...)
		if err := saveStagingManifest(manifest); err != nil {
			return err
		}
		fmt.Printf("Unstaged: %s %s %s\n", artifact.Package, artifact.Version, artifact.Architecture)
		return nil
	})
}

func verifyStagedArtifact(artifact stagedArtifact) error {
	stagedPath, err := filepath.Abs(artifact.StagedPath)
	artifactRoot, rootErr := filepath.Abs(stagingArtifactsRoot())
	if rootErr != nil || err != nil || filepath.Dir(stagedPath) != artifactRoot {
		return fmt.Errorf("staged artifact path is outside the staging area: %s", artifact.Filename)
	}
	info, err := os.Lstat(stagedPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("staged artifact is missing: %s", artifact.Filename)
	}
	if info.Size() != artifact.Size {
		return fmt.Errorf("staged artifact size changed: %s", artifact.Filename)
	}
	digest, err := fileSHA256(stagedPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(digest, artifact.SHA256) {
		return fmt.Errorf("staged artifact hash changed: %s", artifact.Filename)
	}
	packageName, version, architecture, err := parseDebIdentity(stagedPath)
	if err != nil || packageName != artifact.Package || version != artifact.Version || architecture != artifact.Architecture {
		return fmt.Errorf("staged artifact metadata changed: %s", artifact.Filename)
	}
	return nil
}

func statusCLI() error {
	return withStagingLock(func() error {
		manifest, err := loadStagingManifest()
		if err != nil {
			return err
		}
		if len(manifest.Artifacts) == 0 {
			fmt.Println("No packages staged.")
			return nil
		}
		fmt.Println("Staged for publication:")
		var total int64
		for _, artifact := range manifest.Artifacts {
			fmt.Printf("\n  %s %s %s\n    %s\n    %s\n    sha256:%s\n", artifact.Package, artifact.Version, artifact.Architecture, artifact.Filename, formatBytes(artifact.Size), artifact.SHA256)
			total += artifact.Size
		}
		fmt.Printf("\n%d packages staged\n%s total\n", len(manifest.Artifacts), formatBytes(total))
		return nil
	})
}

func clearStagingLocked(manifest stagingManifest) error {
	for _, artifact := range manifest.Artifacts {
		_ = os.Remove(artifact.StagedPath)
	}
	manifest.Artifacts = nil
	return saveStagingManifest(manifest)
}
