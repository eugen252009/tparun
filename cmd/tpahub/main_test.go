package main

import (
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
	s := &server{cfg: config.Config{DataDir: d, MaxUpload: 1024}, db: db, runner: tpa.Runner{}}
	h := httptest.NewServer(s.routes())
	t.Cleanup(func() { h.Close(); db.Close() })
	return s, h
}
func TestHTTPRepositoryAndServing(t *testing.T) {
	s, h := testServer(t)
	resp, e := http.Post(h.URL+"/v1/repositories", "application/json", strings.NewReader(`{"name":"demo"}`))
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
	if resp, e = http.Get(h.URL + "/v1/repositories/" + id); e != nil || resp.StatusCode != 200 {
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
	if resp.StatusCode != 404 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
