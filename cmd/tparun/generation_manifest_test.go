package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestDecodeGenerationManifestRequiresCanonicalSafeInventory(t *testing.T) {
	good := generationManifest{Version: 1, RepositoryID: "repo", GenerationID: strings.Repeat("a", 32), ParentGeneration: "", Files: []generationManifestFile{{Path: "dists/stable/Release", Size: 1, SHA256: strings.Repeat("a", 64)}}}
	data, err := json.MarshalIndent(good, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, _, err = decodeGenerationManifest(data, "repo"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*generationManifest){
		"traversal":           func(m *generationManifest) { m.Files[0].Path = "../escape" },
		"wrong-repository":    func(m *generationManifest) { m.RepositoryID = "other" },
		"unsupported-version": func(m *generationManifest) { m.Version = 2 },
		"digest":              func(m *generationManifest) { m.Files[0].SHA256 = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := good
			copy.Files = append([]generationManifestFile(nil), good.Files...)
			mutate(&copy)
			bad, _ := json.MarshalIndent(copy, "", "  ")
			bad = append(bad, '\n')
			if _, _, err := decodeGenerationManifest(bad, "repo"); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	duplicate := strings.Replace(string(data), `"version": 1`, `"version": 1, "version": 1`, 1)
	if _, _, err := decodeGenerationManifest([]byte(duplicate), "repo"); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
	tooDeep := good
	tooDeep.Files = []generationManifestFile{{Path: strings.TrimSuffix(strings.Repeat("a/", maxGenerationPathDepth+1), "/"), Size: 1, SHA256: strings.Repeat("a", 64)}}
	if _, _, err := decodeGenerationManifest(canonicalManifest(t, tooDeep), "repo"); err == nil {
		t.Fatal("over-depth path accepted")
	}
	fileDirectoryCollision := good
	fileDirectoryCollision.Files = []generationManifestFile{{Path: "a", Size: 1, SHA256: strings.Repeat("a", 64)}, {Path: "a/b", Size: 1, SHA256: strings.Repeat("a", 64)}}
	if _, _, err := decodeGenerationManifest(canonicalManifest(t, fileDirectoryCollision), "repo"); err == nil {
		t.Fatal("file/directory path collision accepted")
	}
	manyDirectories := good
	manyDirectories.Files = make([]generationManifestFile, 1100)
	for i := range manyDirectories.Files {
		parts := []string{fmt.Sprintf("d%04d", i)}
		for depth := 0; depth < maxGenerationPathDepth-2; depth++ {
			parts = append(parts, "x")
		}
		parts = append(parts, "file")
		manyDirectories.Files[i] = generationManifestFile{Path: strings.Join(parts, "/"), Size: 0, SHA256: strings.Repeat("a", 64)}
	}
	if _, _, err := decodeGenerationManifest(canonicalManifest(t, manyDirectories), "repo"); err == nil {
		t.Fatal("manifest with excessive directory count accepted")
	}
}

func TestDecodeGenerationManifestSupportsTenThousandFiles(t *testing.T) {
	manifest := generationManifest{
		Version: 1, RepositoryID: "repo", GenerationID: strings.Repeat("a", 32),
		Files: make([]generationManifestFile, 10000),
	}
	for index := range manifest.Files {
		manifest.Files[index] = generationManifestFile{
			Path: fmt.Sprintf("package-%05d.deb", index), Size: 1, SHA256: strings.Repeat("a", 64),
		}
	}
	decoded, total, err := decodeGenerationManifest(canonicalManifest(t, manifest), "repo")
	if err != nil {
		t.Fatalf("decode 10,000-file manifest: %v", err)
	}
	if len(decoded.Files) != len(manifest.Files) || total != int64(len(manifest.Files)) {
		t.Fatalf("decoded files=%d total=%d, want files=%d total=%d", len(decoded.Files), total, len(manifest.Files), len(manifest.Files))
	}
}

func TestPrebuiltSignedGenerationAdmissionAndParentCAS(t *testing.T) {
	s, h := testServer(t)
	s.cfg.MaxUpload = 16 << 20
	s.cfg.PublishTimeout = time.Minute
	repositoryID := strings.Repeat("b", 32)
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "prebuilt", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	signing := testCandidateSigningConfig(t)
	key, err := exec.Command("gpg", "--batch", "--homedir", signing.GPGHome, "--export", signing.GPGFingerprint).Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryAPTSigner(context.Background(), store.RepositoryAPTSigner{RepositoryID: repositoryID, Fingerprint: signing.GPGFingerprint, PublicKey: key, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	buildSemanticTestDeb(t, filepath.Join(work, "test_1_amd64.deb"))
	candidate := filepath.Join(t.TempDir(), "candidate")
	writeSemanticCandidate(t, candidate, work, signing)
	manifest := inventoryForTest(t, candidate, repositoryID, strings.Repeat("c", 32), "")
	manifestPath := filepath.Join(t.TempDir(), "generation.json")
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, append(manifestData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	enteredWrite := make(chan struct{})
	resumeWrite := make(chan struct{})
	var gateMu sync.Mutex
	var gatedPath string
	var resumeOnce sync.Once
	resumeUpload := func() { resumeOnce.Do(func() { close(resumeWrite) }) }
	defer resumeUpload()
	s.capacityObserve = func(stage, path string) {
		if stage != "before-write" {
			return
		}
		gateMu.Lock()
		if gatedPath == "" {
			gatedPath = path
			close(enteredWrite)
			gateMu.Unlock()
			<-resumeWrite
			return
		}
		gateMu.Unlock()
	}
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- publishClientGeneration(cliConfig{Server: h.URL, Token: "tph_s_test"}, repositoryID, candidate, manifestPath)
	}()
	select {
	case <-enteredWrite:
	case <-time.After(10 * time.Second):
		resumeUpload()
		t.Fatal("first upload did not reach streamed file write")
	}
	if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", candidate, manifest); response.Code != http.StatusConflict {
		t.Fatalf("concurrent same-repository upload status=%d body=%s", response.Code, response.Body.String())
	}
	independentRepository := strings.Repeat("e", 32)
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: independentRepository, AccountID: "a", Name: "independent", Visibility: "public", CreatedAt: "now"}); err != nil {
		resumeUpload()
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryAPTSigner(context.Background(), store.RepositoryAPTSigner{RepositoryID: independentRepository, Fingerprint: signing.GPGFingerprint, PublicKey: key, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		resumeUpload()
		t.Fatal(err)
	}
	independentManifest := inventoryForTest(t, candidate, independentRepository, strings.Repeat("e", 31)+"f", "")
	if response := postGenerationForTest(t, h.URL, independentRepository, "tph_s_test", candidate, independentManifest); response.Code != http.StatusAccepted {
		resumeUpload()
		t.Fatalf("different-repository upload was blocked: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(s.reservations) != 1 {
		resumeUpload()
		t.Fatalf("duplicate upload changed the active capacity reservation count: %d", len(s.reservations))
	}
	resumeUpload()
	if err = <-publishDone; err != nil {
		t.Fatalf("client generation upload: %v", err)
	}
	s.capacityObserve = nil
	active, err := s.db.ActiveGeneration(context.Background(), repositoryID)
	if err != nil || active != manifest.GenerationID {
		t.Fatalf("active generation=%q err=%v", active, err)
	}
	stored := filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations", active)
	if err := verifyGenerationInventory(stored, manifest); err != nil {
		t.Fatalf("stored generation changed: %v", err)
	}
	commitFailureID := strings.Repeat("a", 32)
	commitFailureManifest := inventoryForTest(t, candidate, repositoryID, commitFailureID, active)
	s.durablePublicationFault = func() error { return errors.New("injected durable commit failure") }
	if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", candidate, commitFailureManifest); response.Code != http.StatusInternalServerError {
		t.Fatalf("durable commit failure status=%d body=%s", response.Code, response.Body.String())
	}
	s.durablePublicationFault = nil
	if activeNow, err := s.db.ActiveGeneration(context.Background(), repositoryID); err != nil || activeNow != active {
		t.Fatalf("failed commit changed active generation=%q err=%v", activeNow, err)
	}
	pointerTarget, err := os.Readlink(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current"))
	if err != nil || pointerTarget != filepath.Join("generations", active) {
		t.Fatalf("failed commit left pointer target=%q err=%v", pointerTarget, err)
	}
	if _, err = os.Lstat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations", commitFailureID)); !os.IsNotExist(err) {
		t.Fatalf("failed commit generation was retained: %v", err)
	}
	failedID := strings.Repeat("9", 32)
	repositoryRoot := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
	blocker := filepath.Join(repositoryRoot, "current."+failedID)
	if err = os.WriteFile(blocker, []byte("block symlink creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedManifest := inventoryForTest(t, candidate, repositoryID, failedID, manifest.GenerationID)
	if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", candidate, failedManifest); response.Code != http.StatusInternalServerError {
		t.Fatalf("activation preparation failure status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err = os.Lstat(filepath.Join(repositoryRoot, "generations", failedID)); !os.IsNotExist(err) {
		t.Fatalf("failed generation was not removed: %v", err)
	}
	if len(s.reservations) != 0 {
		t.Fatalf("failed generation capacity reservation leaked: %d", len(s.reservations))
	}
	if err = os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	quotaID := strings.Repeat("8", 32)
	quotaManifest := inventoryForTest(t, candidate, repositoryID, quotaID, manifest.GenerationID)
	originalMaxUpload := s.cfg.MaxUpload
	s.cfg.MaxUpload = int64(len(manifestData) + 512)
	if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", candidate, quotaManifest); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload limit failure status=%d body=%s", response.Code, response.Body.String())
	}
	if len(s.reservations) != 0 {
		t.Fatalf("rejected generation capacity reservation leaked: %d", len(s.reservations))
	}
	s.cfg.MaxUpload = originalMaxUpload
	stale := inventoryForTest(t, candidate, repositoryID, strings.Repeat("d", 32), "")
	if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", candidate, stale); response.Code != http.StatusConflict {
		t.Fatalf("stale parent status=%d body=%s", response.Code, response.Body.String())
	}
	untrustedSigning := testCandidateSigningConfig(t)
	untrustedCandidate := filepath.Join(t.TempDir(), "candidate")
	writeSemanticCandidate(t, untrustedCandidate, work, untrustedSigning)
	untrusted := inventoryForTest(t, untrustedCandidate, repositoryID, strings.Repeat("f", 32), manifest.GenerationID)
	var earlyWrites []string
	s.capacityObserve = func(stage, path string) {
		if stage == "before-write" {
			earlyWrites = append(earlyWrites, path)
		}
	}
	if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", untrustedCandidate, untrusted); response.Code == http.StatusAccepted {
		t.Fatalf("unregistered signer accepted: %s", response.Body.String())
	}
	s.capacityObserve = nil
	if len(earlyWrites) != 1 || !strings.HasSuffix(earlyWrites[0], filepath.Join("dists", "stable", "InRelease")) {
		t.Fatalf("untrusted signer caused remaining payload writes: %v", earlyWrites)
	}
	untrustedManifestPath := filepath.Join(t.TempDir(), "generation.json")
	if err := os.WriteFile(untrustedManifestPath, canonicalManifest(t, untrusted), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishClientGeneration(cliConfig{Server: h.URL, Token: "tph_s_test"}, repositoryID, untrustedCandidate, untrustedManifestPath); err == nil || !strings.Contains(err.Error(), "generation publication failed") {
		t.Fatalf("streaming CLI did not surface early signer rejection: %v", err)
	}
	for i, relative := range []string{
		"dists/stable/InRelease",
		"dists/stable/Release",
		"dists/stable/main/binary-amd64/Packages",
		"pool/test_1_amd64.deb",
	} {
		tampered := filepath.Join(t.TempDir(), "candidate")
		copyGenerationForTest(t, candidate, tampered)
		path := filepath.Join(tampered, filepath.FromSlash(relative))
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte("tampered")); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		badManifest := inventoryForTest(t, tampered, repositoryID, fmt.Sprintf("%032x", 10+i), manifest.GenerationID)
		if response := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", tampered, badManifest); response.Code == http.StatusAccepted {
			t.Fatalf("tampered APT file %s accepted", relative)
		}
	}
	if err := os.WriteFile(filepath.Join(candidate, "tamper"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	badManifest := inventoryForTest(t, candidate, repositoryID, strings.Repeat("d", 32), manifest.GenerationID)
	failed := postGenerationForTest(t, h.URL, repositoryID, "tph_s_test", candidate, badManifest)
	if failed.Code == http.StatusAccepted {
		t.Fatalf("generation with unexpected file accepted: %s", failed.Body.String())
	}
	active, err = s.db.ActiveGeneration(context.Background(), repositoryID)
	if err != nil || active != manifest.GenerationID {
		t.Fatalf("rejected generation changed active pointer: generation=%q err=%v", active, err)
	}
}

func canonicalManifest(t *testing.T, manifest generationManifest) []byte {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func copyGenerationForTest(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func inventoryForTest(t *testing.T, root, repository, generation, parent string) generationManifest {
	t.Helper()
	manifest := generationManifest{Version: 1, RepositoryID: repository, GenerationID: generation, ParentGeneration: parent, Files: []generationManifestFile{}}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, generationManifestFile{Path: filepath.ToSlash(rel), Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// WalkDir returns lexical order, which is also slash-path order here.
	return manifest
}

func postGenerationForTest(t *testing.T, serverURL, repository, token, root string, manifest generationManifest) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	manifestPart, err := writer.CreateFormField("manifest")
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	if _, err = manifestPart.Write(manifestData); err != nil {
		t.Fatal(err)
	}
	writeFile := func(file generationManifestFile) {
		part, err := writer.CreateFormField("file:" + file.Path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	var inRelease *generationManifestFile
	for i := range manifest.Files {
		if manifest.Files[i].Path == "dists/stable/InRelease" {
			inRelease = &manifest.Files[i]
			break
		}
	}
	if inRelease == nil {
		t.Fatal("test manifest has no InRelease")
	}
	writeFile(*inRelease)
	for _, file := range manifest.Files {
		if file.Path != inRelease.Path {
			writeFile(file)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, serverURL+"/v1/repositories/"+repository+"/generations", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	return serveTestRequest(t, serverURL, request)
}

func serveTestRequest(t *testing.T, serverURL string, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	// The request body is already formed; use the configured test server's URL.
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	recorder.Code = response.StatusCode
	recorder.Body.Write(data)
	return recorder
}
