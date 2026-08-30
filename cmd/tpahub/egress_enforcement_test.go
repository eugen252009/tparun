package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestEgressGracefulPackageLimitingAndMonthlyReset(t *testing.T) {
	s, h := testServer(t)
	clock := time.Date(2026, 8, 31, 23, 59, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	id := "55555555555555555555555555555555"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: "limited", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", id)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), make([]byte, 1000), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Packages.gz"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryEgress(context.Background(), id, "2026-08", NanoEgressBytes-1000, "now"); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *http.Response {
		response, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := get("/r/" + id + "/package.deb")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || len(body) != 1000 || response.Header.Get("X-TPAHub-Egress-Limited") != "" {
		t.Fatalf("threshold-crossing response status=%d bytes=%d limited=%q", response.StatusCode, len(body), response.Header.Get("X-TPAHub-Egress-Limited"))
	}
	response = get("/r/" + id + "/package.deb")
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || len(body) != 1000 || response.Header.Get("X-TPAHub-Egress-Limited") != "true" {
		t.Fatalf("limited response status=%d bytes=%d limited=%q", response.StatusCode, len(body), response.Header.Get("X-TPAHub-Egress-Limited"))
	}
	response = get("/r/" + id + "/Packages.gz")
	response.Body.Close()
	if response.Header.Get("X-TPAHub-Egress-Limited") != "" {
		t.Fatal("metadata was rate-limited")
	}
	if got, err := s.db.RepositoryEgress(context.Background(), id, "2026-08"); err != nil || got != NanoEgressBytes+1100 {
		t.Fatalf("August egress=%d, %v", got, err)
	}
	clock = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	response = get("/r/" + id + "/package.deb")
	response.Body.Close()
	if response.Header.Get("X-TPAHub-Egress-Limited") != "" {
		t.Fatal("new UTC month remained limited")
	}
	if got, err := s.db.RepositoryEgress(context.Background(), id, "2026-09"); err != nil || got != 1000 {
		t.Fatalf("September egress=%d, %v", got, err)
	}
}
