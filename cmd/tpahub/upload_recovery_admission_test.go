package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

type admissionErrorReader struct{ sent bool }

func (r *admissionErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}
func TestUploadReadFailureReleasesRepresentation(t *testing.T) {
	for _, ct := range []string{"application/octet-stream", "multipart/form-data; boundary=incomplete"} {
		t.Run(ct, func(t *testing.T) {
			s, h := admissionFixture(t, 100)
			req := httptest.NewRequest(http.MethodPost, "/v1/repositories/"+admissionRepo+"/publishes?filename=x.deb", &admissionErrorReader{})
			req.Header.Set("Authorization", "Bearer tph_s_test")
			req.Header.Set("Content-Type", ct)
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, req)
			if w.Code < 400 {
				t.Fatal("read failure admitted")
			}
			jobs, total := admissionState(t, s)
			if jobs != 0 || total != 0 {
				t.Fatal("failed read retained representation")
			}
			entries, _ := os.ReadDir(filepath.Join(s.cfg.DataDir, "incoming"))
			if len(entries) != 0 {
				t.Fatal("failed read retained files")
			}
			if code, err := admissionHTTP(h, "multipart", 100); err != nil || code != 202 {
				t.Fatalf("retry=%d %v", code, err)
			}
		})
	}
}
func TestUploadDisconnectReleasesRepresentation(t *testing.T) {
	s, h := admissionFixture(t, 100)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = io.WriteString(conn, "POST /v1/repositories/"+admissionRepo+"/publishes?filename=partial.deb HTTP/1.1\r\nHost: "+strings.TrimPrefix(h.URL, "http://")+"\r\nAuthorization: Bearer tph_s_test\r\nContent-Type: application/octet-stream\r\nContent-Length: 100\r\n\r\npartial")
	if err != nil {
		t.Fatal(err)
	}
	wait := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.stagingMu.Lock()
			live := len(s.reservations) > 0
			s.stagingMu.Unlock()
			if live == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("reservation lifecycle timed out")
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(true)
	conn.Close()
	wait(false)
	jobs, total := admissionState(t, s)
	if jobs != 0 || total != 0 {
		t.Fatal("disconnect retained admitted work")
	}
	entries, _ := os.ReadDir(filepath.Join(s.cfg.DataDir, "incoming"))
	if len(entries) != 0 {
		t.Fatal("disconnect retained files")
	}
	if code, err := admissionHTTP(h, "multipart", 100); err != nil || code != 202 {
		t.Fatalf("disconnect retry=%d %v", code, err)
	}
}

func TestAdmissionRestartDurableAndOrphanOwnership(t *testing.T) {
	for _, state := range []string{"queued", "running", "pending-cleanup"} {
		t.Run(state, func(t *testing.T) {
			s, h := admissionFixture(t, 100)
			if code, err := admissionHTTP(h, "multipart", 100); err != nil || code != 202 {
				t.Fatalf("admit=%d %v", code, err)
			}
			paths, err := s.db.AccountStagingPaths(context.Background(), "a")
			if err != nil || len(paths) != 1 {
				t.Fatal("missing durable input")
			}
			orphan := filepath.Join(s.cfg.DataDir, "incoming", "orphan")
			os.MkdirAll(orphan, 0700)
			os.WriteFile(filepath.Join(orphan, "orphan.deb"), []byte("not customer authority"), 0600)
			db, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
			if err != nil {
				t.Fatal(err)
			}
			if state == "running" {
				_, err = db.Exec("UPDATE publish_jobs SET state='running'")
			} else if state == "pending-cleanup" {
				_, err = db.Exec("UPDATE publish_jobs SET state='failed',cleanup_state='pending'")
			}
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			h.Close()
			s.db.Close()
			s.reservations = nil
			reopened, err := store.Open(s.cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			s.db = reopened
			if state != "queued" {
				s.removeAll = func(path string) error {
					if path == paths[0] {
						return errors.New("injected cleanup failure")
					}
					return os.RemoveAll(path)
				}
			}
			if err := s.reconcileStagingState(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(orphan); !os.IsNotExist(err) {
				t.Fatal("orphan not reconciled")
			}
			jobs, total := admissionState(t, s)
			if jobs != 1 || total != 100 {
				t.Fatal("durable pending input not counted after restart")
			}
			if state != "queued" {
				s.removeAll = nil
				if err := s.reconcileStagingState(); err != nil {
					t.Fatal(err)
				}
				jobs, total = admissionState(t, s)
				if jobs != 0 || total != 0 {
					t.Fatal("cleanup did not release durable input")
				}
			}
			// Exercise the production predicate after reconciliation, not a duplicated
			// arithmetic helper or a fabricated customer reservation for orphan files.
			s.stagingMu.Lock()
			err = s.checkAccountLogicalCapacity(context.Background(), "a", 1)
			s.stagingMu.Unlock()
			if state == "queued" && (err == nil || !strings.Contains(err.Error(), "limit exceeded")) {
				t.Fatal("queued capacity lost")
			}
			if state != "queued" && err != nil {
				t.Fatal(err)
			}
		})
	}
}
