package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

const admissionRepo = "1234567890abcdef1234567890abcdef"

func admissionFixture(t *testing.T, limit int64) (*server, *httptest.Server) {
	t.Helper()
	s, h := configuredTestServer(t, func(s *server) {
		s.cfg.MaxUpload = 16 << 20
		s.cfg.MaxStagingBytes = 1 << 30
		s.cfg.MaxAccountStagingBytes = 1 << 30
	})
	setStorageLimit(t, s, limit)
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: admissionRepo, AccountID: "a", Name: "admission", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	return s, h
}
func admissionBody(mode string, n int) (io.Reader, string) {
	payload := bytes.Repeat([]byte("x"), n)
	if mode == "multipart" {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		f, _ := w.CreateFormFile("files", "package.deb")
		f.Write(payload)
		w.Close()
		return bytes.NewReader(b.Bytes()), w.FormDataContentType()
	}
	if mode == "unknown" {
		return struct{ io.Reader }{bytes.NewReader(payload)}, "application/octet-stream"
	}
	return bytes.NewReader(payload), "application/octet-stream"
}
func admissionHTTP(h *httptest.Server, mode string, n int) (int, error) {
	body, ct := admissionBody(mode, n)
	req, _ := http.NewRequest("POST", h.URL+"/v1/repositories/"+admissionRepo+"/publishes?filename=package.deb", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer tph_s_test")
	res, err := h.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode == 507 {
		return res.StatusCode, fmt.Errorf("physical admission: %s", data)
	}
	return res.StatusCode, nil
}
func admissionState(t *testing.T, s *server) (int, int64) {
	t.Helper()
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	if len(s.reservations) != 0 {
		t.Fatal("in-flight reservation leaked after completion")
	}
	paths, err := s.db.AccountStagingPaths(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, p := range paths {
		n, err := fileTreeSize(p)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	return len(paths), total
}
func TestUploadAdmissionRepresentationBoundaries(t *testing.T) {
	for _, mode := range []string{"raw", "unknown", "multipart"} {
		for _, used := range []int{0, 3} {
			for _, delta := range []int{-1, 0, 1} {
				t.Run(fmt.Sprintf("%s/published%d/delta%d", mode, used, delta), func(t *testing.T) {
					s, h := admissionFixture(t, 10)
					root := filepath.Join(s.cfg.DataDir, "repos", admissionRepo, "generations", "old")
					os.MkdirAll(root, 0700)
					if err := os.Symlink("generations/old", filepath.Join(s.cfg.DataDir, "repos", admissionRepo, "current")); err != nil {
						t.Fatal(err)
					}
					os.WriteFile(filepath.Join(root, "old.deb"), bytes.Repeat([]byte("o"), used), 0600)
					if err := s.db.SetActiveGeneration(context.Background(), admissionRepo, "old", "now"); err != nil {
						t.Fatal(err)
					}
					n := 10 - used + delta
					status, err := admissionHTTP(h, mode, n)
					if err != nil {
						t.Fatal(err)
					}
					want := 202
					if delta > 0 {
						want = 413
					}
					if status != want {
						t.Fatalf("status=%d want=%d", status, want)
					}
					jobs, staged := admissionState(t, s)
					if delta > 0 {
						if jobs != 0 || staged != 0 {
							t.Fatal("rejection persisted incoming")
						}
					} else if jobs != 1 || staged != int64(n) {
						t.Fatalf("job representation=%d/%d", jobs, staged)
					}
					grant, err := s.db.GetGrant(context.Background(), "a", TPAHubProductID, CapabilityRepositoryPublish, s.currentTime())
					if err != nil || grant.Limits.StorageBytes != 10 {
						t.Fatal("grant changed")
					}
					active, err := s.db.ActiveGeneration(context.Background(), admissionRepo)
					if err != nil || active != "old" {
						t.Fatal("admission changed publication authority")
					}
				})
			}
		}
	}
}
func TestUploadAdmissionConcurrentRepresentations(t *testing.T) {
	for _, modes := range [][2]string{{"multipart", "multipart"}, {"raw", "raw"}, {"raw", "multipart"}, {"unknown", "multipart"}} {
		for _, sizes := range [][2]int{{6000000, 6000000}, {4000000, 6000000}, {5000000, 5000000}} {
			t.Run(fmt.Sprint(modes, sizes), func(t *testing.T) {
				s, h := admissionFixture(t, 10000000)
				start := make(chan struct{})
				var wg sync.WaitGroup
				codes := make([]int, 2)
				errs := make([]error, 2)
				for i := range codes {
					wg.Add(1)
					go func(i int) { defer wg.Done(); <-start; codes[i], errs[i] = admissionHTTP(h, modes[i], sizes[i]) }(i)
				}
				close(start)
				wg.Wait()
				accepted := 0
				for i, c := range codes {
					if errs[i] != nil {
						t.Fatal(errs[i])
					}
					if c == 202 {
						accepted++
					} else if c != 413 {
						t.Fatalf("unexpected status=%d", c)
					}
				}
				jobs, total := admissionState(t, s)
				if jobs != accepted || total > 10000000 {
					t.Fatalf("durable admission exceeds authority: jobs=%d accepted=%d bytes=%d", jobs, accepted, total)
				}
				if sizes[0]+sizes[1] <= 10000000 && accepted != 2 {
					t.Fatalf("valid exact-fit competitors rejected without retry: codes=%v jobs=%d bytes=%d", codes, jobs, total)
				}
				if sizes[0]+sizes[1] > 10000000 && accepted > 1 {
					t.Fatal("both oversized competitors admitted")
				}
			})
		}
	}
}
func TestUploadAdmissionFailureAndRetry(t *testing.T) {
	for _, mode := range []string{"raw", "unknown", "multipart"} {
		t.Run(mode, func(t *testing.T) {
			s, h := admissionFixture(t, 100)
			db, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TRIGGER fail_admission BEFORE INSERT ON publish_jobs BEGIN SELECT RAISE(ABORT,'injected admission failure'); END`); err != nil {
				t.Fatal(err)
			}
			status, err := admissionHTTP(h, mode, 100)
			if err != nil || status != 500 {
				t.Fatalf("insertion failure=%d %v", status, err)
			}
			jobs, total := admissionState(t, s)
			if jobs != 0 || total != 0 {
				t.Fatal("failed job retained authority")
			}
			entries, _ := os.ReadDir(filepath.Join(s.cfg.DataDir, "incoming"))
			if len(entries) != 0 {
				t.Fatal("failed input retained")
			}
			db.Exec("DROP TRIGGER fail_admission")
			status, err = admissionHTTP(h, mode, 100)
			if err != nil || status != 202 {
				t.Fatalf("retry=%d %v", status, err)
			}
			jobs, total = admissionState(t, s)
			if jobs != 1 || total != 100 {
				t.Fatal("retry representation incorrect")
			}
		})
	}
}
func TestMultipartFiveMillionPolicyRegression(t *testing.T) {
	s, h := admissionFixture(t, 5000000)
	status, err := admissionHTTP(h, "multipart", 3000000)
	if err != nil || status != 202 {
		t.Fatalf("previous unjustified 413: %d %v", status, err)
	}
	jobs, total := admissionState(t, s)
	if jobs != 1 || total != 3000000 {
		t.Fatal("wrong durable representation")
	}
	status, err = admissionHTTP(h, "multipart", 2000001)
	if err != nil || status != 413 {
		t.Fatalf("over limit=%d %v", status, err)
	}
}
