package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestListCLIShowsRepositoriesForLoggedInAccount(t *testing.T) {
	clientTestHome(t)
	s, h := testServer(t)
	ctx := context.Background()
	for _, repository := range []store.Repository{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AccountID: "a", Name: "private-repo", Visibility: "private", CreatedAt: "now"},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AccountID: "a", Name: "public-repo", Visibility: "public", CreatedAt: "now"},
	} {
		if err := s.db.CreateRepository(ctx, repository); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.CreateAccount(ctx, store.Account{ID: "other", Email: "other@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "cccccccccccccccccccccccccccccccc", AccountID: "other", Name: "not-owned", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}

	client := loadCLIForServer(h.URL)
	client.Email = "a@example.invalid"
	client.Token = "tph_s_test"
	if err := saveCLI(client); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	cliErr := cli(config.Config{}, []string{"list"})
	_ = write.Close()
	os.Stdout = oldStdout
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if cliErr != nil || readErr != nil {
		t.Fatalf("list command failed: %v/%v", cliErr, readErr)
	}
	text := string(output)
	for _, want := range []string{
		"ID\tVISIBILITY\tURL",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tprivate\t" + h.URL + "/r/a/private-repo",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\tpublic\t" + h.URL + "/r/a/public-repo",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("list output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "not-owned") || strings.Contains(text, "cccccccccccccccccccccccccccccccc") {
		t.Fatalf("list exposed another account's repository:\n%s", text)
	}
}

func TestListCLIRequiresLoginAndNoOperands(t *testing.T) {
	clientTestHome(t)
	_, h := testServer(t)
	t.Setenv("TPARUN_SERVER", h.URL)
	if err := cli(config.Config{}, []string{"list"}); err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("unauthenticated list error=%v", err)
	}
	if err := listRepositoriesCLI(cliConfig{Server: h.URL, Token: "tph_s_test"}, []string{"unexpected"}); err == nil || err.Error() != "usage: tparun list" {
		t.Fatalf("operand validation error=%v", err)
	}
}

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
	if !strings.Contains(string(output), "/etc/apt/auth.conf.d/tparun-"+repo) {
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
	if !strings.Contains(text, "/etc/apt/keyrings/tparun-"+repo+".gpg") || !strings.Contains(text, "/etc/apt/sources.list.d/tparun-"+repo+".list") {
		t.Fatalf("dry-run omitted target files: %s", text)
	}
}
