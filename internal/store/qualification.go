package store

import (
	"context"
	"fmt"
	"time"
)

// QualificationCandidateMutation is a disposable integration seam. It is only
// called by the opt-in qualification HTTP hook and uses the normal account
// persistence path after enabling WAL for the real SQLite connection.
func (s *Store) QualificationCandidateMutation(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable qualification WAL: %w", err)
	}
	return s.CreateAccount(ctx, Account{ID: "qualification-candidate", Email: "qualification-candidate@example.invalid", PasswordHash: "qualification", Status: AccountStatusActive, CreatedAt: now.UTC().Format(time.RFC3339Nano)})
}
