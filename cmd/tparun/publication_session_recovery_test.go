package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestStartupDiscardsIncompletePublicationSession(t *testing.T) {
	s, _ := testServer(t)
	const repositoryID = "acacacacacacacacacacacacacacacac"
	const sessionID = "adadadadadadadadadadadadadadadad"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "recovery", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", sessionID)
	if err := os.MkdirAll(filepath.Join(incoming, "batches", "batch-00000000"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "batches", "batch-00000000", "partial.deb"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreatePublicationSession(context.Background(), store.PublicationSession{ID: sessionID, RepositoryID: repositoryID, OwnerAccountID: "a", IncomingPath: incoming, ExpectedCount: 1, CreatedAt: "now"}, []store.PublicationArtifact{{Filename: "partial.deb", Package: "partial", Version: "1", Architecture: "all", UploadedSize: 10, UploadedSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(incoming); !os.IsNotExist(err) {
		t.Fatalf("incomplete session directory remains: %v", err)
	}
	if _, err := s.db.PublicationSession(context.Background(), sessionID); err == nil {
		t.Fatal("incomplete session remains in durable state")
	}
}
