package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestRealFirefoxNanoOpsMatrix(t *testing.T) {
	if _, err := exec.LookPath("firefox-esr"); err != nil {
		t.Skip("Firefox ESR unavailable")
	}
	s, h := testServer(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	accounts := map[string][2]int64{
		"nano-missing": {tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes},
		"nano-zero":    {tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes},
		"nano-below":   {tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes},
		"nano-equal":   {tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes},
		"nano-high":    {tpahubproduct.NanoStorageBytes + 2_000_000, tpahubproduct.NanoEgressBytes + 8_000_000_000},
	}
	for id := range accounts {
		if err := s.db.CreateAccount(context.Background(), store.Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	put := func(id string, storage, egress int64) {
		t.Helper()
		for capability := range map[commerce.CapabilityID]bool{CapabilityRepositoryPublish: true, CapabilityRepositoryDownload: true} {
			if err := s.db.PutGrant(context.Background(), commerce.Grant{CustomerID: commerce.CustomerID(id), ProductID: tpahubproduct.ProductID, CapabilityID: capability, OfferID: "fixture", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: storage, EgressBytes: egress}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("nano-zero", 0, 0)
	put("nano-below", 1, 1)
	put("nano-equal", tpahubproduct.NanoStorageBytes, tpahubproduct.NanoEgressBytes)
	put("nano-high", accounts["nano-high"][0], accounts["nano-high"][1])
	if response, err := http.Get(h.URL + "/ops"); err != nil {
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
	expected := map[string][2]int64{}
	for id, limits := range accounts {
		expected[id] = limits
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
	cmd.Env = append(os.Environ(), "TPAHUB_BROWSER_URL="+h.URL, "TPAHUB_BROWSER_SESSION="+session, "TPAHUB_BROWSER_EXPECTED="+string(expectedJSON))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Firefox qualification failed: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
