package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
)

type shutdownResult struct {
	safe bool
	err  error
}

func startShutdownFixture(t *testing.T, s *server, handler http.Handler, grace, settle time.Duration) (string, context.CancelFunc, <-chan shutdownResult, *http.Server) {
	t.Helper()
	srv := newHTTPServer(s)
	if handler != nil {
		srv.Handler = s.lifecycle.wrap(handler)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan shutdownResult, 1)
	go func() { safe, err := s.serveAndDrain(ctx, srv, ln, grace, settle); done <- shutdownResult{safe, err} }()
	t.Cleanup(cancel)
	return "http://" + ln.Addr().String(), cancel, done, srv
}
func finishShutdown(t *testing.T, done <-chan shutdownResult) shutdownResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not terminate")
		return shutdownResult{}
	}
}
func awaitDraining(t *testing.T, s *server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !s.lifecycle.draining.Load() {
		if time.Now().After(deadline) {
			t.Fatal("drain did not begin")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestShutdownIdleAwaitsBackgroundWork(t *testing.T) {
	s, _ := testServer(t)
	_, stop, done, _ := startShutdownFixture(t, s, nil, time.Second, time.Second)
	stop()
	result := finishShutdown(t, done)
	if !result.safe || result.err != nil {
		t.Fatalf("idle shutdown: %+v", result)
	}
	if _, err := s.db.AccountByID(context.Background(), "a"); err != nil {
		t.Fatalf("store closed before caller owns teardown: %v", err)
	}
}

type shutdownBlockingWriter struct {
	http.ResponseWriter
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *shutdownBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.ResponseWriter.Write(p)
}
func TestShutdownAwaitsActiveAPIAndDownload(t *testing.T) {
	for _, kind := range []string{"api", "download"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := admissionFixture(t, 1<<20)
			root := filepath.Join(s.cfg.DataDir, "repos", admissionRepo)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "fixture.deb"), []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			routes := s.routes()
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "api" {
					close(entered)
					<-release
					routes.ServeHTTP(w, r)
				} else {
					routes.ServeHTTP(&shutdownBlockingWriter{ResponseWriter: w, entered: entered, release: release}, r)
				}
			})
			origin, stop, done, srv := startShutdownFixture(t, s, handler, 2*time.Second, time.Second)
			response := make(chan error, 1)
			path := "/v1/repositories"
			if kind == "download" {
				path = "/r/" + admissionRepo + "/fixture.deb"
			}
			go func() {
				req, _ := http.NewRequest("GET", origin+path, nil)
				req.Header.Set("Authorization", "Bearer tph_s_test")
				res, err := http.DefaultClient.Do(req)
				if err == nil {
					_, err = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						err = io.ErrUnexpectedEOF
					}
				}
				response <- err
			}()
			awaitCapacityEvent(t, entered)
			stop()
			awaitDraining(t, s)
			select {
			case r := <-done:
				t.Fatalf("returned with active %s: %+v", kind, r)
			default:
			}
			denied := httptest.NewRecorder()
			srv.Handler.ServeHTTP(denied, httptest.NewRequest("GET", "/", nil))
			if denied.Code != 503 {
				t.Fatalf("new request during drain=%d", denied.Code)
			}
			once.Do(func() { close(release) })
			select {
			case err := <-response:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("active response stalled")
			}
			result := finishShutdown(t, done)
			if !result.safe || result.err != nil {
				t.Fatalf("active shutdown: %+v", result)
			}
		})
	}
}
func TestShutdownAllowsActiveUploadAndKeepsDurableJob(t *testing.T) {
	s, _ := admissionFixture(t, 100)
	entered := make(chan struct{})
	var once sync.Once
	s.capacityObserve = func(stage, path string) {
		if stage == "after-write" {
			once.Do(func() { close(entered) })
		}
	}
	origin, stop, done, _ := startShutdownFixture(t, s, nil, 2*time.Second, time.Second)
	body, writer := io.Pipe()
	defer body.Close()
	defer writer.Close()
	request, _ := http.NewRequest("POST", origin+"/v1/repositories/"+admissionRepo+"/publishes?filename=live.deb", body)
	request.Header.Set("Authorization", "Bearer tph_s_test")
	request.ContentLength = 100
	response := make(chan *http.Response, 1)
	failure := make(chan error, 1)
	go func() {
		r, e := http.DefaultClient.Do(request)
		if e != nil {
			failure <- e
		} else {
			response <- r
		}
	}()
	if _, err := writer.Write([]byte("prefix")); err != nil {
		t.Fatal(err)
	}
	awaitCapacityEvent(t, entered)
	stop()
	awaitDraining(t, s)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), 94)); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case r := <-response:
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != 202 {
			t.Fatalf("active upload=%d", r.StatusCode)
		}
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("upload stalled")
	}
	result := finishShutdown(t, done)
	if !result.safe || result.err != nil {
		t.Fatalf("upload shutdown: %+v", result)
	}
	jobs, total := admissionState(t, s)
	if jobs != 1 || total != 100 {
		t.Fatalf("durable input lost: %d/%d", jobs, total)
	}
}

type cancellationPublicationRunner struct{ started chan struct{} }

func (r *cancellationPublicationRunner) Check(context.Context) error { return nil }
func (r *cancellationPublicationRunner) Publish(ctx context.Context, _ tpa.PublishRequest) error {
	close(r.started)
	<-ctx.Done()
	return ctx.Err()
}
func (r *cancellationPublicationRunner) PublishBounded(ctx context.Context, p tpa.PublishRequest, _ int64) error {
	return r.Publish(ctx, p)
}
func TestShutdownSettlesWorkerAndPreservesQueuedPublication(t *testing.T) {
	s, _ := admissionFixture(t, 1<<20)
	s.cfg.PublishTimeout = time.Minute
	runner := &cancellationPublicationRunner{started: make(chan struct{})}
	s.runner = runner
	for _, id := range []string{"one", "two"} {
		path := filepath.Join(s.cfg.DataDir, "incoming", id)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "fixture.deb"), []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.db.CreateJob(context.Background(), store.Job{ID: id, RepositoryID: admissionRepo, State: "queued", IncomingPath: path, CreatedAt: id}); err != nil {
			t.Fatal(err)
		}
	}
	_, stop, done, _ := startShutdownFixture(t, s, nil, time.Second, time.Second)
	awaitCapacityEvent(t, runner.started)
	stop()
	result := finishShutdown(t, done)
	if !result.safe || result.err != nil {
		t.Fatalf("worker shutdown: %+v", result)
	}
	first, err := s.db.Job(context.Background(), "one")
	if err != nil || first.State != "failed" || !strings.Contains(first.Error, "canceled") {
		t.Fatalf("active job: %+v %v", first, err)
	}
	second, err := s.db.Job(context.Background(), "two")
	if err != nil || second.State != "queued" {
		t.Fatalf("queued job: %+v %v", second, err)
	}
	if _, err = os.Stat(second.IncomingPath); err != nil {
		t.Fatalf("queued input removed: %v", err)
	}
	if len(s.reservations) != 0 {
		t.Fatal("worker exited with live reservations")
	}
}
func TestShutdownTimeoutDoesNotDeclareLiveStateSafe(t *testing.T) {
	s, _ := testServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		defer close(finished)
		if _, err := s.db.AccountByID(context.Background(), "a"); err != nil {
			t.Error(err)
		}
	})
	origin, stop, done, _ := startShutdownFixture(t, s, handler, 20*time.Millisecond, 50*time.Millisecond)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		r, e := http.Get(origin)
		if e == nil {
			r.Body.Close()
		}
	}()
	awaitCapacityEvent(t, entered)
	stop()
	result := finishShutdown(t, done)
	if result.safe || result.err == nil {
		t.Fatalf("live handler declared safe: %+v", result)
	}
	close(release)
	awaitCapacityEvent(t, finished)
	awaitCapacityEvent(t, requestDone)
}
