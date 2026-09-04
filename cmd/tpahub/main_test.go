package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
)

func testServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	d := t.TempDir()
	db, e := store.Open(d)
	if e != nil {
		t.Fatal(e)
	}
	_ = db.CreateAccount(context.Background(), store.Account{ID: "a", Email: "a@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2024-01-01T00:00:00Z"}, defaultTPAGrants("2024-01-01T00:00:00Z")...)
	_ = db.CreateSession(context.Background(), store.Session{ID: "s", AccountID: "a", TokenPrefix: "tph_s_test"}, store.HashToken("tph_s_test"))
	s := &server{cfg: config.Config{DataDir: d, MaxUpload: 1024, RegistrationEnabled: true}, db: db, runner: tpa.Runner{}}
	h := httptest.NewServer(s.routes())
	t.Cleanup(func() { h.Close(); db.Close() })
	return s, h
}
func TestPublicationCandidateMustBeComplete(t *testing.T) {
	dir := t.TempDir()
	if err := verifyPublicationCandidate(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing candidate accepted")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0755); err != nil {
		t.Fatal(err)
	}
	if err := verifyPublicationCandidate(empty); err == nil {
		t.Fatal("empty candidate accepted")
	}
	if err := os.WriteFile(filepath.Join(empty, "Packages"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyPublicationCandidate(empty); err != nil {
		t.Fatalf("complete candidate rejected: %v", err)
	}
}

func TestRepositoryGenerationsUseActivePointer(t *testing.T) {
	s, _ := testServer(t)
	root := filepath.Join(s.cfg.DataDir, "repos", "generation-test")
	generation := filepath.Join(root, "generations", "job-1")
	if err := os.MkdirAll(generation, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "Packages"), []byte("generation-1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "job-1"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if got := s.activeRepositoryRoot(root); got != filepath.Join(root, "current") {
		t.Fatalf("active root=%q", got)
	}
	data, err := os.ReadFile(filepath.Join(s.activeRepositoryRoot(root), "Packages"))
	if err != nil || string(data) != "generation-1" {
		t.Fatalf("active generation data=%q err=%v", data, err)
	}
}

func TestSuspendedAccountCannotRunQueuedPublication(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	repositoryID := "suspended-publish"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repositoryID, AccountID: "a", Name: "suspended-publish", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetAccountStatus(ctx, "a", "suspended"); err != nil {
		t.Fatal(err)
	}
	if err := s.ensurePublishAccountActive(repositoryID); err == nil {
		t.Fatal("suspended account was allowed to publish")
	}
}

func TestHTTPRepositoryAndServing(t *testing.T) {
	s, h := testServer(t)
	req, _ := http.NewRequest("POST", h.URL+"/v1/repositories", strings.NewReader(`{"name":"demo","visibility":"public"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tph_s_test")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	var r map[string]string
	json.NewDecoder(resp.Body).Decode(&r)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	id := r["id"]
	if _, e = os.Stat(filepath.Join(s.cfg.DataDir, "repos", id)); e != nil {
		t.Fatal(e)
	}
	req, _ = http.NewRequest("GET", h.URL+"/v1/repositories/"+id, nil)
	req.Header.Set("Authorization", "Bearer tph_s_test")
	resp, e = http.DefaultClient.Do(req)
	if e != nil || resp.StatusCode != 200 {
		t.Fatalf("lookup: %v %v", resp, e)
	}
	os.WriteFile(filepath.Join(s.cfg.DataDir, "repos", id, "file"), []byte("ok"), 0644)
	resp, e = http.Get(h.URL + "/r/" + id + "/file")
	if e != nil || resp.StatusCode != 200 {
		t.Fatalf("file: %v %v", resp, e)
	}
	resp, e = http.Get(h.URL + "/r/" + id + "/../tpahub.db")
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode == 200 {
		t.Fatal("path escaped repository")
	}
}
func TestHTTPInvalidUpload(t *testing.T) {
	_, h := testServer(t)
	resp, _ := http.Post(h.URL+"/v1/repositories/not-an-id/publishes?filename=x.deb", "application/octet-stream", strings.NewReader("x"))
	if resp.StatusCode != 401 && resp.StatusCode != 404 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
