package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

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
