//go:build qualificationhooks

package store

import (
	"context"
	"fmt"
	"time"
)

// QualificationCandidateReady proves the running application has opened and
// queried the migrated database. It is only used by the opt-in qualification hook.
func (s *Store) QualificationCandidateReady(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM tpahub_schema WHERE singleton=1`).Scan(&version); err != nil {
		return fmt.Errorf("query qualification schema: %w", err)
	}
	if version != currentSchemaVersion {
		return fmt.Errorf("qualification schema version %d, want %d", version, currentSchemaVersion)
	}
	return nil
}

// QualificationCandidateMutation is a disposable integration seam. It is only
// called by the qualification HTTP hook and uses the normal account persistence
// path after enabling WAL for the real SQLite connection.
func (s *Store) QualificationCandidateMutation(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable qualification WAL: %w", err)
	}
	return s.CreateAccount(ctx, Account{ID: "qualification-candidate", Email: "qualification-candidate@example.invalid", PasswordHash: "qualification", Status: AccountStatusActive, CreatedAt: now.UTC().Format(time.RFC3339Nano)})
}
