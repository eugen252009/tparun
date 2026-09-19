package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestParseRepositoryReference(t *testing.T) {
	for _, tc := range []struct {
		name, value, origin, id string
	}{
		{"canonical", "https://TPA.RUN/e64140bae68efd2489afd72a7330a4ad", "https://tpa.run", "e64140bae68efd2489afd72a7330a4ad"},
		{"trailing slash", "https://tpa.run/e64140bae68efd2489afd72a7330a4ad/", "https://tpa.run", "e64140bae68efd2489afd72a7330a4ad"},
		{"explicit port", "https://example.test:8443/0123456789abcdef0123456789abcdef", "https://example.test:8443", "0123456789abcdef0123456789abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, id, err := parseRepositoryReference(tc.value)
			if err != nil || origin != tc.origin || id != tc.id {
				t.Fatalf("parse=%q,%q,%v", origin, id, err)
			}
		})
	}
	for _, value := range []string{
		"https://example.test/not-an-id",
		"https://example.test/0123456789abcdef0123456789abcdef/extra",
		"https://example.test/0123456789abcdef0123456789abcdef?token=secret",
		"http://example.test/0123456789abcdef0123456789abcdef",
		"https://example.test/0123456789abcdef0123456789abcdef#fragment",
	} {
		if _, _, err := parseRepositoryReference(value); err == nil {
			t.Errorf("accepted invalid repository reference %q", value)
		}
	}
}

func TestRepoAddPublicDoesNotPrompt(t *testing.T) {
	repo := "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/repositories/" + repo:
			_, _ = io.WriteString(w, `{"id":"`+repo+`","visibility":"public"}`)
		case "/v1/repositories/" + repo + "/signing-key":
			_, _ = io.WriteString(w, "public-key")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	called := false
	oldPrompt := repositoryPasswordReader
	repositoryPasswordReader = func() (string, error) { called = true; return "unexpected", nil }
	t.Cleanup(func() { repositoryPasswordReader = oldPrompt })
	if err := repositoryCommand(cliConfig{Server: server.URL}, []string{"add", server.URL + "/" + repo, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("public repository prompted for a credential")
	}
}

func TestRepoAddPrivatePromptsWithoutPrintingSecret(t *testing.T) {
	repo := "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/repositories/" + repo:
			w.WriteHeader(http.StatusUnauthorized)
		case "/r/" + repo + "/dists/stable/InRelease":
			_, _ = io.WriteString(w, "signed metadata")
		case "/v1/repositories/" + repo + "/signing-key":
			_, _ = io.WriteString(w, "public-key")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	secret := "reader-secret-not-output"
	oldPrompt := repositoryPasswordReader
	repositoryPasswordReader = func() (string, error) { return secret, nil }
	t.Cleanup(func() { repositoryPasswordReader = oldPrompt })
	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = addRepo(cliConfig{Server: server.URL}, repo, "", true)
	_ = write.Close()
	os.Stdout = old
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if err != nil || readErr != nil {
		t.Fatalf("private dry-run failed: %v/%v", err, readErr)
	}
	if strings.Contains(string(output), secret) {
		t.Fatal("private credential appeared in dry-run output")
	}
	if !strings.Contains(string(output), "/etc/apt/auth.conf.d/tpahub-"+repo) {
		t.Fatalf("private dry-run omitted auth path: %s", output)
	}
}

func TestInvalidReaderCredentialIsNotExposed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	secret := "reader-secret-not-output"
	err := validateReaderCredential(cliConfig{Server: server.URL}, "0123456789abcdef0123456789abcdef", secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential validation error leaked or was absent: %v", err)
	}
}

func TestRepoAddDryRunDoesNotWriteAPTFiles(t *testing.T) {
	repo := "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/repositories/" + repo:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"`+repo+`","visibility":"public"}`)
		case "/v1/repositories/" + repo + "/signing-key":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "public-key")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = addRepo(cliConfig{Server: server.URL}, repo, "", true)
	_ = write.Close()
	os.Stdout = old
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if err != nil || readErr != nil {
		t.Fatalf("dry-run failed: %v/%v", err, readErr)
	}
	text := string(output)
	if !strings.Contains(text, "/etc/apt/keyrings/tpahub-"+repo+".gpg") || !strings.Contains(text, "/etc/apt/sources.list.d/tpahub-"+repo+".list") {
		t.Fatalf("dry-run omitted target files: %s", text)
	}
}
