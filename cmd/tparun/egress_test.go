package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestRepositoryEgressHTTPSemantics(t *testing.T) {
	s, _ := testServer(t)
	s.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	// Client EOF/Close is not a server settlement barrier. Observe completion
	// of the real handler, including its deferred strict-accounting settlement.
	finished := make(chan struct{}, 1)
	handler := s.routes()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r); finished <- struct{}{} }))
	defer h.Close()
	id := "22222222222222222222222222222222"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: "egress", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", id)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "package.deb")
	if err := os.WriteFile(path, make([]byte, 10000), 0644); err != nil {
		t.Fatal(err)
	}
	request := func(method, url string, headers map[string]string) *http.Response {
		req, _ := http.NewRequest(method, h.URL+url, nil)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, response.Body); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not finish settlement")
		}
		return response
	}
	response := request("GET", "/r/"+id+"/package.deb", nil)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("GET status %d", response.StatusCode)
	}
	if got, err := s.db.RepositoryEgress(context.Background(), id, "2026-08"); err != nil || got != 10000 {
		t.Fatalf("GET egress = %d, %v", got, err)
	}
	response = request("HEAD", "/r/"+id+"/package.deb", nil)
	response.Body.Close()
	response = request("GET", "/r/"+id+"/package.deb", map[string]string{"Range": "bytes=0-999"})
	response.Body.Close()
	if response.StatusCode != 206 {
		t.Fatalf("range status %d", response.StatusCode)
	}
	if got, _ := s.db.RepositoryEgress(context.Background(), id, "2026-08"); got != 11000 {
		t.Fatalf("range egress = %d", got)
	}
	info, _ := os.Stat(path)
	response = request("GET", "/r/"+id+"/package.deb", map[string]string{"If-Modified-Since": info.ModTime().UTC().Format(http.TimeFormat)})
	response.Body.Close()
	if response.StatusCode != 304 {
		t.Fatalf("conditional status %d", response.StatusCode)
	}
	if got, _ := s.db.RepositoryEgress(context.Background(), id, "2026-08"); got != 11000 {
		t.Fatalf("304 egress = %d", got)
	}
	response = request("GET", "/", nil)
	response.Body.Close()
	if got, _ := s.db.RepositoryEgress(context.Background(), id, "2026-08"); got != 11000 {
		t.Fatalf("website changed egress = %d", got)
	}
}

type shortWriter struct{ http.ResponseWriter }

func (shortWriter) Header() http.Header         { return make(http.Header) }
func (shortWriter) Write(p []byte) (int, error) { return 3, context.Canceled }
func (shortWriter) WriteHeader(int)             {}
func TestRepositoryCountingWriterCountsSuccessfulPartialWrites(t *testing.T) {
	w := &repositoryCountingWriter{ResponseWriter: shortWriter{}}
	n, _ := w.Write(make([]byte, 10))
	if n != 3 || w.bytes != 3 {
		t.Fatalf("partial write n=%d bytes=%d", n, w.bytes)
	}
}

func TestRepositoryEgressAtomicMonthlyAggregation(t *testing.T) {
	s, _ := testServer(t)
	ids := []string{"33333333333333333333333333333333", "44444444444444444444444444444444"}
	for _, id := range ids {
		if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: id, Visibility: "public", CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	var group sync.WaitGroup
	for i := 0; i < 100; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := s.db.AddRepositoryEgress(context.Background(), ids[0], "2026-08", 1000, "now"); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if err := s.db.AddRepositoryEgress(context.Background(), ids[1], "2026-08", 2500, "now"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.db.AccountEgress(context.Background(), "a", "2026-08"); err != nil || got != 102500 {
		t.Fatalf("account egress=%d, %v", got, err)
	}
	if got, err := s.db.AccountEgress(context.Background(), "a", "2026-09"); err != nil || got != 0 {
		t.Fatalf("new month egress=%d, %v", got, err)
	}
}
