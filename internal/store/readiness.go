package store

import (
	"context"
	"fmt"
)

// Ready probes the persistent store opened and validated by Open. It is not an
// integrity scan, signing check, disk-capacity promise or migration operation.
func (s *Store) Ready(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM tpahub_schema WHERE singleton=1`).Scan(&version); err != nil {
		return err
	}
	if version != CurrentSchemaVersion() {
		return fmt.Errorf("persistent state version changed")
	}
	return nil
}
