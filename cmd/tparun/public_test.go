package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	appversion "github.com/eugen252009/tpahub/internal/version"
)

func TestSourcesEndpointIsIndependentOfWorkingDirectory(t *testing.T) {
	_, h := testServer(t)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(h.URL + "/tparun.sources")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || len(body) == 0 || !bytes.Contains(body, []byte("Types: deb")) || !bytes.Contains(body, []byte("Signed-By:")) || !bytes.Contains(body, []byte("https://tpa.run")) {
		t.Fatalf("cwd-independent installer response: status=%d bytes=%d", response.StatusCode, len(body))
	}
}

func TestPublicBootstrapAndVersionEndpoints(t *testing.T) {
	s, h := testServer(t)
	r, e := http.Get(h.URL + "/tparun.sources")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	source, err := os.ReadFile("tparun.sources")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 || !bytes.Equal(b, source) || !bytes.Contains(b, []byte("Signed-By:")) {
		t.Fatalf("sources status/content: %d", r.StatusCode)
	}
	r, e = http.Get(h.URL + "/version")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Service   string `json:"service"`
		Version   string `json:"version"`
		Revision  string `json:"revision"`
		Schema    string `json:"schema"`
		StartedAt string `json:"started_at"`
	}
	json.NewDecoder(r.Body).Decode(&v)
	r.Body.Close()
	if r.StatusCode != 200 || v.Service != "tparun" || v.Version != appversion.Current || v.Revision == "" || v.Schema != strconv.Itoa(store.CurrentSchemaVersion()) {
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
