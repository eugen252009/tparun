package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *Store) CreateJobWithArtifacts(ctx context.Context, job Job, artifacts []PublicationArtifact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO publish_jobs(id,repository_id,publisher_account_id,state,incoming_path,created_at) VALUES(?,?,?,?,?,?)`, job.ID, job.RepositoryID, job.PublisherAccountID, job.State, job.IncomingPath, job.CreatedAt); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if len(artifact.UploadedSHA256) != 64 {
			return fmt.Errorf("invalid uploaded SHA256")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO publication_artifacts(job_id,filename,package,version,architecture,uploaded_size,uploaded_sha256) VALUES(?,?,?,?,?,?,?)`, job.ID, artifact.Filename, artifact.Package, artifact.Version, artifact.Architecture, artifact.UploadedSize, artifact.UploadedSHA256); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PublicationArtifacts(ctx context.Context, jobID string) ([]PublicationArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT filename,package,version,architecture,uploaded_size,uploaded_sha256,published_filename,published_size,published_sha256,verification_state FROM publication_artifacts WHERE job_id=? ORDER BY filename`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublicationArtifact
	for rows.Next() {
		var a PublicationArtifact
		if err := rows.Scan(&a.Filename, &a.Package, &a.Version, &a.Architecture, &a.UploadedSize, &a.UploadedSHA256, &a.PublishedFilename, &a.PublishedSize, &a.PublishedSHA256, &a.VerificationState); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) RecordPublicationVerification(ctx context.Context, jobID, generationID, verificationError string, artifacts []PublicationArtifact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := "match"
	if verificationError != "" {
		state = "mismatch"
	}
	for _, a := range artifacts {
		result, err := tx.ExecContext(ctx, `UPDATE publication_artifacts SET published_filename=?,published_size=?,published_sha256=?,verification_state=? WHERE job_id=? AND filename=?`, a.PublishedFilename, a.PublishedSize, a.PublishedSHA256, state, jobID, a.Filename)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return sql.ErrNoRows
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE publish_jobs SET generation_id=?,verification_state=?,verification_error=?,verified_at=? WHERE id=?`, generationID, state, verificationError, time.Now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
