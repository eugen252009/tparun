package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
)

func TestPublicationSessionPublishes1000DistinctPackagesEndToEnd(t *testing.T) {
	bin := buildCurrentTPA(t)
	signing := testCandidateSigningConfig(t)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	s, h := configuredTestServer(t, func(s *server) {
		s.cfg.GPGHome, s.cfg.GPGFingerprint = signing.GPGHome, signing.GPGFingerprint
		s.cfg.MaxUpload = 1 << 20
		s.cfg.MaxStagingBytes = 1 << 30
		s.cfg.MaxAccountStagingBytes = 1 << 30
		s.cfg.MaxCandidateInodes = 65536
		s.cfg.MinDiskFreeBytes = 0
		s.cfg.MinFreeInodes = 0
		s.cfg.PublishTimeout = 5 * time.Minute
		s.cfg.MaxGenerations = 2
		s.verifyCandidate = nil
		s.runner = tpa.Runner{Bin: bin, GPGHome: signing.GPGHome, Fingerprint: signing.GPGFingerprint}
		s.ctx = workerCtx
	})
	defer h.Close()
	const repositoryID = "bdbdbdbdbdbdbdbdbdbdbdbdbdbdbdbd"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "large-session", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}

	const count = 1000
	staging := filepath.Join(t.TempDir(), "staging")
	t.Setenv("TPARUN_STAGING_DIR", staging)
	if err := os.MkdirAll(stagingArtifactsRoot(), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := stagingManifest{Version: 1, Artifacts: make([]stagedArtifact, 0, count)}
	for i := 0; i < count; i++ {
		packageName := fmt.Sprintf("tparun-large-%04d", i)
		source := filepath.Join(t.TempDir(), packageName+"_1.0.0_all.deb")
		buildWorkerTestDeb(t, source, packageName)
		digest, err := fileSHA256(source)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		stagedPath := filepath.Join(stagingArtifactsRoot(), digest+".deb")
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(stagedPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, stagedArtifact{OriginalPath: source, StagedPath: stagedPath, Filename: filepath.Base(source), Package: packageName, Version: "1.0.0", Architecture: "all", Size: info.Size(), SHA256: digest, StagedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	if err := saveStagingManifest(manifest); err != nil {
		t.Fatal(err)
	}

	var uploadedHashes int
	s.publicationHashObserve = func(int64) { uploadedHashes++ }
	workerDone := make(chan string, 1)
	go func() {
		deadline := time.NewTimer(10 * time.Minute)
		defer deadline.Stop()
		for {
			job, err := s.db.Claim(context.Background())
			if err == nil {
				s.process(job)
				workerDone <- job.ID
				return
			}
			select {
			case <-workerCtx.Done():
				return
			case <-deadline.C:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe
	publishErr := publishStaged(cliConfig{Server: h.URL, Token: "tph_s_test"}, repositoryID, true, false)
	os.Stdout = oldStdout
	_ = writePipe.Close()
	output, readErr := io.ReadAll(readPipe)
	_ = readPipe.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	jobID := ""
	select {
	case jobID = <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("publication worker did not finish")
	}
	if uploadedHashes != count {
		t.Fatalf("server hashed %d newly uploaded files, want %d", uploadedHashes, count)
	}
	totalBatches := (count + maxUploadFiles - 1) / maxUploadFiles
	for batch := 1; batch <= totalBatches; batch++ {
		if !strings.Contains(string(output), fmt.Sprintf("Uploading batch %d/%d ... accepted", batch, totalBatches)) {
			t.Fatalf("CLI progress omitted batch %d/%d", batch, totalBatches)
		}
	}
	finished, err := s.db.Job(context.Background(), jobID)
	if err != nil || finished.State != "succeeded" {
		t.Fatalf("large publication job=%+v err=%v", finished, err)
	}
	artifacts, err := s.db.PublicationArtifacts(context.Background(), jobID)
	if err != nil || len(artifacts) != count {
		t.Fatalf("final job contains %d artifacts, want %d: %v", len(artifacts), count, err)
	}
	seen := make(map[string]bool, count)
	for _, artifact := range artifacts {
		seen[artifact.Package] = true
	}
	if len(seen) != count {
		t.Fatalf("final job has %d distinct package identities, want %d", len(seen), count)
	}
	usage, err := s.repositoryUsage(repositoryID)
	if err != nil || usage.PackageCount != int64(count) {
		t.Fatalf("activated repository packages=%d want=%d err=%v", usage.PackageCount, count, err)
	}
	active, err := s.db.ActiveGeneration(context.Background(), repositoryID)
	if err != nil || active != jobID {
		t.Fatalf("active generation=%q want=%s err=%v", active, jobID, err)
	}
	staged, err := loadStagingManifest()
	if err != nil || len(staged.Artifacts) != 0 {
		t.Fatalf("successful publication did not clear local staging: files=%d err=%v", len(staged.Artifacts), err)
	}
}
