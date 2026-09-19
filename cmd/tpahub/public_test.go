package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestInstallEndpointIsIndependentOfWorkingDirectory(t *testing.T) {
	_, h := testServer(t)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(h.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || len(body) == 0 || !bytes.Contains(body, []byte("#!/usr/bin/env bash")) || !bytes.Contains(body, []byte("https://tpa.run")) || bytes.Contains(body, []byte("tpa.lupricht.net")) {
		t.Fatalf("cwd-independent installer response: status=%d bytes=%d", response.StatusCode, len(body))
	}
}

func TestPublicBootstrapAndVersionEndpoints(t *testing.T) {
	s, h := testServer(t)
	r, e := http.Get(h.URL + "/install.sh")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	source, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapChecksum := regexp.MustCompile(`(?m)^RELEASE_PACKAGE_SHA256=\$\{[^}]+:-([a-f0-9]{64})\}$`).FindSubmatch(source)
	if len(bootstrapChecksum) != 2 {
		t.Fatal("installer must define one pinned bootstrap package checksum")
	}
	rootSource, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rootSource, source) {
		t.Fatal("root installer must match the embedded installer source")
	}
	if r.StatusCode != 200 || string(b) != string(source) || !strings.Contains(string(b), "tpahub-release_0.1.2_all.deb") || !strings.Contains(string(b), string(bootstrapChecksum[1])) {
		t.Fatalf("installer status/content: %d", r.StatusCode)
	}
	r, e = http.Get(h.URL + "/tpahub.asc")
	if e != nil {
		t.Fatal(e)
	}
	k, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.HasPrefix(string(k), "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatalf("key status/content: %d", r.StatusCode)
	}
	r, e = http.Get(h.URL + "/version")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Service   string `json:"service"`
		Version   string `json:"version"`
		Revision  string `json:"revision"`
		StartedAt string `json:"started_at"`
	}
	json.NewDecoder(r.Body).Decode(&v)
	r.Body.Close()
	if r.StatusCode != 200 || v.Service != "tpahub" || v.Version == "" || v.Revision == "" {
		t.Fatalf("version: %+v", v)
	}
	started := s.startedAt
	parsed, e := time.Parse(time.RFC3339, v.StartedAt)
	if e != nil || !parsed.Equal(started.Truncate(time.Second)) {
		t.Fatalf("started_at: %s %v", v.StartedAt, e)
	}
	r, _ = http.Get(h.URL + "/version")
	var v2 struct {
		StartedAt string `json:"started_at"`
	}
	json.NewDecoder(r.Body).Decode(&v2)
	r.Body.Close()
	if v2.StartedAt != v.StartedAt {
		t.Fatal("started_at changed")
	}
}
