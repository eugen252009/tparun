package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// The gate closes before Wait starts: no late handler can increment the group
// after state has been declared safe to close. Rejected requests never enter
// application code or touch persistent state.
type serverLifecycle struct {
	gate     sync.Mutex
	draining atomic.Bool
	requests sync.WaitGroup
}

func (l *serverLifecycle) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.gate.Lock()
		if l.draining.Load() {
			l.gate.Unlock()
			http.Error(w, "server draining", http.StatusServiceUnavailable)
			return
		}
		l.requests.Add(1)
		l.gate.Unlock()
		defer l.requests.Done()
		next.ServeHTTP(w, r)
	})
}
func (l *serverLifecycle) drain() { l.gate.Lock(); l.draining.Store(true); l.gate.Unlock() }

// The caller owns the database and directory lock. safe=false means a bounded
// forced shutdown could not settle all application work: retain those resources
// until process exit, rather than closing them underneath live goroutines.
func (s *server) serveAndDrain(stop context.Context, srv *http.Server, listener net.Listener, grace, settle time.Duration) (safe bool, err error) {
	if grace <= 0 {
		grace = 20 * time.Second
	}
	if settle <= 0 {
		settle = 5 * time.Second
	}
	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	s.ctx = work
	var background sync.WaitGroup
	background.Add(3)
	go func() { defer background.Done(); s.worker() }()
	go func() { defer background.Done(); s.cleanupRetryLoop() }()
	go func() { defer background.Done(); s.lifecycleMaintenanceLoop() }()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	var serveErr error
	select {
	case <-stop.Done():
	case serveErr = <-served:
	}
	s.lifecycle.drain()
	drainContext, cancelDrain := context.WithTimeout(context.Background(), grace)
	drainErr := srv.Shutdown(drainContext)
	cancelDrain()
	// Active HTTP settlement had the grace interval. Stop publication/cleanup
	// workers now; queued jobs remain durable for restart, active work settles its
	// existing success/failure path before the store may close.
	cancelWork()
	if drainErr != nil {
		_ = srv.Close()
	}
	done := make(chan struct{})
	go func() { s.lifecycle.requests.Wait(); background.Wait(); close(done) }()
	timer := time.NewTimer(settle)
	defer timer.Stop()
	select {
	case <-done:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return true, serveErr
		}
		if drainErr != nil {
			return true, fmt.Errorf("HTTP shutdown grace expired; forced connections closed and work settled: %w", drainErr)
		}
		return true, nil
	case <-timer.C:
		return false, fmt.Errorf("shutdown settlement timed out; state retained for process-exit recovery")
	}
}
