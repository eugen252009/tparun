package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPublicBootstrapAndVersionEndpoints(t *testing.T) {
	s, h := testServer(t)
	r, e := http.Get(h.URL + "/install.sh")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(b), "a0d8fd0f82235ee431c41581aa253ec8") {
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
