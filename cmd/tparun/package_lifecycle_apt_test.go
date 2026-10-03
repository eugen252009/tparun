package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

// This opt-in qualification publishes a disposable managed-signed repository,
// installs through Debian APT, unlists and deletes through the TPA.run CLI, and
// checks both installed-package continuity and active-payload quota accounting.
func TestManagedPackageLifecycleDockerAPT(t *testing.T) {
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
	tpaBin := buildCurrentTPA(t)
	signing := testCandidateSigningConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, handler := configuredTestServer(t, func(s *server) {
		s.ctx = ctx
		s.cfg.GPGHome = signing.GPGHome
		s.cfg.GPGFingerprint = signing.GPGFingerprint
		s.cfg.TPABin = tpaBin
		s.cfg.MaxUpload = 16 << 20
		s.cfg.MaxStagingBytes = 4 << 30
		s.cfg.MaxAccountStagingBytes = 1 << 30
		s.cfg.MaxCandidateInodes = 65536
		s.cfg.MinDiskFreeBytes = 0
		s.cfg.MinFreeInodes = 0
		s.cfg.PublishTimeout = 2 * time.Minute
		s.cfg.MaxGenerations = 3
		s.verifyCandidate = nil
		s.verifyUnsignedCandidate = nil
		s.signCandidate = nil
		s.runner = publicationTPARunner(s.cfg)
	})
	repositoryID := createLocalPolicyRepo(t, server, "tph_s_test", "public")
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); server.worker() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(10 * time.Second):
			t.Error("publication worker did not stop")
		}
	})

	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	if output, err := exec.Command(tpaBin, "init", "-name=lifecycle-e2e", "-ver=1.0", "-arch=amd64", "-maintainer=TPA.run lifecycle <lifecycle@example.invalid>", "-desc=Disposable lifecycle qualification", "-out="+tree).CombinedOutput(); err != nil {
		t.Fatalf("TPA init: %v: %s", err, output)
	}
	payloadPath := filepath.Join(tree, "usr", "share", "lifecycle-e2e", "payload")
	if err = os.MkdirAll(filepath.Dir(payloadPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(payloadPath, []byte("installed before unlisting\n"), 0644); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(root, "lifecycle-e2e_1.0_amd64.deb")
	if output, err := exec.Command(tpaBin, "build", "-in="+tree, "-out="+deb).CombinedOutput(); err != nil {
		t.Fatalf("TPA build: %v: %s", err, output)
	}
	debBytes, err := os.ReadFile(deb)
	if err != nil {
		t.Fatal(err)
	}
	upload, err := http.NewRequest(http.MethodPost, handler.URL+"/v1/repositories/"+repositoryID+"/publishes?filename=lifecycle-e2e_1.0_amd64.deb", bytes.NewReader(debBytes))
	if err != nil {
		t.Fatal(err)
	}
	upload.Header.Set("Authorization", "Bearer tph_s_test")
	upload.Header.Set("Content-Type", "application/octet-stream")
	response, err := handler.Client().Do(upload)
	if err != nil {
		t.Fatal(err)
	}
	var queued struct {
		ID string `json:"id"`
	}
	responseData, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || json.Unmarshal(responseData, &queued) != nil || queued.ID == "" {
		t.Fatalf("managed publication upload=%d %s", response.StatusCode, responseData)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		job, jobErr := server.db.Job(context.Background(), queued.ID)
		if jobErr != nil {
			t.Fatal(jobErr)
		}
		if job.State == "succeeded" {
			break
		}
		if job.State == "failed" {
			t.Fatalf("managed publication failed: %s", job.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed publication timed out: %+v", job)
		}
		time.Sleep(25 * time.Millisecond)
	}

	activeGeneration, activeErr := server.db.ActiveGeneration(context.Background(), repositoryID)
	if activeErr != nil {
		t.Fatal(activeErr)
	}
	activeRoot := filepath.Join(server.cfg.DataDir, "repos", repositoryID, "generations", activeGeneration)
	if info, statErr := os.Stat(filepath.Join(activeRoot, "dists", "stable", "InRelease")); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("managed active generation has no InRelease: root=%s err=%v", activeRoot, statErr)
	}
	cli := filepath.Join(root, "tparun")
	buildCLI := exec.Command("go", "build", "-o", cli, "./cmd/tparun")
	buildCLI.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := buildCLI.CombinedOutput(); err != nil {
		t.Fatalf("build TPA.run CLI: %v: %s", err, output)
	}
	configRoot := filepath.Join(root, "config")
	if err = os.MkdirAll(filepath.Join(configRoot, "tparun"), 0700); err != nil {
		t.Fatal(err)
	}
	clientConfig, err := json.Marshal(map[string]any{"version": 2, "server": handler.URL, "credentials": map[string]any{handler.URL: map[string]string{"token": "tph_s_test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(configRoot, "tparun", "config.json"), clientConfig, 0600); err != nil {
		t.Fatal(err)
	}
	publicKey, err := exec.Command("gpg", "--batch", "--homedir", signing.GPGHome, "--export", signing.GPGFingerprint).Output()
	if err != nil {
		t.Fatal(err)
	}
	container := "tparun-lifecycle-apt-" + newID()[:12]
	if output, err := exec.Command(docker, "run", "-d", "--rm", "--network=host", "--name", container, "debian:bookworm-slim", "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("start disposable Debian container: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", container).Run() })
	keyPath := filepath.Join(root, "apt-key.gpg")
	if err = os.WriteFile(keyPath, publicKey, 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(docker, "cp", keyPath, container+":/usr/share/keyrings/tparun-lifecycle.gpg").CombinedOutput(); err != nil {
		t.Fatalf("copy disposable APT key: %v: %s", err, output)
	}
	repositoryURL := handler.URL + "/" + repositoryID
	sourcePath := filepath.Join(root, "lifecycle.list")
	source := fmt.Sprintf("deb [arch=amd64 signed-by=/usr/share/keyrings/tparun-lifecycle.gpg] %s/r/%s stable main\n", handler.URL, repositoryID)
	if err = os.WriteFile(sourcePath, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(docker, "cp", sourcePath, container+":/etc/apt/lifecycle.list").CombinedOutput(); err != nil {
		t.Fatalf("copy disposable APT source: %v: %s", err, output)
	}
	apt := func(args ...string) []byte {
		t.Helper()
		command := []string{"exec", container, "apt-get", "-o", "Dir::Etc::sourcelist=/etc/apt/lifecycle.list", "-o", "Dir::Etc::sourceparts=-", "-o", "APT::Get::List-Cleanup=0"}
		command = append(command, args...)
		output, err := exec.Command(docker, command...).CombinedOutput()
		if err != nil {
			t.Fatalf("Debian APT %v: %v: %s", args, err, output)
		}
		return output
	}
	apt("update")
	apt("install", "-y", "--no-install-recommends", "lifecycle-e2e=1.0")
	installedVersion := func() string {
		t.Helper()
		output, err := exec.Command(docker, "exec", container, "dpkg-query", "-W", "-f=${Version}", "lifecycle-e2e").CombinedOutput()
		if err != nil {
			t.Fatalf("query installed package: %v: %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if got := installedVersion(); got != "1.0" {
		t.Fatalf("installed version=%q", got)
	}
	cliEnv := append(os.Environ(), "XDG_CONFIG_HOME="+configRoot)
	runLifecycleCLI := func(args ...string) string {
		t.Helper()
		command := exec.Command(cli, args...)
		command.Env = cliEnv
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("tparun %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	repositoryRef := repositoryURL
	unlistOutput := runLifecycleCLI("package", "unlist", repositoryRef, "lifecycle-e2e", "1.0", "amd64")
	unlistID := lifecycleJobID(t, unlistOutput)
	if got := installedVersion(); got != "1.0" {
		t.Fatalf("unlisting removed the installed package: %q", got)
	}
	apt("update")
	unlistStatus := fetchLifecycleStatus(t, handler, "tph_s_test", unlistID)
	if unlistStatus.Lifecycle == nil || unlistStatus.Lifecycle.MetadataState != "published" || unlistStatus.Lifecycle.ArtifactState != "retained" {
		t.Fatalf("unlist lifecycle audit=%+v", unlistStatus.Lifecycle)
	}
	installed := exec.Command(docker, "exec", container, "apt-cache", "policy", "lifecycle-e2e")
	policy, err := installed.CombinedOutput()
	if err != nil || strings.Contains(string(policy), "http://") || strings.Contains(string(policy), "https://") {
		t.Fatalf("APT still offers unlisted package from the repository: %v: %s", err, policy)
	}
	deleteOutput := runLifecycleCLI("package", "delete", repositoryRef, "lifecycle-e2e", "1.0", "amd64", "--yes")
	deleteID := lifecycleJobID(t, deleteOutput)
	apt("update")
	if got := installedVersion(); got != "1.0" {
		t.Fatalf("deleting repository artifact removed the installed package: %q", got)
	}
	deleteStatus := fetchLifecycleStatus(t, handler, "tph_s_test", deleteID)
	if deleteStatus.State != "succeeded" || deleteStatus.Lifecycle == nil || deleteStatus.Lifecycle.MetadataState != "unchanged" || deleteStatus.Lifecycle.ArtifactState != "deleted" {
		t.Fatalf("delete lifecycle audit=%+v", deleteStatus)
	}
	usage, err := server.accountUsage(context.Background(), "a")
	if err != nil || usage.PackageCount != 0 || usage.StoredBytes != 0 {
		t.Fatalf("active payload quota after delete=%+v err=%v", usage, err)
	}
	activeGeneration, err = server.db.ActiveGeneration(context.Background(), repositoryID)
	if err != nil || activeGeneration != deleteID {
		t.Fatalf("active generation=%q want=%q err=%v", activeGeneration, deleteID, err)
	}
	activeArtifact := filepath.Join(server.cfg.DataDir, "repos", repositoryID, "generations", activeGeneration, filepath.FromSlash(deleteStatus.Lifecycle.ArtifactFilename))
	if _, err = os.Stat(activeArtifact); !os.IsNotExist(err) {
		t.Fatalf("deleted artifact remains in active generation: %v", err)
	}
	if _, err = os.Stat(filepath.Join(server.cfg.DataDir, "repos", repositoryID, "generations", queued.ID, filepath.FromSlash(deleteStatus.Lifecycle.ArtifactFilename))); err != nil {
		t.Fatalf("retained generation did not preserve its independent artifact: %v", err)
	}
}

func lifecycleJobID(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Package lifecycle job ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				return strings.TrimSuffix(fields[3], ":")
			}
		}
	}
	t.Fatalf("lifecycle job ID missing from CLI output: %s", output)
	return ""
}

type lifecycleJobStatus struct {
	store.Job
	Lifecycle *store.PackageLifecycleJob `json:"lifecycle"`
}

func fetchLifecycleStatus(t *testing.T, handler *httptest.Server, token, jobID string) lifecycleJobStatus {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, handler.URL+"/v1/publish-jobs/"+jobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := handler.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status lifecycleJobStatus
	if err = json.NewDecoder(response.Body).Decode(&status); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("lifecycle job status=%d err=%v", response.StatusCode, err)
	}
	return status
}
