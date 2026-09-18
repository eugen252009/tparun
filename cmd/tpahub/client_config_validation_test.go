package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
)

func TestClientConfigValidationAndOverridePrecedence(t *testing.T) {
	clientTestHome(t)
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected authorization")
		}
		w.Write([]byte("[]"))
	}))
	defer h.Close()
	t.Setenv("TPAHUB_SERVER", "not-an-origin")
	runClient(t, "repo", "list", "--server", h.URL)
	for _, args := range [][]string{{"repo", "list", "--server"}, {"repo", "list", "--server="}, {"--server", h.URL}} {
		if cli(config.Config{}, args) == nil {
			t.Fatal("incomplete arguments accepted")
		}
	}
	os.MkdirAll(filepath.Dir(configPath()), 0700)
	os.WriteFile(configPath(), []byte("invalid JSON"), 0600)
	if cli(config.Config{}, []string{"repo", "list", "--server", h.URL}) == nil {
		t.Fatal("malformed configuration ignored")
	}
	runClient(t, "version")
	runClient(t, "schema")
}
