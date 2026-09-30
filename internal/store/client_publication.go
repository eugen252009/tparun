package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ClaimRepositoryPublication atomically reserves a repository before upload
// admission. The primary key is the durable per-repository serialization gate.
func (s *Store) ClaimRepositoryPublication(ctx context.Context, repositoryID, publicationID, claimedAt string) error {
	if repositoryID == "" || publicationID == "" || claimedAt == "" {
		return fmt.Errorf("invalid repository publication claim")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO repository_publication_slots(repository_id,publication_id,claimed_at)
		SELECT ?,?,? WHERE NOT EXISTS (SELECT 1 FROM repository_publication_slots WHERE repository_id=?)
		AND NOT EXISTS (SELECT 1 FROM publish_jobs WHERE repository_id=? AND state NOT IN ('succeeded','failed'))
		AND NOT EXISTS (SELECT 1 FROM publication_sessions WHERE repository_id=? AND state='open')`,
		repositoryID, publicationID, claimedAt, repositoryID, repositoryID, repositoryID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrRepositoryBusy
	}
	return nil
}

// ReleaseRepositoryPublication only releases the caller's own claim.
func (s *Store) ReleaseRepositoryPublication(ctx context.Context, repositoryID, publicationID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repository_publication_slots WHERE repository_id=? AND publication_id=?`, repositoryID, publicationID)
	return err
}

func (s *Store) RegisterPrebuiltPublication(ctx context.Context, job Job, expectedParent, signerFingerprint string, manifest []byte) error {
	if job.ID == "" || job.RepositoryID == "" || job.State != "running" || signerFingerprint == "" || len(manifest) == 0 {
		return fmt.Errorf("invalid prebuilt publication")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO publish_jobs(id,repository_id,publisher_account_id,state,incoming_path,created_at) VALUES(?,?,?,'running',?,?)`, job.ID, job.RepositoryID, job.PublisherAccountID, job.IncomingPath, job.CreatedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO prebuilt_publications(job_id,expected_parent,apt_signer_fingerprint,manifest_json) VALUES(?,?,?,?)`, job.ID, expectedParent, signerFingerprint, manifest); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PrebuiltPublication(ctx context.Context, jobID string) (string, string, []byte, error) {
	var parent, fingerprint string
	var manifest []byte
	err := s.db.QueryRowContext(ctx, `SELECT expected_parent,apt_signer_fingerprint,manifest_json FROM prebuilt_publications WHERE job_id=?`, jobID).Scan(&parent, &fingerprint, &manifest)
	if err == sql.ErrNoRows {
		return "", "", nil, ErrNotFound
	}
	return parent, fingerprint, manifest, err
}
