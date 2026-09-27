package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestPublicationSessionIncrementalBatchesHashOnlyNewFilesAndQueueOnce(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.MaxUpload = 1 << 20
	s.cfg.MaxStagingBytes = 1 << 30
	s.cfg.MinDiskFreeBytes = 0
	const repositoryID = "bcbcbcbcbcbcbcbcbcbcbcbcbcbcbcbc"
	const sessionID = "dededededededededededededededede"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "incremental", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}

	files := make([]string, 65)
	expected := make([]store.PublicationArtifact, len(files))
	for i := range files {
		packageName := fmt.Sprintf("tparun-session-%03d", i)
		files[i] = filepath.Join(t.TempDir(), packageName+"_1.0.0_all.deb")
		buildWorkerTestDeb(t, files[i], packageName)
		identity, err := debIdentity(context.Background(), files[i])
		if err != nil {
			t.Fatal(err)
		}
		digest, err := fileSHA256(files[i])
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(files[i])
		if err != nil {
			t.Fatal(err)
		}
		expected[i] = store.PublicationArtifact{Filename: filepath.Base(files[i]), Package: packageName, Version: "1.0.0", Architecture: "all", UploadedSize: info.Size(), UploadedSHA256: digest}
		if identity != packageName+"\x001.0.0\x00all" {
			t.Fatalf("unexpected fixture identity %q", identity)
		}
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", sessionID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.CreatePublicationSession(context.Background(), store.PublicationSession{ID: sessionID, RepositoryID: repositoryID, OwnerAccountID: "a", IncomingPath: incoming, ExpectedCount: int64(len(expected)), CreatedAt: now}, expected); err != nil {
		t.Fatal(err)
	}
	var hashCount int
	var hashBytes int64
	s.publicationHashObserve = func(size int64) { hashCount++; hashBytes += size }

	uploadBatch := func(batch int, selected []string) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, path := range selected {
			part, err := writer.CreateFormFile("files", filepath.Base(path))
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(part, file)
			_ = file.Close()
			if copyErr != nil {
				t.Fatal(copyErr)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/repositories/%s/publishes?session=%s&batch=%d", repositoryID, sessionID, batch), &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request = request.WithContext(context.WithValue(request.Context(), accountKey, "a"))
		response := httptest.NewRecorder()
		s.upload(response, request, repositoryID)
		return response
	}

	first := uploadBatch(0, files[:64])
	if first.Code != http.StatusAccepted {
		t.Fatalf("first batch status=%d body=%s", first.Code, first.Body.String())
	}
	if _, err := s.db.Job(context.Background(), sessionID); err == nil {
		t.Fatal("a transport batch created a publication job before finalization")
	}
	if hashCount != 64 {
		t.Fatalf("after batch one hashed files=%d, want 64", hashCount)
	}
	second := uploadBatch(1, files[64:])
	if second.Code != http.StatusAccepted {
		t.Fatalf("second batch status=%d body=%s", second.Code, second.Body.String())
	}
	if hashCount != 65 {
		t.Fatalf("second batch rehashed prior files: observed %d new-file hashes, want 65 total", hashCount)
	}
	if hashBytes <= 0 {
		t.Fatal("hash-byte observer saw no payload bytes")
	}
	if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current")); !os.IsNotExist(err) {
		t.Fatalf("repository activated before finalization: err=%v", err)
	}
	var ack struct {
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &ack); err != nil || ack.Replayed {
		t.Fatalf("first batch response=%s err=%v", first.Body.String(), err)
	}
	replayResponse := uploadBatch(0, files[:64])
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("replayed batch status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &ack); err != nil || !ack.Replayed {
		t.Fatalf("identical retry was not acknowledged as replay: %s err=%v", replayResponse.Body.String(), err)
	}
	if hashCount != 129 {
		t.Fatalf("retry hash observations=%d, want 129 including 64 retransmitted files", hashCount)
	}

	if err := s.db.FinalizePublicationSession(context.Background(), sessionID, "a", now); err != nil {
		t.Fatal(err)
	}
	job, err := s.db.Job(context.Background(), sessionID)
	if err != nil || job.State != "queued" {
		t.Fatalf("final job=%+v err=%v", job, err)
	}
	artifacts, err := s.db.PublicationArtifacts(context.Background(), sessionID)
	if err != nil || len(artifacts) != len(files) {
		t.Fatalf("final job artifacts=%d err=%v", len(artifacts), err)
	}
	if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "repos", repositoryID, "current")); !os.IsNotExist(err) {
		t.Fatalf("queued complete job activated before worker processing: err=%v", err)
	}
}
