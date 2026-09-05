package store

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
)

func TestOperatorRecoveryReplacementAndSingleConsumption(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a := sha256.Sum256([]byte("TPA-RCV-a"))
	b := sha256.Sum256([]byte("TPA-RCV-b"))
	if err := s.IssueOperatorRecovery(ctx, a[:], "2026-01-01T00:00:00Z", "2026-01-01T00:15:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueOperatorRecovery(ctx, b[:], "2026-01-01T00:01:00Z", "2026-01-01T00:16:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeOperatorRecovery(ctx, a[:], "2026-01-01T00:02:00Z"); err == nil {
		t.Fatal("replaced recovery code accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.ConsumeOperatorRecovery(ctx, b[:], "2026-01-01T00:02:00Z") }()
	}
	wg.Wait()
	close(results)
	successes := 0
	failures := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("recovery consumption successes=%d failures=%d", successes, failures)
	}
	if err := s.ConsumeOperatorRecovery(ctx, b[:], "2026-01-01T00:02:00Z"); err == nil {
		t.Fatal("consumed recovery code replay accepted")
	}
}

func TestOperatorRecoveryExpires(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := sha256.Sum256([]byte("TPA-RCV-expired"))
	if err := s.IssueOperatorRecovery(context.Background(), h[:], "2026-01-01T00:00:00Z", "2026-01-01T00:15:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeOperatorRecovery(context.Background(), h[:], "2026-01-01T00:15:00Z"); err == nil {
		t.Fatal("expired recovery code accepted")
	}
}
