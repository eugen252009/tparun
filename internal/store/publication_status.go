package store

import "context"

// PublicationQueueStatus is an aggregate snapshot of persisted publication work.
// It deliberately omits tenant, repository, path, and artifact details.
type PublicationQueueStatus struct {
	QueuedJobs         int64
	RunningJobs        int64
	IncompleteSessions int64
}

// PublicationQueueStatus returns counts from the authoritative job and session
// tables. Only open sessions are incomplete; finalized and aborted sessions are
// terminal history and do not block full quiescence.
func (s *Store) PublicationQueueStatus(ctx context.Context) (PublicationQueueStatus, error) {
	var status PublicationQueueStatus
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM publish_jobs WHERE state='queued'),
			(SELECT COUNT(*) FROM publish_jobs WHERE state='running'),
			(SELECT COUNT(*) FROM publication_sessions WHERE state='open')
	`).Scan(&status.QueuedJobs, &status.RunningJobs, &status.IncompleteSessions)
	return status, err
}
