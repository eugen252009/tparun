package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryResolutionUsesImmutableIDsAndExplicitScope(t *testing.T) {
	a := strings.Repeat("a", 32)
	b := strings.Repeat("b", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/repositories":
			if r.Header.Get("Authorization") != "Bearer owner" {
				t.Error("owned lookup missing scoped credential")
			}
			fmt.Fprintf(w, `[{"id":%q,"name":"private"},{"id":%q,"name":"shared"}]`, a, a)
		case "/v1/public/repositories":
			if r.Header.Get("Authorization") != "" {
				t.Error("public lookup received publisher credential")
			}
			fmt.Fprintf(w, `[{"id":%q,"name":"public"},{"id":%q,"name":"shared"}]`, b, b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := cliConfig{Server: server.URL, Token: "owner"}
	for _, tc := range []struct {
		name  string
		owner bool
		want  string
	}{
		{"private", false, a}, {"private", true, a}, {"public", false, b}, {"shared", true, a}, {a, true, a}, {b, false, b},
	} {
		got, err := resolveRepositoryInScope(c, tc.name, tc.owner)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
	if _, err := resolveRepository(c, "shared"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous read scope: %v", err)
	}
	if _, err := resolveRepositoryInScope(c, "public", true); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("foreign mutation name resolved: %v", err)
	}
	if _, err := resolveRepository(cliConfig{Server: server.URL}, "private"); err == nil {
		t.Fatal("private name anonymously discovered")
	}
}

func TestRepositoryResolutionDeduplicatesOwnedPublicIdentity(t *testing.T) {
	id := strings.Repeat("a", 32)
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `[{"id":%q,"name":"mine"}]`, id) }))
	defer h.Close()
	got, err := resolveRepository(cliConfig{Server: h.URL, Token: "owner"}, "mine")
	if err != nil || got != id {
		t.Fatalf("same identity considered ambiguous: %q %v", got, err)
	}
}

func TestAPTAtomicCredentialReplacementRestrictsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.conf")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if err := writeAPTFile(path, []byte("new-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new-secret" || info.Mode().Perm() != 0600 {
		t.Fatalf("replacement: %s %o %v", data, info.Mode().Perm(), err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary credential file leaked")
	}
}
