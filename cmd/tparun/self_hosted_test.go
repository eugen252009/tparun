package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
)

func TestSelfHostedServerProcess(t *testing.T) {
	if os.Getenv("TPARUN_SELF_HOSTED_TEST_PROCESS") != "1" {
		return
	}
	os.Args = []string{"tpahub", "serve"}
	main() // real startup/composition, SQLite, runner, worker and HTTP server
}

func TestFreshSelfHostedInitializationStartupPublicationAndUsage(t *testing.T) {
	dir := t.TempDir()
	if err := cli(config.Config{DataDir: dir}, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	// Neither reinitialization nor a running server may reinterpret this profile.
	if err := cli(config.Config{DataDir: dir}, []string{"init"}); err == nil {
		t.Fatal("profile overwritten")
	}
	tpaBin := buildCurrentTPA(t)
	signing := testCandidateSigningConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	logPath := filepath.Join(t.TempDir(), "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSelfHostedServerProcess$")
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TPARUN_") && !strings.HasPrefix(e, "GNUPGHOME=") && !strings.HasPrefix(e, "TPA_BIN=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "TPARUN_SELF_HOSTED_TEST_PROCESS=1", "TPARUN_DATA_DIR="+dir, "TPARUN_LISTEN="+address, "TPA_BIN="+tpaBin, "GNUPGHOME="+signing.GPGHome, "TPARUN_GPG_FINGERPRINT="+signing.GPGFingerprint,
		"TPARUN_STRIPE_TEST_SECRET=deliberately-invalid", "TPARUN_BILLING_RATE_CARD=does-not-exist", "TPARUN_CHECKOUT_SUCCESS_URL=invalid:unused")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-stopped:
			if err != nil {
				b, _ := os.ReadFile(logPath)
				t.Errorf("server exit=%v log=%s", err, b)
			}
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-stopped
			t.Error("server shutdown timed out")
		}
	}()
	base := "http://" + address
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, err := client.Get(base + "/version")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logPath)
			t.Fatalf("startup timed out: %s", b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	request := func(method, path string, body []byte, contentType, token string) []byte {
		t.Helper()
		r, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		result, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("%s %s status=%d", method, path, response.StatusCode)
		}
		return result
	}
	var account struct{ Token string }
	if err := json.Unmarshal(request("POST", "/v1/accounts", []byte(`{"email":"self-hosted@example.invalid","password":"self-hosted-test-password"}`), "application/json", ""), &account); err != nil || account.Token == "" {
		t.Fatal("local account provisioning failed")
	}
	var repo struct{ ID string }
	if err := json.Unmarshal(request("POST", "/v1/repositories", []byte(`{"name":"local","visibility":"public"}`), "application/json", account.Token), &repo); err != nil || repo.ID == "" {
		t.Fatal("repository creation failed")
	}
	deb := filepath.Join(t.TempDir(), "local.deb")
	buildWorkerTestDeb(t, deb, "self-hosted-test")
	payload, err := os.ReadFile(deb)
	if err != nil {
		t.Fatal(err)
	}
	var job struct{ ID string }
	if err := json.Unmarshal(request("POST", "/v1/repositories/"+repo.ID+"/publishes?filename=local.deb", payload, "application/octet-stream", account.Token), &job); err != nil || job.ID == "" {
		t.Fatal("publication admission failed")
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		var status struct{ State, Error string }
		if err := json.Unmarshal(request("GET", "/v1/publish-jobs/"+job.ID, nil, "", account.Token), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "succeeded" {
			break
		}
		if status.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("publication: %+v", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	release := request("GET", "/r/"+repo.ID+"/dists/stable/InRelease", nil, "", "")
	if !bytes.Contains(release, []byte("BEGIN PGP SIGNED MESSAGE")) {
		t.Fatal("signed repository not delivered")
	}
	r, _ := http.NewRequest("GET", base+"/account", nil)
	r.AddCookie(&http.Cookie{Name: "tpahub_session", Value: account.Token})
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 ||
		!bytes.Contains(body, []byte(formatBytes(int64(len(release))))) ||
		!bytes.Contains(body, []byte("observed locally")) ||
		!bytes.Contains(body, []byte("1 admitted file requests")) ||
		!bytes.Contains(body, []byte("Volatile local metrics; reset on restart. No egress quota enforcement.")) ||
		bytes.Contains(body, []byte("used this entitlement year")) ||
		bytes.Contains(body, []byte("Egress capacity: 0 B")) ||
		bytes.Contains(body, []byte("Billing preview")) {
		t.Fatalf("usage dashboard=%d %s", response.StatusCode, body)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Fresh initialized registration must persist operator-local facts, not Nano
	// offer facts. This assertion observes the real child-process main/serve path.
	var localGrants, publishGrants, downloadGrants int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE source='local' AND offer_id='local-policy-v1' AND purchase_id='' AND enabled=1 AND valid_until='' AND period_cadence='annual'`).Scan(&localGrants); err != nil || localGrants != 4 {
		t.Fatalf("fresh local grants=%d err=%v", localGrants, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE capability_id='repository.publish' AND storage_limit_bytes=5000000`).Scan(&publishGrants); err != nil || publishGrants != 1 {
		t.Fatalf("local storage policy=%d %v", publishGrants, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE capability_id='repository.download' AND egress_limit_bytes=12000000000`).Scan(&downloadGrants); err != nil || downloadGrants != 1 {
		t.Fatalf("local egress policy=%d %v", downloadGrants, err)
	}
	for _, table := range []string{"egress_accounting_uncertain", "repository_egress_daily", "repository_egress_usage", "mock_billing_events", "billing_purchases", "billing_provider_accounts"} {
		var count int
		if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unexpected %s effects=%d err=%v", table, count, err)
		}
	}
}
