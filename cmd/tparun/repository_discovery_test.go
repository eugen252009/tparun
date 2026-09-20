package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveRepositoryPreservesIDsAndResolvesPublicNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/public/repositories" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"0123456789abcdef0123456789abcdef","name":"bloom-public","visibility":"public"}]`))
	}))
	defer server.Close()
	config := cliConfig{Server: server.URL}
	if got, err := resolveRepository(config, "bloom-public"); err != nil || got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("name resolution got %q, %v", got, err)
	}
	if got, err := resolveRepository(config, "0123456789abcdef0123456789abcdef"); err != nil || got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("ID resolution got %q, %v", got, err)
	}
}

func TestResolveRepositoryRejectsUnknownAndAmbiguousNames(t *testing.T) {
	for _, test := range []struct{ response, name string }{
		{`[{"id":"0123456789abcdef0123456789abcdef","name":"other","visibility":"public"}]`, "missing"},
		{`[{"id":"0123456789abcdef0123456789abcdef","name":"same","visibility":"public"},{"id":"fedcba9876543210fedcba9876543210","name":"same","visibility":"public"}]`, "same"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(test.response))
		}))
		_, err := resolveRepository(cliConfig{Server: server.URL}, test.name)
		server.Close()
		if err == nil {
			t.Fatalf("response %s unexpectedly resolved", test.response)
		}
	}
}
