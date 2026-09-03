package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

type repositoryCountingWriter struct {
	http.ResponseWriter
	bytes int64
}

func (w *repositoryCountingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}
func (w *repositoryCountingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type rateLimitedResponseWriter struct {
	http.ResponseWriter
	rate    int64
	started time.Time
	written int64
}

func (w *rateLimitedResponseWriter) Write(p []byte) (int, error) {
	if w.started.IsZero() {
		w.started = time.Now()
	}
	target := time.Duration(float64(w.written+int64(len(p))) / float64(w.rate) * float64(time.Second))
	if wait := target - time.Since(w.started); wait > 0 {
		time.Sleep(wait)
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}
func (w *rateLimitedResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *server) recordRepositoryEgress(repositoryID, usageDate string, bytes int64) {
	if bytes == 0 {
		return
	}
	now := s.currentTime().UTC()
	if err := s.db.AddRepositoryEgressDaily(context.Background(), repositoryID, usageDate, bytes, now.Format(time.RFC3339Nano)); err != nil {
		log.Printf("repository egress accounting: repository=%s bytes=%d date=%s: %v", repositoryID, bytes, usageDate, err)
	}
}

func (s *server) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *server) accountEgress(accountID string, now time.Time) (int64, error) {
	entitlement, err := s.db.Entitlement(context.Background(), accountID)
	if err != nil {
		return 0, err
	}
	period, err := effectiveEntitlementPeriod(entitlement, now)
	if err != nil {
		return 0, err
	}
	return s.db.AccountEgressWindow(context.Background(), accountID, period.Start, period.End)
}
func egressLimited(used int64) bool { return used >= NanoEgressBytes }

func egressCapacityMessage(used int64) string {
	if used == NanoEgressBytes {
		return "Annual egress allowance reached. New package downloads are currently rate-limited until the next entitlement period."
	}
	if used > NanoEgressBytes {
		return fmt.Sprintf("Annual egress allowance exceeded by %s. New package downloads are currently rate-limited until the next entitlement period.", formatBytes(used-NanoEgressBytes))
	}
	return "Package delivery is currently unrestricted by the annual egress allowance."
}
