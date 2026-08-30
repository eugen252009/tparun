package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestPublishedUsageTracksCanonicalArtifacts(t *testing.T) {
	s, _ := testServer(t)
	root := filepath.Join(s.cfg.DataDir, "repos", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if e := os.MkdirAll(filepath.Join(root, "metadata"), 0755); e != nil {
		t.Fatal(e)
	}
	for _, item := range []struct {
		name string
		size int
	}{{"one.deb", 3}, {"two.DEB", 7}} {
		if e := os.WriteFile(filepath.Join(root, item.name), make([]byte, item.size), 0644); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(root, "metadata", "Packages"), []byte("metadata"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(s.cfg.DataDir, "incoming"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(s.cfg.DataDir, "incoming", "upload.deb"), []byte("incoming"), 0644); e != nil {
		t.Fatal(e)
	}
	stats, e := s.repositoryUsage("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if e != nil || stats.PackageCount != 2 || stats.StoredBytes != 10 {
		t.Fatalf("repository usage = %+v, %v", stats, e)
	}
	if e := os.Remove(filepath.Join(root, "one.deb")); e != nil {
		t.Fatal(e)
	}
	stats, e = s.repositoryUsage("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if e != nil || stats.PackageCount != 1 || stats.StoredBytes != 7 {
		t.Fatalf("after removal = %+v, %v", stats, e)
	}
}

func TestAccountUsageAggregatesRepositories(t *testing.T) {
	s, _ := testServer(t)
	for i, id := range []string{"cccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddd"} {
		if e := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: fmt.Sprintf("repo-%d", i), Visibility: "public", CreatedAt: "now"}); e != nil {
			t.Fatal(e)
		}
		root := filepath.Join(s.cfg.DataDir, "repos", id)
		if e := os.MkdirAll(root, 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(root, "package.deb"), make([]byte, i+1), 0644); e != nil {
			t.Fatal(e)
		}
	}
	usage, e := s.accountUsage(context.Background(), "a")
	if e != nil || usage.RepositoryCount != 2 || usage.PackageCount != 2 || usage.StoredBytes != 3 {
		t.Fatalf("account usage = %+v, %v", usage, e)
	}
}

func TestPublishedPackageCountTracksCanonicalArtifacts(t *testing.T) {
	s, _ := testServer(t)
	root := filepath.Join(s.cfg.DataDir, "repos", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if e := os.MkdirAll(filepath.Join(root, "metadata"), 0755); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"one.deb", "two.DEB"} {
		if e := os.WriteFile(filepath.Join(root, name), []byte("deb"), 0644); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(root, "metadata", "ignored.deb.tmp"), []byte("tmp"), 0644); e != nil {
		t.Fatal(e)
	}
	if got, e := s.publishedPackageCount(); e != nil || got != 2 {
		t.Fatalf("initial count = %d, %v", got, e)
	}
	if e := os.Remove(filepath.Join(root, "one.deb")); e != nil {
		t.Fatal(e)
	}
	if got, e := s.publishedPackageCount(); e != nil || got != 1 {
		t.Fatalf("after removal count = %d, %v", got, e)
	}
}

func TestWebsitePagesAndProtocolBoundaries(t *testing.T) {
	s, h := testServer(t)
	root := filepath.Join(s.cfg.DataDir, "repos", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if e := os.MkdirAll(filepath.Join(root, "metadata"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "package.deb"), []byte("deb"), 0644); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/", "/docs", "/how-to", "/pricing", "/nano", "/register", "/login"} {
		r, e := http.Get(h.URL + path)
		if e != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v %d", path, e, r.StatusCode)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if path == "/" || path == "/nano" || path == "/pricing" || path == "/docs" {
			text := string(body)
			wants := []string{"Nano"}
			if path == "/" {
				wants = append(wants, "Same capability. Different capacity.", "same core TPA Hub capabilities", "5 MB", "1 GB", "500 MB", "20 GB", "Pay as you go", "AVAILABLE NOW", "COMING SOON", "actual resources", "package or repository-count limits", "project-stat", ">1</strong>", "packages currently published through TPA Hub", "/pricing")
			} else if path == "/nano" {
				wants = append(wants, "Neovim 0.12.5", "about 2.4 KB", "recipe package, not Neovim itself", "2,500 recipe-sized packages", "200 times", "Your mileage may vary", "STANDARD", "PRO", "same core TPA Hub capabilities")
			} else if path == "/pricing" {
				wants = append(wants, "Standard", "Pro", "Same capability. Different capacity.", "same core TPA Hub capabilities", "5 MB", "1 GB", "500 MB", "20 GB", "Pay as you go", "storage blocks", "egress blocks", "AVAILABLE NOW", "COMING SOON")
			} else {
				wants = append(wants, "Same capability. Different capacity.", "not feature-based", "5 MB", "1 GB", "500 MB", "20 GB", "pay as you go", "future", "not implemented")
			}
			for _, want := range wants {
				if !strings.Contains(text, want) {
					t.Fatalf("website page %s missing %q", path, want)
				}
			}
			if path == "/pricing" && (strings.Contains(text, "€") || strings.Contains(text, "$") || strings.Contains(text, "Buy Pro")) {
				t.Fatal("pricing exposes an unimplemented Pro price or purchase action")
			}
		}
	}
	r, e := http.Get(h.URL + "/account")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 303 {
		t.Fatalf("account status %d", r.StatusCode)
	}
	r, e = http.Get(h.URL + "/r/invalid/InRelease")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode == 200 || strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("repository boundary status=%d type=%s", r.StatusCode, r.Header.Get("Content-Type"))
	}
}
