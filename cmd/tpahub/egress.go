package main

import (
	"context"
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

func (s *server) recordRepositoryEgress(repositoryID string, bytes int64) {
	if bytes == 0 {
		return
	}
	now := s.currentTime().UTC()
	period := now.Format("2006-01")
	if err := s.db.AddRepositoryEgress(context.Background(), repositoryID, period, bytes, now.Format(time.RFC3339Nano)); err != nil {
		log.Printf("repository egress accounting: repository=%s bytes=%d period=%s: %v", repositoryID, bytes, period, err)
	}
}

func (s *server) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *server) accountEgress(accountID string, now time.Time) (int64, error) {
	return s.db.AccountEgress(context.Background(), accountID, now.UTC().Format("2006-01"))
}
