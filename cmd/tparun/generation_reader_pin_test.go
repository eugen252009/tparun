package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/enforcement"
	"github.com/eugen252009/tpahub/internal/store"
)

type blockingDeliveryStrategy struct {
	entered chan struct{}
	resume  chan struct{}
}

func (s blockingDeliveryStrategy) Prepare(ctx context.Context, request enforcement.Request) (enforcement.Guard, error) {
	close(s.entered)
	select {
	case <-s.resume:
		return enforcement.None{}.Prepare(ctx, request)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestReaderPinsGenerationFileAcrossRetentionCleanup(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	s, h := configuredTestServer(t, func(s *server) {
		s.cfg.MaxGenerations = 1
		s.deliveryOnce.Do(func() {
			s.delivery = &deliveryBehavior{enforcement: blockingDeliveryStrategy{entered: entered, resume: resume}}
		})
	})
	repositoryID := strings.Repeat("7", 32)
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "reader-pin", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	generationRoot := filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations")
	oldGeneration := filepath.Join(generationRoot, "old")
	newGeneration := filepath.Join(generationRoot, "new")
	if err := os.MkdirAll(oldGeneration, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newGeneration, 0o755); err != nil {
		t.Fatal(err)
	}
	const payload = "already-open generation bytes"
	if err := os.WriteFile(filepath.Join(oldGeneration, "Packages"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newGeneration, "Packages"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(context.Background(), repositoryID, "old", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	responseDone := make(chan struct {
		status int
		body   string
		err    error
	}, 1)
	go func() {
		response, err := http.Get(h.URL + "/r/" + repositoryID + "/Packages")
		if err != nil {
			responseDone <- struct {
				status int
				body   string
				err    error
			}{err: err}
			return
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		responseDone <- struct {
			status int
			body   string
			err    error
		}{status: response.StatusCode, body: string(body), err: readErr}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(resume)
		t.Fatal("reader did not reach delivery after opening its generation file")
	}
	if err := s.db.SetActiveGeneration(context.Background(), repositoryID, "new", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		close(resume)
		t.Fatal(err)
	}
	if err := s.collectOldGenerations(repositoryID); err != nil {
		close(resume)
		t.Fatal(err)
	}
	if _, err := os.Stat(oldGeneration); !os.IsNotExist(err) {
		close(resume)
		t.Fatalf("old generation was not collected: %v", err)
	}
	close(resume)
	result := <-responseDone
	if result.err != nil || result.status != http.StatusOK || result.body != payload {
		t.Fatalf("reader result status=%d body=%q err=%v", result.status, result.body, result.err)
	}
}
