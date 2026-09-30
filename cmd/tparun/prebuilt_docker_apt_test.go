package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

// This opt-in integration test exercises the actual TPA and TPA.run CLIs,
// streamed publication API, and stock Debian APT against two signed snapshots.
func TestPrebuiltGenerationDockerAPTUpgrade(t *testing.T) {
	if os.Getenv("TPARUN_DOCKER_APT_QUALIFICATION") != "1" {
		t.Skip("set TPARUN_DOCKER_APT_QUALIFICATION=1 to run the disposable Docker APT qualification")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is unavailable")
	}
	if output, err := exec.Command(docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("Docker daemon is unavailable: %v: %s", err, output)
	}
	server, handler := testServer(t)
	server.cfg.MaxUpload = 64 << 20
	server.cfg.MinDiskFreeBytes = 0
	server.cfg.MinFreeInodes = 0
	server.cfg.PublishTimeout = time.Minute
	server.cfg.MaxGenerations = 4
	repositoryID := strings.Repeat("a", 32)
	if err := server.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "docker-apt", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	signing := testCandidateSigningConfig(t)
	publicKey, err := exec.Command("gpg", "--batch", "--homedir", signing.GPGHome, "--export", signing.GPGFingerprint).Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.db.AddRepositoryAPTSigner(context.Background(), store.RepositoryAPTSigner{RepositoryID: repositoryID, Fingerprint: signing.GPGFingerprint, PublicKey: publicKey, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	tpa := buildCurrentTPA(t)
	tparun := filepath.Join(root, "tparun")
	build := exec.Command("go", "build", "-o", tparun, "./cmd/tparun")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build TPA.run CLI: %v: %s", err, output)
	}
	configRoot := filepath.Join(root, "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "tparun"), 0700); err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]any{
		"version":     2,
		"server":      handler.URL,
		"credentials": map[string]any{handler.URL: map[string]string{"token": "tph_s_test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "tparun", "config.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	publish := func(version, generation, parent string) string {
		t.Helper()
		work := filepath.Join(root, "work-"+version)
		artifactDir := filepath.Join(root, "artifacts-"+version)
		candidate := filepath.Join(root, "candidate-"+version)
		manifest := filepath.Join(root, "manifest-"+version+".json")
		tree := filepath.Join(work, "package")
		if err := os.MkdirAll(filepath.Join(artifactDir), 0755); err != nil {
			t.Fatal(err)
		}
		init := exec.Command(tpa, "init", "-name=tparun-e2e-fixture", "-ver="+version, "-arch=amd64", "-maintainer=TPA.run E2E <e2e@example.invalid>", "-desc=Disposable signed APT upgrade fixture", "-out="+tree)
		if output, err := init.CombinedOutput(); err != nil {
			t.Fatalf("TPA init %s: %v: %s", version, err, output)
		}
		payload := filepath.Join(tree, "usr", "share", "tparun-e2e-fixture", "payload")
		if err := os.MkdirAll(filepath.Dir(payload), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payload, []byte("version "+version+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		deb := filepath.Join(artifactDir, "tparun-e2e-fixture_"+version+"_amd64.deb")
		buildDeb := exec.Command(tpa, "build", "-in="+tree, "-out="+deb)
		if output, err := buildDeb.CombinedOutput(); err != nil {
			t.Fatalf("TPA build %s: %v: %s", version, err, output)
		}
		args := []string{"pack", "-in=" + artifactDir, "-out=" + candidate, "-gpg=" + signing.GPGFingerprint, "-generation-manifest=" + manifest, "-repository-id=" + repositoryID, "-generation-id=" + generation}
		if parent != "" {
			args = append(args, "-parent-generation="+parent)
		}
		pack := exec.Command(tpa, args...)
		pack.Env = append(os.Environ(), "GNUPGHOME="+signing.GPGHome)
		if output, err := pack.CombinedOutput(); err != nil {
			t.Fatalf("TPA pack %s: %v: %s", version, err, output)
		}
		cli := exec.Command(tparun, "publish-generation", repositoryID, "--directory", candidate, "--manifest", manifest)
		cli.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configRoot)
		if output, err := cli.CombinedOutput(); err != nil {
			t.Fatalf("TPA.run publish-generation %s: %v: %s", version, err, output)
		}
		return candidate
	}
	v1 := strings.Repeat("1", 32)
	publish("1.0", v1, "")

	container := "tparun-prebuilt-apt-" + newID()[:12]
	if output, err := exec.Command(docker, "run", "-d", "--rm", "--network=host", "--name", container, "debian:bookworm-slim", "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("start disposable Debian container: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", container).Run() })
	keyPath := filepath.Join(root, "apt-e2e.gpg")
	if err := os.WriteFile(keyPath, publicKey, 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(docker, "cp", keyPath, container+":/usr/share/keyrings/tpa-e2e.gpg").CombinedOutput(); err != nil {
		t.Fatalf("copy disposable APT keyring: %v: %s", err, output)
	}
	source := fmt.Sprintf("deb [arch=amd64 signed-by=/usr/share/keyrings/tpa-e2e.gpg] %s/r/%s stable main\n", handler.URL, repositoryID)
	sourcePath := filepath.Join(root, "tpa-e2e.list")
	if err := os.WriteFile(sourcePath, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(docker, "cp", sourcePath, container+":/etc/apt/tpa-e2e.list").CombinedOutput(); err != nil {
		t.Fatalf("copy disposable APT source: %v: %s", err, output)
	}
	apt := func(args ...string) {
		t.Helper()
		aptArgs := []string{"exec", container, "apt-get", "-o", "Dir::Etc::sourcelist=/etc/apt/tpa-e2e.list", "-o", "Dir::Etc::sourceparts=-", "-o", "APT::Get::List-Cleanup=0"}
		aptArgs = append(aptArgs, args...)
		if output, err := exec.Command(docker, aptArgs...).CombinedOutput(); err != nil {
			t.Fatalf("disposable APT %v: %v: %s", args, err, output)
		}
	}
	apt("update")
	apt("install", "-y", "--no-install-recommends", "tparun-e2e-fixture=1.0")
	checkVersion := func(want string) {
		t.Helper()
		output, err := exec.Command(docker, "exec", container, "dpkg-query", "-W", "-f=${Version}", "tparun-e2e-fixture").CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != want {
			t.Fatalf("installed package version=%q want=%q err=%v", strings.TrimSpace(string(output)), want, err)
		}
	}
	checkVersion("1.0")
	v2 := strings.Repeat("2", 32)
	publish("2.0", v2, v1)
	apt("update")
	apt("install", "-y", "--no-install-recommends", "tparun-e2e-fixture=2.0")
	checkVersion("2.0")
}
