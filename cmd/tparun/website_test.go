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
	"time"

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

func TestAccountEgressStatusMatchesEnforcementThreshold(t *testing.T) {
	for _, tc := range []struct {
		name string
		used int64
		want string
	}{
		{"below", NanoEgressBytes - 1, "Package delivery is currently unrestricted"},
		{"at", NanoEgressBytes, "Annual egress allowance reached"},
		{"over", NanoEgressBytes + 1, "Annual egress allowance exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := testServer(t)
			now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }

			repositoryID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
			if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "egress-ui", Visibility: "public", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.AddRepositoryEgressDaily(context.Background(), repositoryID, "2026-08-15", tc.used, "2026-08-15T12:00:00Z"); err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodGet, h.URL+"/account", nil)
			request.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_test"})
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK || !strings.Contains(string(body), tc.want) {
				t.Fatalf("status=%d body missing %q", response.StatusCode, tc.want)
			}
		})
	}

	t.Run("available", func(t *testing.T) {
		_, h := testServer(t)
		// Invalid compatibility history cannot make current quota resolution
		// unavailable; product_grants remain authoritative.

		request, _ := http.NewRequest(http.MethodGet, h.URL+"/account", nil)
		request.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_test"})
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Package delivery is currently unrestricted") {
			t.Fatalf("unexpected status=%d", response.StatusCode)
		}
	})
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
				wants = append(wants, "Bootstrap package SHA-256", "Build, publish, install", "Get Started", "tparun-bootstrap.deb", "/docs#install")
			} else if path == "/nano" {
				wants = append(wants, "5 MB", "12 GB", "2,500 packages", "Illustration only", "Recipes are optional", "not currently available upgrades")
			} else if path == "/pricing" {
				wants = append(wants, "Standard", "Pro", "Same core capabilities", "5 MB", "12 GB", "500 MB", "20 GB", "Pay as you go", "storage blocks", "egress blocks", "Current plan", "Coming soon", "not an active live purchase offer")
			} else {
				wants = append(wants, "id=\"consume\"", "id=\"publish\"", "signed-by", "anniversary-based entitlement year", "Actual delivered", "live usage-based charging", "future spend controls", "test-only")
			}
			for _, want := range wants {
				if !strings.Contains(text, want) {
					t.Fatalf("website page %s missing %q", path, want)
				}
			}
			if path == "/pricing" && (strings.Contains(text, "Buy Pro") || strings.Contains(text, "Buy Standard")) {
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
	req, _ := http.NewRequest("GET", h.URL+"/account", nil)
	req.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_test"})
	accountResponse, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	accountBody, _ := io.ReadAll(accountResponse.Body)
	accountResponse.Body.Close()
	accountText := string(accountBody)
	if accountResponse.StatusCode != 200 ||
		!strings.Contains(accountText, "Storage") ||
		!strings.Contains(accountText, "Storage capacity") ||
		strings.Contains(accountText, "quota enforcement is not yet active") ||
		!strings.Contains(accountText, "Egress") ||
		(!strings.Contains(accountText, "used this entitlement year") && !strings.Contains(accountText, "observed locally")) ||
		strings.Contains(accountText, "€") {
		t.Fatalf("account usage display invalid: status=%d", accountResponse.StatusCode)
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
