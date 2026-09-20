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
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestZeroEgressGrantIsNotRaisedToNanoQuota(t *testing.T) {
	s, h := testServer(t)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	grant, err := s.db.GetGrant(context.Background(), "a", tpahubproduct.ProductID, CapabilityRepositoryDownload, now)
	if err != nil {
		t.Fatal(err)
	}
	grant.Limits.EgressBytes = 0
	if err = s.db.PutGrant(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: "nano-download", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", id)
	if err = os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "package.deb"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	used, limit, err := s.accountEgress("a", now)
	if err != nil || limit != 0 || used != 0 {
		t.Fatalf("persisted egress used=%d limit=%d err=%v", used, limit, err)
	}
	response, err := http.Get(h.URL + "/r/" + id + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || response.Header.Get("X-TPAHub-Egress-Limited") != "true" || string(body) != "x" {
		t.Fatalf("zero-limit download status=%d body=%q limited=%q err=%v", response.StatusCode, body, response.Header.Get("X-TPAHub-Egress-Limited"), readErr)
	}
	response, err = http.Get(h.URL + "/r/" + id + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || response.Header.Get("X-TPAHub-Egress-Limited") != "true" || string(body) != "x" {
		t.Fatalf("exhausted download status=%d body=%q limited=%q err=%v", response.StatusCode, body, response.Header.Get("X-TPAHub-Egress-Limited"), readErr)
	}
	if got, err := s.db.RepositoryEgress(context.Background(), id, "2026-08"); err != nil || got != 2 {
		t.Fatalf("settled egress=%d err=%v", got, err)
	}
}
