package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func firefoxFormatBytes(value int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(value)
	unit := 0
	for unit < len(units)-1 && v >= 1000 {
		v /= 1000
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", value)
	}
	return fmt.Sprintf("%.2f %s", v, units[unit])
}

func TestRealFirefoxNanoOpsMatrix(t *testing.T) {
	if _, err := exec.LookPath("firefox-esr"); err != nil {
		t.Skip("Firefox ESR unavailable")
	}
	s, h := testServer(t)
	h.Close()
	var methods []string
	var methodsMu sync.Mutex
	candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodsMu.Lock()
		methods = append(methods, r.Method)
		methodsMu.Unlock()
		s.routes().ServeHTTP(w, r)
	}))
	defer candidate.Close()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	accounts := map[string][2]int64{
		"nano-small": {1, 1},
		"nano-equal": {tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes},
		"nano-high":  {tpahubproduct.NanoStorageBytes + 2_000_000, tpahubproduct.NanoEgressBytes + 8_000_000_000},
	}
	for id := range accounts {
		if err := s.db.CreateAccount(context.Background(), store.Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: "0123456789abcdef0123456789abcdef", AccountID: "nano-small", Name: "firefox-nano-repository", Visibility: "public", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	put := func(id string, storage, egress int64) {
		t.Helper()
		for capability := range map[commerce.CapabilityID]bool{CapabilityRepositoryPublish: true, CapabilityRepositoryDownload: true} {
			if err := s.db.PutGrant(context.Background(), commerce.Grant{CustomerID: commerce.CustomerID(id), ProductID: tpahubproduct.ProductID, CapabilityID: capability, OfferID: "fixture", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: storage, EgressBytes: egress}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("nano-small", 1, 1)
	put("nano-equal", tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes)
	put("nano-high", accounts["nano-high"][0], accounts["nano-high"][1])
	if response, err := http.Get(candidate.URL + "/ops"); err != nil {
		t.Fatalf("anonymous /ops err=%v", err)
	} else {
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode >= 300 || bytes.Contains(body, []byte("Service dashboard")) {
			t.Fatalf("anonymous /ops was not denied: status=%d err=%v", response.StatusCode, readErr)
		}
	}
	session, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}
	type browserExpectation struct {
		Limits [2]int64  `json:"limits"`
		Texts  [2]string `json:"texts"`
	}
	expected := map[string]browserExpectation{}
	for id, limits := range accounts {
		expected[id] = browserExpectation{Limits: limits, Texts: [2]string{"0 B / " + firefoxFormatBytes(limits[0]) + " (0%)", "0 B / " + firefoxFormatBytes(limits[1]) + " (0%)"}}
	}
	expectedJSON, _ := json.Marshal(expected)
	_, file, _, _ := runtime.Caller(0)
	profile := t.TempDir()
	browser := exec.Command("firefox-esr", "--headless", "--marionette", "--profile", profile)
	browser.Stdout, browser.Stderr = nil, nil
	if err := browser.Start(); err != nil {
		t.Fatal(err)
	}
	defer browser.Process.Kill()
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:2828", 200*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Firefox Marionette did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	script := filepath.Join(filepath.Dir(file), "..", "..", "tests", "firefox-ops-nano-test.py")
	cmd := exec.Command("python3", script)
	cmd.Env = append(os.Environ(), "TPAHUB_BROWSER_URL="+candidate.URL, "TPAHUB_BROWSER_SESSION="+session, "TPAHUB_BROWSER_EXPECTED="+string(expectedJSON))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Firefox qualification failed: %v\n%s", err, output)
	}
	t.Logf("%s", output)
	methodsMu.Lock()
	observedMethods := append([]string(nil), methods...)
	methodsMu.Unlock()
	for _, method := range observedMethods {
		if method != http.MethodGet && method != http.MethodHead {
			t.Fatalf("dashboard generated unexpected HTTP method %s; methods=%v", method, observedMethods)
		}
	}
	response, err := http.Get(candidate.URL + "/static/ops.js")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ops.js status=%d", response.StatusCode)
	}
}
