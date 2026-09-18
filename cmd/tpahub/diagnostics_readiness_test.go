package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReadinessTransitionsDuringDrain(t *testing.T) {
	s, _ := testServer(t)
	routes := s.routes()

	ready := httptest.NewRecorder()
	routes.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", ready.Code, ready.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(ready.Body.Bytes(), &payload); err != nil || payload["ready"] != true {
		t.Fatalf("ready payload=%s err=%v", ready.Body, err)
	}

	s.lifecycle = &serverLifecycle{}
	s.lifecycle.drain()
	draining := httptest.NewRecorder()
	routes = s.routes()
	routes.ServeHTTP(draining, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if draining.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining readiness status=%d", draining.Code)
	}

	method := httptest.NewRecorder()
	routes.ServeHTTP(method, httptest.NewRequest(http.MethodPost, "/ready", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("readiness method status=%d", method.Code)
	}
}

func TestDiagnosticsAllowlistExcludesSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TPAHUB_DATA_DIR", dir)
	t.Setenv("TPAHUB_STRIPE_TEST_SECRET", "secret-must-not-appear")
	t.Setenv("TPAHUB_STRIPE_WEBHOOK_SECRET", "webhook-must-not-appear")
	t.Setenv("TPAHUB_OPERATOR_TOKEN", "operator-must-not-appear")

	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	callErr := diagnostics()
	_ = write.Close()
	os.Stdout = old
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if callErr != nil || readErr != nil {
		t.Fatalf("diagnostics: call=%v read=%v", callErr, readErr)
	}
	if strings.Contains(string(output), "secret-must-not-appear") || strings.Contains(string(output), "webhook-must-not-appear") || strings.Contains(string(output), "operator-must-not-appear") {
		t.Fatalf("diagnostics leaked secret: %s", output)
	}
	if !bytes.Contains(output, []byte(`"data_directory"`)) || bytes.Contains(output, []byte(`"stripe_test_secret"`)) {
		t.Fatalf("unexpected diagnostics payload: %s", output)
	}
}
