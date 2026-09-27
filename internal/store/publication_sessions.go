package store

import (
	"context"
	"database/sql"
	"fmt"
)

type PublicationSession struct {
	ID, RepositoryID, OwnerAccountID, IncomingPath, State, CreatedAt string
	ExpectedCount, NextBatch                                         int64
}

func (s *Store) CreatePublicationSession(ctx context.Context, session PublicationSession, expected []PublicationArtifact) error {
	if session.ID == "" || session.ExpectedCount != int64(len(expected)) || len(expected) == 0 {
		return fmt.Errorf("invalid publication session manifest")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO publication_sessions(id,repository_id,owner_account_id,incoming_path,state,expected_count,created_at) VALUES(?,?,?,?,'open',?,?)`, session.ID, session.RepositoryID, session.OwnerAccountID, session.IncomingPath, session.ExpectedCount, session.CreatedAt); err != nil {
		return err
	}
	for _, artifact := range expected {
		if len(artifact.UploadedSHA256) != 64 || artifact.UploadedSize <= 0 {
			return fmt.Errorf("invalid expected publication artifact")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO publication_session_artifacts(session_id,filename,package,version,architecture,expected_size,expected_sha256) VALUES(?,?,?,?,?,?,?)`, session.ID, artifact.Filename, artifact.Package, artifact.Version, artifact.Architecture, artifact.UploadedSize, artifact.UploadedSHA256); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PublicationSession(ctx context.Context, id string) (PublicationSession, error) {
	var session PublicationSession
	err := s.db.QueryRowContext(ctx, `SELECT id,repository_id,owner_account_id,incoming_path,state,expected_count,next_batch,created_at FROM publication_sessions WHERE id=?`, id).Scan(&session.ID, &session.RepositoryID, &session.OwnerAccountID, &session.IncomingPath, &session.State, &session.ExpectedCount, &session.NextBatch, &session.CreatedAt)
	return session, err
}

func (s *Store) PublicationSessionBatchReplay(ctx context.Context, id, accountID string, batch int64, digest string) (bool, error) {
	var owner, state string
	var next int64
	if err := s.db.QueryRowContext(ctx, `SELECT owner_account_id,state,next_batch FROM publication_sessions WHERE id=?`, id).Scan(&owner, &state, &next); err != nil {
		return false, err
	}
	if owner != accountID || state != "open" {
		return false, fmt.Errorf("publication session is not open for this account")
	}
	if batch > next {
		return false, fmt.Errorf("publication batch out of sequence; expected %d", next)
	}
	if batch == next {
		return false, nil
	}
	var prior string
	if err := s.db.QueryRowContext(ctx, `SELECT digest FROM publication_session_batches WHERE session_id=? AND batch_number=?`, id, batch).Scan(&prior); err != nil {
		return false, err
	}
	if prior != digest {
		return false, fmt.Errorf("publication batch retry differs from accepted batch")
	}
	return true, nil
}

// RecordPublicationSessionBatch binds an accepted batch to server-measured
// artifact metadata. Replaying an already recorded batch is idempotent only
// when its canonical digest is identical.
func (s *Store) RecordPublicationSessionBatch(ctx context.Context, id, accountID string, batch int64, digest, createdAt string, artifacts []PublicationArtifact) error {
	if batch < 0 || len(digest) != 64 || len(artifacts) == 0 {
		return fmt.Errorf("invalid publication batch")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, state string
	var next int64
	if err = tx.QueryRowContext(ctx, `SELECT owner_account_id,state,next_batch FROM publication_sessions WHERE id=?`, id).Scan(&owner, &state, &next); err != nil {
		return err
	}
	if owner != accountID || state != "open" {
		return fmt.Errorf("publication session is not open for this account")
	}
	if batch != next {
		return fmt.Errorf("publication batch out of sequence; expected %d", next)
	}
	for _, artifact := range artifacts {
		result, updateErr := tx.ExecContext(ctx, `UPDATE publication_session_artifacts SET batch_number=? WHERE session_id=? AND filename=? AND package=? AND version=? AND architecture=? AND expected_size=? AND expected_sha256=? AND batch_number=-1`, batch, id, artifact.Filename, artifact.Package, artifact.Version, artifact.Architecture, artifact.UploadedSize, artifact.UploadedSHA256)
		if updateErr != nil {
			return updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count != 1 {
			return fmt.Errorf("uploaded package does not match the expected publication manifest: %s", artifact.Filename)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO publication_session_batches(session_id,batch_number,digest,artifact_count,created_at) VALUES(?,?,?,?,?)`, id, batch, digest, len(artifacts), createdAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE publication_sessions SET next_batch=next_batch+1 WHERE id=? AND state='open' AND next_batch=?`, id, next); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinalizePublicationSession(ctx context.Context, id, accountID, createdAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var repositoryID, incomingPath, state string
	var expected int64
	if err = tx.QueryRowContext(ctx, `SELECT repository_id,incoming_path,state,expected_count FROM publication_sessions WHERE id=? AND owner_account_id=?`, id, accountID).Scan(&repositoryID, &incomingPath, &state, &expected); err != nil {
		return err
	}
	if state != "open" {
		return fmt.Errorf("publication session is not open")
	}
	var received int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publication_session_artifacts WHERE session_id=? AND batch_number>=0`, id).Scan(&received); err != nil {
		return err
	}
	if received != expected {
		return fmt.Errorf("publication session is incomplete: received %d of %d packages", received, expected)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO publish_jobs(id,repository_id,publisher_account_id,state,incoming_path,created_at) VALUES(?,?,?,'queued',?,?)`, id, repositoryID, accountID, incomingPath, createdAt); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT filename,package,version,architecture,expected_size,expected_sha256 FROM publication_session_artifacts WHERE session_id=? ORDER BY filename`, id)
	if err != nil {
		return err
	}
	var artifacts []PublicationArtifact
	for rows.Next() {
		var a PublicationArtifact
		if err = rows.Scan(&a.Filename, &a.Package, &a.Version, &a.Architecture, &a.UploadedSize, &a.UploadedSHA256); err != nil {
			rows.Close()
			return err
		}
		artifacts = append(artifacts, a)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, a := range artifacts {
		if _, err = tx.ExecContext(ctx, `INSERT INTO publication_artifacts(job_id,filename,package,version,architecture,uploaded_size,uploaded_sha256) VALUES(?,?,?,?,?,?,?)`, id, a.Filename, a.Package, a.Version, a.Architecture, a.UploadedSize, a.UploadedSHA256); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM publication_session_artifacts WHERE session_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM publication_session_batches WHERE session_id=?`, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE publication_sessions SET state='finalized' WHERE id=? AND state='open'`, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OpenPublicationSessions(ctx context.Context) ([]PublicationSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,repository_id,owner_account_id,incoming_path,state,expected_count,next_batch,created_at FROM publication_sessions WHERE state='open' ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublicationSession
	for rows.Next() {
		var session PublicationSession
		if err = rows.Scan(&session.ID, &session.RepositoryID, &session.OwnerAccountID, &session.IncomingPath, &session.State, &session.ExpectedCount, &session.NextBatch, &session.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

func (s *Store) DeleteOpenPublicationSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM publication_sessions WHERE id=? AND state='open'`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
