package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthenticatedJSONRequestRenewsExpiredSSHSessionOnce(t *testing.T) {
	var requests, renewals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "Bearer old-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer replacement-session" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	}))
	defer server.Close()
	old := sshReauthenticate
	defer func() { sshReauthenticate = old }()
	sshReauthenticate = func(c cliConfig) (cliConfig, error) {
		renewals.Add(1)
		if c.IdentityFile != "/tmp/intended-key" {
			t.Fatalf("identity path=%q", c.IdentityFile)
		}
		c.Token = "replacement-session"
		return c, nil
	}
	var out map[string]string
	if err := authenticatedJSONRequest(cliConfig{Server: server.URL, Token: "old-session", Email: "agent@example.invalid", IdentityFile: "/tmp/intended-key"}, http.MethodGet, server.URL, nil, &out); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || renewals.Load() != 1 || out["ok"] != "yes" {
		t.Fatalf("requests=%d renewals=%d out=%v", requests.Load(), renewals.Load(), out)
	}
}

func TestAuthenticatedJSONRequestDoesNotRenewAuthorizationFailure(t *testing.T) {
	var renewals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", http.StatusForbidden) }))
	defer server.Close()
	old := sshReauthenticate
	defer func() { sshReauthenticate = old }()
	sshReauthenticate = func(c cliConfig) (cliConfig, error) { renewals.Add(1); return c, nil }
	if err := authenticatedJSONRequest(cliConfig{Server: server.URL, Token: "session", IdentityFile: "/tmp/intended-key"}, http.MethodGet, server.URL, nil, nil); err == nil || renewals.Load() != 0 {
		t.Fatalf("err=%v renewals=%d", err, renewals.Load())
	}
}

func TestAuthenticatedJSONRequestReturnsSecondAuthenticationFailure(t *testing.T) {
	var requests, renewals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer server.Close()
	old := sshReauthenticate
	defer func() { sshReauthenticate = old }()
	sshReauthenticate = func(c cliConfig) (cliConfig, error) { renewals.Add(1); c.Token = "replacement"; return c, nil }
	err := authenticatedJSONRequest(cliConfig{Server: server.URL, Token: "old", IdentityFile: "/tmp/intended-key"}, http.MethodGet, server.URL, nil, nil)
	if err == nil || requests.Load() != 2 || renewals.Load() != 1 {
		t.Fatalf("err=%v requests=%d renewals=%d", err, requests.Load(), renewals.Load())
	}
}

func TestAuthenticatedJSONRequestRedactsAuthenticationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token=tph_s_secret", http.StatusUnauthorized)
	}))
	defer server.Close()
	old := sshReauthenticate
	defer func() { sshReauthenticate = old }()
	sshReauthenticate = func(c cliConfig) (cliConfig, error) { return c, nil }
	err := authenticatedJSONRequest(cliConfig{Server: server.URL, Token: "old", IdentityFile: "/tmp/intended-key"}, http.MethodGet, server.URL, nil, nil)
	if err == nil || strings.Contains(err.Error(), "tph_s_secret") {
		t.Fatalf("unredacted error: %v", err)
	}
}
