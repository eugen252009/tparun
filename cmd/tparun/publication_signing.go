package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/tpa"
)

func publicationRunnerForConfig(cfg config.Config) publicationRunner {
	switch cfg.PublicationBackend {
	case "", "local":
		return publicationTPARunner(cfg)
	case "ssh":
		return tpa.SSHRunner{Config: tpa.SSHConfig{Target: cfg.SSHWorkerTarget, IdentityFile: cfg.SSHIdentityFile,
			KnownHostsFile: cfg.SSHKnownHostsFile, Port: cfg.SSHWorkerPort, SSHBinary: cfg.SSHBinary, ConnectTimeout: cfg.SSHConnectTimeout}}
	default:
		return invalidPublicationRunner{err: fmt.Errorf("unsupported publication backend %q", cfg.PublicationBackend)}
	}
}

type invalidPublicationRunner struct{ err error }

func (r invalidPublicationRunner) Check(context.Context) error { return r.err }

func publicationTPARunner(cfg config.Config) tpa.Runner {
	home := cfg.GPGHome
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			home = filepath.Join(home, ".gnupg")
		}
	}
	var protected []string
	if home != "" {
		protected = []string{home}
	}
	return tpa.Runner{Bin: cfg.TPABin, ProtectedPaths: protected}
}

func signPublicationCandidate(ctx context.Context, candidate string, cfg config.Config) error {
	if cfg.GPGFingerprint == "" {
		return fmt.Errorf("publication signing fingerprint is not configured")
	}
	distDir := filepath.Join(candidate, "dists", "stable")
	releasePath := filepath.Join(distDir, "Release")
	inReleasePath := filepath.Join(distDir, "InRelease")
	releaseInfo, err := os.Lstat(releasePath)
	if err != nil || !releaseInfo.Mode().IsRegular() {
		return fmt.Errorf("unsigned candidate Release is unavailable or not regular")
	}
	for _, path := range []string{inReleasePath, filepath.Join(distDir, "Release.gpg")} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("unsigned candidate already contains signature artifact: %s", filepath.Base(path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, "gpg", "--batch", "--yes", "--clearsign", "-u", cfg.GPGFingerprint, "-o", inReleasePath, releasePath)
	cmd.Env = serverGPGEnvironment(cfg)
	cmd.Stdout = io.Discard
	var diagnostic boundedDiagnostic
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(diagnostic.String())
		if message != "" {
			return fmt.Errorf("GPG signing failed: %w: %s", err, message)
		}
		return fmt.Errorf("GPG signing failed: %w", err)
	}
	if err := os.Chmod(inReleasePath, 0644); err != nil {
		return fmt.Errorf("set InRelease permissions: %w", err)
	}
	return nil
}

func serverGPGEnvironment(cfg config.Config) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, variable := range os.Environ() {
		if name, _, _ := strings.Cut(variable, "="); name == "GNUPGHOME" && cfg.GPGHome != "" {
			continue
		}
		environment = append(environment, variable)
	}
	if cfg.GPGHome != "" {
		environment = append(environment, "GNUPGHOME="+cfg.GPGHome)
	}
	return environment
}

func checkCandidateBounds(candidate string, byteLimit, allocationLimit int64) error {
	candidateBytes, err := fileTreeSize(candidate)
	if err != nil {
		return err
	}
	candidateAllocated, err := fileTreeAllocatedSize(candidate)
	if err != nil {
		return err
	}
	if candidateBytes > byteLimit || candidateAllocated > allocationLimit {
		return fmt.Errorf("candidate exceeded reserved capacity")
	}
	return nil
}
