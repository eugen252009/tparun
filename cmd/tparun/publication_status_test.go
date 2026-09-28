package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
)

func publicationStatusRequest(t *testing.T, s *server, cookie string) publicationOperationalStatus {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, operatorPublicationStatusPath, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("publication status returned %d: %s", w.Code, w.Body.String())
	}
	var status publicationOperationalStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestPublicationStatusRequiresOperatorSessionAndReturnsOnlyAggregates(t *testing.T) {
	s, _ := testServer(t)
	for name, request := range map[string]*http.Request{
		"anonymous": httptest.NewRequest(http.MethodGet, operatorPublicationStatusPath, nil),
		"account bearer": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, operatorPublicationStatusPath, nil)
			r.Header.Set("Authorization", "Bearer tph_s_test")
			return r
		}(),
	} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, request)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s status=%d body=%q, want 401", name, w.Code, w.Body.String())
		}
	}

	token, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, operatorPublicationStatusPath, nil)
	r.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: token})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("operator status=%d body=%s", w.Code, w.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"queued_jobs", "running_jobs", "active_attempts", "incomplete_sessions", "execution_idle", "fully_quiescent"}
	if len(fields) != len(want) {
		t.Fatalf("status exposed unexpected fields: %v", fields)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Errorf("status omitted %q", key)
		}
	}
	var status publicationOperationalStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status != (publicationOperationalStatus{ExecutionIdle: true, FullyQuiescent: true}) {
		t.Fatalf("empty queue status=%+v", status)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("operator status cache policy=%q", w.Header().Get("Cache-Control"))
	}
	post := httptest.NewRequest(http.MethodPost, operatorPublicationStatusPath, nil)
	post.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: token})
	postWriter := httptest.NewRecorder()
	s.routes().ServeHTTP(postWriter, post)
	if postWriter.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status=%d, want 405", postWriter.Code)
	}
	if strings.Contains(w.Body.String(), "incoming") || strings.Contains(w.Body.String(), "repository") || strings.Contains(w.Body.String(), "account") {
		t.Fatalf("status leaked tenant or path data: %s", w.Body.String())
	}
}

func TestPublicationStatusDistinguishesRunningAttemptAndOpenSession(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	const repositoryID = "abababababababababababababababab"
	const sessionID = "bcbcbcbcbcbcbcbcbcbcbcbcbcbcbcbc"
	const jobID = "cccccccccccccccccccccccccccccccc"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repositoryID, AccountID: "a", Name: "status-test", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", jobID)
	if err := s.db.CreateJob(ctx, store.Job{ID: jobID, RepositoryID: repositoryID, PublisherAccountID: "a", State: "queued", IncomingPath: incoming, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	cookie, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}
	queuedStatus := publicationStatusRequest(t, s, cookie)
	if queuedStatus.QueuedJobs != 1 || queuedStatus.RunningJobs != 0 || queuedStatus.ActiveAttempts != 0 || queuedStatus.ExecutionIdle || queuedStatus.FullyQuiescent {
		t.Fatalf("queued job status=%+v", queuedStatus)
	}
	job, err := s.db.Claim(ctx)
	if err != nil || job.ID != jobID || job.State != "running" {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	artifact := store.PublicationArtifact{Filename: "fixture.deb", Package: "fixture", Version: "1", Architecture: "all", UploadedSize: 1, UploadedSHA256: strings.Repeat("a", 64)}
	if err := s.db.CreatePublicationSession(ctx, store.PublicationSession{ID: sessionID, RepositoryID: repositoryID, OwnerAccountID: "a", IncomingPath: filepath.Join(s.cfg.DataDir, "incoming", sessionID), ExpectedCount: 1, CreatedAt: "now"}, []store.PublicationArtifact{artifact}); err != nil {
		t.Fatal(err)
	}

	// A persisted running job blocks execution-idle even before its runner call
	// begins (for example, while the authority prepares bounded inputs).
	localStatus := publicationStatusRequest(t, s, cookie)
	if localStatus.QueuedJobs != 0 || localStatus.RunningJobs != 1 || localStatus.ActiveAttempts != 0 || localStatus.IncompleteSessions != 1 || localStatus.ExecutionIdle || localStatus.FullyQuiescent {
		t.Fatalf("running job status=%+v", localStatus)
	}

	// Exercise the real SSHRunner process path with a controlled fake ssh
	// executable that blocks after consuming the request frame. The same
	// production counter wraps the configured SSHRunner call.
	s.cfg.PublicationBackend = "ssh"
	input := filepath.Join(t.TempDir(), "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "fixture.deb"), []byte("synthetic SSH status input"), 0600); err != nil {
		t.Fatal(err)
	}
	sshRoot := t.TempDir()
	identity := filepath.Join(sshRoot, "identity")
	knownHosts := filepath.Join(sshRoot, "known_hosts")
	for _, path := range []string{identity, knownHosts} {
		if err := os.WriteFile(path, []byte("test-only"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	startedPath := filepath.Join(sshRoot, "started")
	releasePath := filepath.Join(sshRoot, "release")
	sshBinary := filepath.Join(sshRoot, "controlled-ssh")
	script := "#!/bin/sh\n/bin/cat >/dev/null\nprintf started > " + shellQuote(startedPath) + "\nwhile [ ! -e " + shellQuote(releasePath) + " ]; do /bin/sleep 0.01; done\nexit 1\n"
	if err := os.WriteFile(sshBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.runner = tpa.SSHRunner{Config: tpa.SSHConfig{Target: "worker@example.invalid", IdentityFile: identity, KnownHostsFile: knownHosts, SSHBinary: sshBinary}}
	attemptDone := make(chan error, 1)
	go func() {
		attemptDone <- s.runPublicationAttempt(context.Background(), tpa.PublishRequest{Input: input, Output: filepath.Join(sshRoot, "candidate"), JobID: job.ID, AttemptID: strings.Repeat("d", 32), StartedAt: "2026-01-01T00:00:00Z"}, 1<<20, 64)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("controlled SSH process did not reach its blocked phase")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sshStatus := publicationStatusRequest(t, s, cookie)
	if sshStatus.QueuedJobs != 0 || sshStatus.RunningJobs != 1 || sshStatus.ActiveAttempts != 1 || sshStatus.IncompleteSessions != 1 || sshStatus.ExecutionIdle || sshStatus.FullyQuiescent {
		t.Fatalf("active SSH runner status=%+v", sshStatus)
	}
	if err := os.WriteFile(releasePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-attemptDone; err == nil {
		t.Fatal("controlled SSH failure unexpectedly succeeded")
	}
	if err := s.db.Finish(ctx, job.ID, "failed", "fixture complete"); err != nil {
		t.Fatal(err)
	}
	idleWithSession := publicationStatusRequest(t, s, cookie)
	if idleWithSession.QueuedJobs != 0 || idleWithSession.RunningJobs != 0 || idleWithSession.ActiveAttempts != 0 || idleWithSession.IncompleteSessions != 1 || !idleWithSession.ExecutionIdle || idleWithSession.FullyQuiescent {
		t.Fatalf("incomplete-session-only status=%+v", idleWithSession)
	}
	if err := s.db.DeleteOpenPublicationSession(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	fullyIdle := publicationStatusRequest(t, s, cookie)
	if fullyIdle != (publicationOperationalStatus{ExecutionIdle: true, FullyQuiescent: true}) {
		t.Fatalf("terminal job/session status=%+v", fullyIdle)
	}
}

func TestAdminPublicationStatusCLIUsesOperatorCookieFile(t *testing.T) {
	s, h := testServer(t)
	cookie, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}
	cookiePath := filepath.Join(t.TempDir(), "operator-cookie")
	if err := os.WriteFile(cookiePath, []byte(cookie+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TPARUN_SERVER", h.URL)
	restore := capturePublicationStatusStdout(t)
	err = cli(config.Config{}, []string{"admin", "publication-status", "--cookie-file", cookiePath, "--json"})
	output := restore()
	if err != nil {
		t.Fatal(err)
	}
	var status publicationOperationalStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatalf("CLI JSON %q: %v", output, err)
	}
	if status != (publicationOperationalStatus{ExecutionIdle: true, FullyQuiescent: true}) {
		t.Fatalf("CLI returned %+v", status)
	}
	if strings.Contains(output, cookie) {
		t.Fatal("CLI leaked operator cookie")
	}
}

func TestReadOperatorSessionCookieRejectsUnsafeOrInvalidFiles(t *testing.T) {
	valid, err := randomOpaque("ops_")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "cookie")
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readOperatorSessionCookie(path); err != nil || got != valid {
		t.Fatalf("valid cookie got=%q err=%v", got, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorSessionCookie(path); err == nil {
		t.Fatal("world-readable cookie file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-a-cookie"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorSessionCookie(path); err == nil {
		t.Fatal("invalid cookie value accepted")
	}
	link := filepath.Join(root, "cookie-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorSessionCookie(link); err == nil {
		t.Fatal("symlink cookie file accepted")
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func capturePublicationStatusStdout(t *testing.T) func() string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	return func() string {
		os.Stdout = old
		_ = writer.Close()
		data, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(data)
	}
}
