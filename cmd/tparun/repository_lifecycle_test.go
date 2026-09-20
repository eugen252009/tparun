package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func lifecycleRequest(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRepositoryVisibilityMutationAndDeletion(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	const repo = "0123456789abcdef0123456789abcdef"
	const other = "fedcba9876543210fedcba9876543210"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repo, AccountID: "a", Name: "lifecycle", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateRepository(ctx, store.Repository{ID: other, AccountID: "a", Name: "other", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Packages"), []byte("authoritative"), 0644); err != nil {
		t.Fatal(err)
	}

	res := lifecycleRequest(t, http.MethodPatch, h.URL+"/v1/repositories/"+repo, "", `{"visibility":"public"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated visibility status=%d", res.StatusCode)
	}
	res.Body.Close()
	res = lifecycleRequest(t, http.MethodPatch, h.URL+"/v1/repositories/"+repo, "tph_s_test", `{"visibility":"visible"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid visibility status=%d", res.StatusCode)
	}
	res.Body.Close()
	res = lifecycleRequest(t, http.MethodPatch, h.URL+"/v1/repositories/"+repo, "tph_s_test", `{"visibility":"public"}`)
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("private to public status=%d body=%s", res.StatusCode, b)
	}
	res.Body.Close()
	res = lifecycleRequest(t, http.MethodGet, h.URL+"/v1/repositories/"+repo, "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public repository anonymous show status=%d", res.StatusCode)
	}
	res.Body.Close()

	res = lifecycleRequest(t, http.MethodPatch, h.URL+"/v1/repositories/"+repo, "tph_s_test", `{"visibility":"private"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public to private status=%d", res.StatusCode)
	}
	res.Body.Close()
	res = lifecycleRequest(t, http.MethodGet, h.URL+"/v1/repositories/"+repo, "", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("private repository anonymous show status=%d", res.StatusCode)
	}
	res.Body.Close()

	res = lifecycleRequest(t, http.MethodDelete, h.URL+"/v1/repositories/"+repo, "tph_s_test", "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", res.StatusCode)
	}
	res.Body.Close()
	if _, err := s.db.Repository(ctx, repo); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted repository lookup err=%v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("deleted repository root still exists: %v", err)
	}
	if _, err := s.db.Repository(ctx, other); err != nil {
		t.Fatalf("other repository affected: %v", err)
	}
}

func TestRepositoryDeletionRejectsActivePublicationAndNonOwner(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	const repo = "0123456789abcdef0123456789abcdef"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repo, AccountID: "a", Name: "busy", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateJob(ctx, store.Job{ID: "busy-job", RepositoryID: repo, State: "queued", IncomingPath: filepath.Join(s.cfg.DataDir, "incoming", "busy"), CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	res := lifecycleRequest(t, http.MethodDelete, h.URL+"/v1/repositories/"+repo, "tph_s_test", "")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("active deletion status=%d", res.StatusCode)
	}
	res.Body.Close()

	if err := s.db.CreateAccount(ctx, store.Account{ID: "b", Email: "b@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSession(ctx, store.Session{ID: "session-b", AccountID: "b", TokenPrefix: "tph_s_other"}, store.HashToken("tph_s_other")); err != nil {
		t.Fatal(err)
	}
	res = lifecycleRequest(t, http.MethodPatch, h.URL+"/v1/repositories/"+repo, "tph_s_other", `{"visibility":"public"}`)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner visibility status=%d", res.StatusCode)
	}
	res.Body.Close()

	var listed []store.Repository
	res = lifecycleRequest(t, http.MethodGet, h.URL+"/v1/repositories", "tph_s_test", "")
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&listed) != nil {
		t.Fatalf("owner list failed: %d", res.StatusCode)
	}
	res.Body.Close()
}
