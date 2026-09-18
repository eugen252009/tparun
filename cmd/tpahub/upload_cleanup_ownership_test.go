package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveUploadOwnershipSurvivesOrphanCleanup(t *testing.T) {
	s, h := admissionFixture(t, 100)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /v1/repositories/%s/publishes?filename=live.deb HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer tph_s_test\r\nContent-Type: application/octet-stream\r\nContent-Length: 100\r\n\r\npartial", admissionRepo, strings.TrimPrefix(h.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	var input string
	deadline := time.Now().Add(5 * time.Second)
	for input == "" {
		s.stagingMu.Lock()
		for _, reservation := range s.reservations {
			if reservation.Kind != "incoming" {
				continue
			}
			path := filepath.Join(s.cfg.DataDir, "incoming", reservation.JobID, "live.deb")
			if info, e := os.Stat(path); e == nil && info.Size() == 7 {
				input = path
			}
		}
		s.stagingMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("upload did not reach observed partial write")
		}
		if input == "" {
			time.Sleep(time.Millisecond)
		}
	}
	orphan := filepath.Join(s.cfg.DataDir, "incoming", "unowned-fixture")
	if err = os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(orphan, "orphan.deb"), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	// Invoke the exact production cleanup while the real HTTP upload is blocked.
	if err = s.reconcileOrphanedStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(input); err != nil {
		t.Fatalf("cleanup deleted live reserved upload: %v", err)
	}
	if _, err = os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan was not removed: %v", err)
	}
	if _, err = io.WriteString(conn, strings.Repeat("x", 93)); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("admission=%d", response.StatusCode)
	}
	if err = s.reconcileOrphanedStagingState(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(input)
	if err != nil || string(data) != "partial"+strings.Repeat("x", 93) {
		t.Fatalf("durable input lost or changed: bytes=%d err=%v", len(data), err)
	}
	jobs, bytes := admissionState(t, s)
	if jobs != 1 || bytes != 100 {
		t.Fatalf("ownership handoff: jobs=%d bytes=%d", jobs, bytes)
	}
}
