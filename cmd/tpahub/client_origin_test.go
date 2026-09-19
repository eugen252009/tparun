package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func clientTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TPAHUB_SERVER", "")
	t.Setenv("TPAHUB_EMAIL", "origin@example.invalid")
	t.Setenv("TPAHUB_PASSWORD", "origin-test-password")
}
func runClient(t *testing.T, args ...string) {
	t.Helper()
	if err := cli(config.Config{}, args); err != nil {
		t.Fatalf("client %s: %v", args[0], err)
	}
}
func TestClientOriginCredentialsAndLegacy(t *testing.T) {
	clientTestHome(t)
	a, ah := testServer(t)
	a.cfg.RegistrationEnabled = true
	b, bh := testServer(t)
	b.cfg.RegistrationEnabled = true
	runClient(t, "register", "--server", ah.URL)
	ca := loadCLI()
	runClient(t, "register", "--server", bh.URL)
	cb := loadCLI()
	if ca.Token == "" || cb.Token == "" || ca.Token == cb.Token {
		t.Fatal("distinct issued credentials required")
	}
	for _, tc := range []struct{ url, token string }{{ah.URL, ca.Token}, {bh.URL, cb.Token}, {ah.URL, ca.Token}} {
		got := loadCLI().selectServer(tc.url)
		if got.Token != tc.token {
			t.Fatal("wrong origin credential")
		}
		_ = cli(config.Config{}, []string{"status", "job", "--server", tc.url})
	}
	var leaked atomic.Bool
	var calls atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		io.WriteString(w, "[]")
	}))
	defer foreign.Close()
	_ = cli(config.Config{}, []string{"status", "job", "--server", foreign.URL})
	t.Setenv("TPAHUB_SERVER", foreign.URL)
	_ = cli(config.Config{}, []string{"status", "job"})
	t.Setenv("TPAHUB_SERVER", "")
	if calls.Load() != 2 || leaked.Load() {
		t.Fatal("foreign origin received authorization")
	}
	before, _ := os.ReadFile(configPath())
	t.Setenv("TPAHUB_PASSWORD", "wrong-password")
	if cli(config.Config{}, []string{"login", "--server", bh.URL}) == nil {
		t.Fatal("bad login succeeded")
	}
	after, _ := os.ReadFile(configPath())
	if string(before) != string(after) {
		t.Fatal("failed login changed credentials")
	}
	runClient(t, "logout", "--server", bh.URL)
	if loadCLI().selectServer(ah.URL).Token != ca.Token || loadCLI().selectServer(bh.URL).Token != "" {
		t.Fatal("logout crossed credential slots")
	}
	if _, err := a.db.AccountByToken(context.Background(), store.HashToken(ca.Token)); err != nil {
		t.Fatal("A session revoked")
	}
	if _, err := b.db.AccountByToken(context.Background(), store.HashToken(cb.Token)); err == nil {
		t.Fatal("B session not revoked")
	}
	legacy, _ := json.Marshal(map[string]string{"Server": ah.URL, "Email": ca.Email, "Token": ca.Token})
	if err := os.WriteFile(configPath(), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TPAHUB_SERVER", foreign.URL)
	runClient(t, "status", "job")
	if leaked.Load() || loadCLI().selectServer(ah.URL).Token != ca.Token {
		t.Fatal("legacy credential rebound")
	}
	t.Setenv("TPAHUB_SERVER", "")
	runClient(t, "status", "job", "--server", foreign.URL)
	if leaked.Load() {
		t.Fatal("flag rebound legacy credential")
	}
	unbound, _ := json.Marshal(map[string]string{"Token": ca.Token})
	os.WriteFile(configPath(), unbound, 0600)
	c := loadCLI().selectServer(foreign.URL)
	if c.Token != "" || c.state.Unbound == nil {
		t.Fatal("ambiguous token selected or lost")
	}
	if err := saveCLI(c); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(configPath())
	dir, _ := os.Stat(filepath.Dir(configPath()))
	if info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("client state is not private")
	}
}
func TestClientCanonicalOrigins(t *testing.T) {
	for raw, want := range map[string]string{"https://HUB.example:443/": "https://hub.example", "https://hub.example:8443": "https://hub.example:8443", "https://sub.hub.example": "https://sub.hub.example", "http://localhost:80": "http://localhost", "https://localhost": "https://localhost", "http://[::1]:80/": "http://[::1]"} {
		got, err := canonicalServer(raw)
		if err != nil || got != want {
			t.Errorf("canonical origin %q: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"http://remote.example", "https://user:secret@hub.example", "https://hub.example/path", "https://hub.example?x=1", "https://hub.example#fragment", "https://hub.example:", "https://hub.example:0"} {
		if _, err := canonicalServer(raw); err == nil {
			t.Error("invalid origin accepted")
		}
	}
}

// Real request helpers are used. The recording destination observes only whether
// anything arrived; no credentials or bodies are printed.
func TestClientRedirectSecrets(t *testing.T) {
	var arrivals atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivals.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer target.Close()
	for _, code := range []int{302, 307, 308} {
		for _, kind := range []string{"bearer", "login", "register", "publication"} {
			t.Run(http.StatusText(code)+kind, func(t *testing.T) {
				src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", target.URL)
					w.WriteHeader(code)
				}))
				defer src.Close()
				var err error
				if kind == "login" || kind == "register" {
					var out any
					err = postJSON(src.URL, map[string]string{"password": "test-secret"}, &out)
				} else if kind == "bearer" {
					err = getPrint(src.URL, "test-token")
				} else {
					req, _ := http.NewRequest("POST", src.URL, strings.NewReader("private-package"))
					req.Header.Set("Authorization", "Bearer test-token")
					_, err = doCLIRequest(req)
				}
				if err == nil || arrivals.Load() != 0 {
					t.Fatal("cross-port redirect not blocked")
				}
			})
		}
	}
	// Production CheckRedirect predicate with verified TLS, including arbitrary
	// hostnames routed to a local TLS server by the test transport.
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 308) }))
	defer tlsServer.Close()
	old := http.DefaultClient
	http.DefaultClient = tlsServer.Client()
	defer func() { http.DefaultClient = old }()
	if err := getPrint(tlsServer.URL, "test-token"); err == nil || arrivals.Load() != 0 {
		t.Fatal("TLS downgrade followed")
	}
	http.DefaultClient = old
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/done", 307)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("same origin lost credential")
		}
		io.WriteString(w, "{}")
	}))
	defer same.Close()
	if err := getPrint(same.URL, "test-token"); err != nil {
		t.Fatal(err)
	}
	// Host/subdomain redirects are rejected before attempting DNS or transport.
	for _, destination := range []string{"https://other.example/", "https://sub.hub.example/"} {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "hub.example" {
				t.Fatal("redirect reached transport")
			}
			return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{destination}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		http.DefaultClient = &http.Client{Transport: transport}
		if err := getPrint("https://hub.example/", "test-token"); err == nil {
			t.Fatal("host redirect followed")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
