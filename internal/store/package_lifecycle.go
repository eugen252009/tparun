package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrLifecycleIdempotencyConflict = errors.New("package lifecycle idempotency key conflicts with a different request")
var ErrLifecycleParentChanged = errors.New("active repository generation changed")

type PackageLifecycleJob struct {
	JobID, RepositoryID, ActorAccountID, IdempotencyKey string
	Operation, Package, Version, Architecture           string
	Confirmed                                           bool
	GenerationBefore, GenerationAfter                   string
	ArtifactFilename, ArtifactSHA256                    string
	ArtifactSize                                        int64
	MetadataState, ArtifactState, Result                string
}

// CreatePackageLifecycleJob atomically records the auditable job and takes the
// repository publication slot. The active generation is compared in the same
// transaction so the caller cannot base a mutation on a stale snapshot.
func (s *Store) CreatePackageLifecycleJob(ctx context.Context, job Job, lifecycle PackageLifecycleJob, expectedParent, ownerAccountID string) (PackageLifecycleJob, bool, error) {
	if job.ID == "" || job.RepositoryID == "" || job.PublisherAccountID == "" || job.State != "running" || job.IncomingPath == "" ||
		lifecycle.JobID != job.ID || lifecycle.RepositoryID != job.RepositoryID || lifecycle.ActorAccountID != job.PublisherAccountID ||
		len(lifecycle.IdempotencyKey) != 32 || (lifecycle.Operation != "unlist" && lifecycle.Operation != "delete") ||
		lifecycle.Confirmed != (lifecycle.Operation == "delete") || lifecycle.Package == "" || lifecycle.Version == "" || lifecycle.Architecture == "" ||
		!strings.HasPrefix(lifecycle.ArtifactFilename, "pool/") || strings.Contains(lifecycle.ArtifactFilename, "\\") || strings.Contains(lifecycle.ArtifactFilename, "../") ||
		lifecycle.ArtifactSize <= 0 || len(lifecycle.ArtifactSHA256) != 64 || lifecycle.MetadataState != "pending" || lifecycle.ArtifactState != "pending" {
		return PackageLifecycleJob{}, false, fmt.Errorf("invalid package lifecycle job")
	}
	if _, err := hex.DecodeString(lifecycle.IdempotencyKey); err != nil || lifecycle.IdempotencyKey != strings.ToLower(lifecycle.IdempotencyKey) {
		return PackageLifecycleJob{}, false, fmt.Errorf("invalid package lifecycle idempotency key")
	}
	if digest, err := hex.DecodeString(lifecycle.ArtifactSHA256); err != nil || len(digest) != 32 || lifecycle.ArtifactSHA256 != strings.ToLower(lifecycle.ArtifactSHA256) {
		return PackageLifecycleJob{}, false, fmt.Errorf("invalid package lifecycle artifact digest")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PackageLifecycleJob{}, false, err
	}
	defer tx.Rollback()
	if prior, lookupErr := packageLifecycleByKeyTx(ctx, tx, lifecycle.RepositoryID, lifecycle.ActorAccountID, lifecycle.IdempotencyKey); lookupErr == nil {
		if !sameLifecycleRequest(prior, lifecycle) {
			return PackageLifecycleJob{}, false, ErrLifecycleIdempotencyConflict
		}
		return prior, true, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return PackageLifecycleJob{}, false, lookupErr
	}
	var owner, active string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(r.account_id,''),COALESCE((SELECT p.active_generation FROM repository_publications p WHERE p.repository_id=r.id),'') FROM repositories r WHERE r.id=?`, job.RepositoryID).Scan(&owner, &active); err != nil {
		return PackageLifecycleJob{}, false, err
	}
	if owner == "" || owner != ownerAccountID {
		return PackageLifecycleJob{}, false, fmt.Errorf("repository owner changed before lifecycle job creation")
	}
	if active != expectedParent {
		return PackageLifecycleJob{}, false, ErrLifecycleParentChanged
	}
	claim, err := tx.ExecContext(ctx, `INSERT INTO repository_publication_slots(repository_id,publication_id,claimed_at)
		SELECT ?,?,? WHERE NOT EXISTS (SELECT 1 FROM repository_publication_slots WHERE repository_id=?)
		AND NOT EXISTS (SELECT 1 FROM publish_jobs WHERE repository_id=? AND state NOT IN ('succeeded','failed'))
		AND NOT EXISTS (SELECT 1 FROM publication_sessions WHERE repository_id=? AND state='open')`,
		job.RepositoryID, job.ID, job.CreatedAt, job.RepositoryID, job.RepositoryID, job.RepositoryID)
	if err != nil {
		return PackageLifecycleJob{}, false, err
	}
	rows, err := claim.RowsAffected()
	if err != nil {
		return PackageLifecycleJob{}, false, err
	}
	if rows != 1 {
		return PackageLifecycleJob{}, false, ErrRepositoryBusy
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO publish_jobs(id,repository_id,publisher_account_id,state,incoming_path,created_at,started_at) VALUES(?,?,?,'running',?,?,?)`, job.ID, job.RepositoryID, job.PublisherAccountID, job.IncomingPath, job.CreatedAt, job.StartedAt); err != nil {
		return PackageLifecycleJob{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO package_lifecycle_jobs(job_id,repository_id,actor_account_id,idempotency_key,operation,package,version,architecture,confirmed,generation_before,artifact_filename,artifact_size,artifact_sha256,metadata_state,artifact_state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'pending','pending',?)`,
		lifecycle.JobID, lifecycle.RepositoryID, lifecycle.ActorAccountID, lifecycle.IdempotencyKey, lifecycle.Operation, lifecycle.Package, lifecycle.Version, lifecycle.Architecture, lifecycle.Confirmed, expectedParent, lifecycle.ArtifactFilename, lifecycle.ArtifactSize, lifecycle.ArtifactSHA256, job.CreatedAt); err != nil {
		return PackageLifecycleJob{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return PackageLifecycleJob{}, false, err
	}
	lifecycle.GenerationBefore = expectedParent
	return lifecycle, false, nil
}

func sameLifecycleRequest(a, b PackageLifecycleJob) bool {
	return a.Operation == b.Operation && a.Package == b.Package && a.Version == b.Version && a.Architecture == b.Architecture && a.Confirmed == b.Confirmed
}

func packageLifecycleByKeyTx(ctx context.Context, tx *sql.Tx, repositoryID, actorID, key string) (PackageLifecycleJob, error) {
	return scanPackageLifecycle(tx.QueryRowContext(ctx, `SELECT job_id,repository_id,actor_account_id,idempotency_key,operation,package,version,architecture,confirmed,generation_before,generation_after,artifact_filename,artifact_size,artifact_sha256,metadata_state,artifact_state,result FROM package_lifecycle_jobs WHERE repository_id=? AND actor_account_id=? AND idempotency_key=?`, repositoryID, actorID, key))
}

func (s *Store) PackageLifecycleByIdempotency(ctx context.Context, repositoryID, actorID, key string) (PackageLifecycleJob, error) {
	return scanPackageLifecycle(s.db.QueryRowContext(ctx, `SELECT job_id,repository_id,actor_account_id,idempotency_key,operation,package,version,architecture,confirmed,generation_before,generation_after,artifact_filename,artifact_size,artifact_sha256,metadata_state,artifact_state,result FROM package_lifecycle_jobs WHERE repository_id=? AND actor_account_id=? AND idempotency_key=?`, repositoryID, actorID, key))
}

func (s *Store) PackageLifecycle(ctx context.Context, jobID string) (PackageLifecycleJob, error) {
	return scanPackageLifecycle(s.db.QueryRowContext(ctx, `SELECT job_id,repository_id,actor_account_id,idempotency_key,operation,package,version,architecture,confirmed,generation_before,generation_after,artifact_filename,artifact_size,artifact_sha256,metadata_state,artifact_state,result FROM package_lifecycle_jobs WHERE job_id=?`, jobID))
}

func scanPackageLifecycle(row *sql.Row) (PackageLifecycleJob, error) {
	var lifecycle PackageLifecycleJob
	var confirmed int
	err := row.Scan(&lifecycle.JobID, &lifecycle.RepositoryID, &lifecycle.ActorAccountID, &lifecycle.IdempotencyKey, &lifecycle.Operation, &lifecycle.Package, &lifecycle.Version, &lifecycle.Architecture, &confirmed, &lifecycle.GenerationBefore, &lifecycle.GenerationAfter, &lifecycle.ArtifactFilename, &lifecycle.ArtifactSize, &lifecycle.ArtifactSHA256, &lifecycle.MetadataState, &lifecycle.ArtifactState, &lifecycle.Result)
	lifecycle.Confirmed = confirmed != 0
	return lifecycle, err
}

func (s *Store) PackageLifecycleGeneration(ctx context.Context, repositoryID, generation string) (PackageLifecycleJob, error) {
	return scanPackageLifecycle(s.db.QueryRowContext(ctx, `SELECT job_id,repository_id,actor_account_id,idempotency_key,operation,package,version,architecture,confirmed,generation_before,generation_after,artifact_filename,artifact_size,artifact_sha256,metadata_state,artifact_state,result FROM package_lifecycle_jobs WHERE repository_id=? AND generation_after=?`, repositoryID, generation))
}

// CommitPackageLifecycleActivation makes the new immutable generation active,
// persists the lifecycle/audit result, marks the observed job outcome, and
// releases the exact repository slot as one SQLite transaction.
func (s *Store) CommitPackageLifecycleActivation(ctx context.Context, repositoryID, jobID, ownerAccountID, expectedParent, metadataState, artifactState, resultText, jobError, finishedAt string, succeeded bool) error {
	if metadataState != "published" && metadataState != "unchanged" {
		return fmt.Errorf("invalid lifecycle metadata state")
	}
	if artifactState != "retained" && artifactState != "deleted" && artifactState != "cleanup_failed" {
		return fmt.Errorf("invalid lifecycle artifact state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, active, slot string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(r.account_id,''),COALESCE((SELECT p.active_generation FROM repository_publications p WHERE p.repository_id=r.id),''),COALESCE((SELECT publication_id FROM repository_publication_slots WHERE repository_id=r.id),'') FROM repositories r WHERE r.id=?`, repositoryID).Scan(&owner, &active, &slot); err != nil {
		return err
	}
	if owner == "" || owner != ownerAccountID {
		return fmt.Errorf("lifecycle repository owner changed before commit")
	}
	if active != expectedParent {
		return fmt.Errorf("lifecycle parent changed before commit")
	}
	if slot != jobID {
		return fmt.Errorf("lifecycle repository claim changed before commit")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES(?,?,?) ON CONFLICT(repository_id) DO UPDATE SET active_generation=excluded.active_generation,updated_at=excluded.updated_at`, repositoryID, jobID, finishedAt); err != nil {
		return err
	}
	state := "succeeded"
	if !succeeded {
		state = "failed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE publish_jobs SET state=?,error=?,finished_at=?,cleanup_state='pending',cleanup_error='',generation_id=?,verification_state='match',verified_at=?,verification_error='' WHERE id=? AND repository_id=? AND state='running'`, state, jobError, finishedAt, jobID, finishedAt, jobID, repositoryID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("lifecycle job state changed before activation")
	}
	result, err = tx.ExecContext(ctx, `UPDATE package_lifecycle_jobs SET generation_after=?,metadata_state=?,artifact_state=?,result=? WHERE job_id=? AND generation_after='' AND metadata_state='pending' AND artifact_state='pending'`, jobID, metadataState, artifactState, resultText, jobID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("lifecycle audit state changed before activation")
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM repository_publication_slots WHERE repository_id=? AND publication_id=?`, repositoryID, jobID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("lifecycle repository slot changed before activation")
	}
	return tx.Commit()
}

func (s *Store) SetPackageLifecycleDiagnostic(ctx context.Context, jobID, diagnostic string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE publish_jobs SET error=? WHERE id=? AND state='running' AND EXISTS (SELECT 1 FROM package_lifecycle_jobs WHERE job_id=?)`, diagnostic, jobID, jobID)
	return err
}

func (s *Store) FailPackageLifecycle(ctx context.Context, jobID, repositoryID, errorText, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE publish_jobs SET state='failed',error=?,finished_at=?,cleanup_state='pending',cleanup_error='' WHERE id=? AND repository_id=? AND state='running'`, errorText, finishedAt, jobID, repositoryID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("lifecycle job state changed before failure recording")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE package_lifecycle_jobs SET metadata_state='unchanged',artifact_state='retained',result='failed' WHERE job_id=? AND generation_after=''`, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM repository_publication_slots WHERE repository_id=? AND publication_id=?`, repositoryID, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LifecycleGenerationHistory(ctx context.Context, repositoryID, activeGeneration string) ([]PackageLifecycleJob, error) {
	var history []PackageLifecycleJob
	seen := map[string]bool{}
	for generation := activeGeneration; generation != ""; {
		if seen[generation] {
			return nil, fmt.Errorf("package lifecycle generation history contains a cycle")
		}
		seen[generation] = true
		entry, err := s.PackageLifecycleGeneration(ctx, repositoryID, generation)
		if errors.Is(err, sql.ErrNoRows) {
			break // A normal publication is the boundary of lifecycle state.
		}
		if err != nil {
			return nil, err
		}
		history = append(history, entry)
		generation = entry.GenerationBefore
	}
	return history, nil
}
