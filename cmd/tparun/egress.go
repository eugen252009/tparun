package main

import (
	"context"
	"fmt"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"log"
	"net/http"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

type repositoryCountingWriter struct {
	http.ResponseWriter
	bytes       int64
	status      int
	writeFailed bool
}

func (w *repositoryCountingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	if err != nil || n != len(p) {
		w.writeFailed = true
	}
	return n, err
}
func (w *repositoryCountingWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *repositoryCountingWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
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
		s.egressMu.Lock()
		if s.egressDegraded == nil {
			s.egressDegraded = make(map[string]bool)
		}
		s.egressDegraded["*"] = true
		s.egressMu.Unlock()
		log.Printf("repository egress accounting: repository=%s bytes=%d date=%s: %v", repositoryID, bytes, usageDate, err)
	}
}

func (s *server) egressAccountingDegraded(accountID string) bool {
	s.egressMu.RLock()
	defer s.egressMu.RUnlock()
	return s.egressDegraded["*"] || s.egressDegraded[accountID]
}

func (s *server) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *server) accountEgress(accountID string, now time.Time) (int64, int64, error) {
	uncertain, uncertaintyErr := s.db.AccountEgressUncertain(context.Background(), accountID)
	if uncertaintyErr != nil {
		return 0, 0, fmt.Errorf("egress accounting uncertainty unavailable: %w", uncertaintyErr)
	}
	if uncertain {
		return 0, 0, fmt.Errorf("egress accounting authority degraded")
	}
	// Durable uncertainty is the source of truth. Once an operator has
	// reconciled all markers for this account, clear the process-local latch.
	s.egressMu.Lock()
	delete(s.egressDegraded, accountID)
	s.egressMu.Unlock()
	if s.egressAccountingDegraded(accountID) {
		return 0, 0, fmt.Errorf("egress accounting authority degraded")
	}
	if s.egressLookup != nil {
		return s.egressLookup(accountID, now)
	}
	grant, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(accountID), tpahubproduct.ProductID, CapabilityRepositoryDownload, now)
	if err != nil {
		return 0, 0, err
	}
	if grant.Limits.EgressBytes < 0 {
		return 0, 0, fmt.Errorf("egress grant has invalid negative allowance")
	}
	start, end, err := grant.PeriodAt(now)
	if err != nil {
		return 0, 0, err
	}
	used, err := s.db.AccountEgressWindow(context.Background(), accountID, start, end)
	return used, grant.Limits.EgressBytes, err
}

func (s *server) egressPeriod(accountID string, now time.Time) (string, string, error) {
	if s.egressLookup != nil {
		start := time.Date(now.UTC().Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		return start.Format(time.RFC3339Nano), start.AddDate(1, 0, 0).Format(time.RFC3339Nano), nil
	}
	grant, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(accountID), tpahubproduct.ProductID, CapabilityRepositoryDownload, now)
	if err != nil {
		return "", "", err
	}
	start, end, err := grant.PeriodAt(now)
	if err != nil {
		return "", "", err
	}
	return start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano), nil
}

// egressLimited marks the soft boundary. TPA.run deliberately keeps package
// delivery available after exhaustion, while exposing the limited state and
// continuing durable accounting; zero is finite and therefore limited at 0.
func egressLimited(used, limit int64) bool { return limit >= 0 && used >= limit }

func egressCapacityMessage(used, limit int64) string {
	if used == limit {
		return "Annual egress allowance reached. New package downloads are currently rate-limited until the next entitlement period."
	}
	if used > limit {
		return fmt.Sprintf("Annual egress allowance exceeded by %s. New package downloads are currently rate-limited until the next entitlement period.", formatBytes(used-limit))
	}
	return "Package delivery is currently unrestricted by the annual egress allowance."
}
