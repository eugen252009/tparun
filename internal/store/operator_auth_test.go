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

func TestOperatorRecoveryExpiryBoundaries(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := sha256.Sum256([]byte("boundary"))
	expiry := "2026-01-01T00:00:00.000000001Z"
	if err := s.IssueOperatorRecovery(context.Background(), h[:], "2026-01-01T00:00:00Z", expiry); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeOperatorRecovery(context.Background(), h[:], "2025-12-31T23:59:59.999999999Z"); err != nil {
		t.Fatal("before expiry", err)
	}
	if err := s.IssueOperatorRecovery(context.Background(), h[:], "2026-01-01T00:00:00Z", expiry); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeOperatorRecovery(context.Background(), h[:], expiry); err == nil {
		t.Fatal("at expiry accepted")
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

func TestLastOperatorCredentialCannotBeDeletedConcurrently(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.AddOperatorCredential(ctx, []byte("A"), []byte("data"), "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOperatorCredential(ctx, []byte("B"), []byte("data"), "", ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, id := range [][]byte{[]byte("A"), []byte("B")} {
		wg.Add(1)
		go func(id []byte) { defer wg.Done(); <-start; _ = s.DeleteOperatorCredential(ctx, id) }(id)
	}
	close(start)
	wg.Wait()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM operator_credentials`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("last credential deleted: %d", count)
	}
}
