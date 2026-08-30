package store

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Repository struct{ ID, Name, CreatedAt string }
type Job struct{ ID, RepositoryID, State, IncomingPath, Error, CreatedAt, StartedAt, FinishedAt string }

type Store struct{ db *sql.DB }

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "tpahub.db"))
	if err != nil {
		return nil, err
	}
	s := &Store{db}
	_, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS repositories (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS publish_jobs (id TEXT PRIMARY KEY, repository_id TEXT NOT NULL REFERENCES repositories(id), state TEXT NOT NULL, incoming_path TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT); CREATE INDEX IF NOT EXISTS publish_jobs_queue ON publish_jobs(state, created_at);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	// A process cannot safely resume a child process from a previous run.
	// Queued jobs remain available; jobs claimed by the old process are
	// deterministically marked failed and can be retried by a new upload.
	if _, err := db.Exec(`UPDATE publish_jobs SET state='failed', error='interrupted by service restart', finished_at=? WHERE state='running'`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover publish jobs: %w", err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) CreateRepository(ctx context.Context, r Repository) error {
	_, e := s.db.ExecContext(ctx, "INSERT INTO repositories(id,name,created_at) VALUES(?,?,?)", r.ID, r.Name, r.CreatedAt)
	return e
}
func (s *Store) Repository(ctx context.Context, id string) (Repository, error) {
	var r Repository
	e := s.db.QueryRowContext(ctx, "SELECT id,name,created_at FROM repositories WHERE id=?", id).Scan(&r.ID, &r.Name, &r.CreatedAt)
	return r, e
}
func (s *Store) CreateJob(ctx context.Context, j Job) error {
	_, e := s.db.ExecContext(ctx, "INSERT INTO publish_jobs(id,repository_id,state,incoming_path,created_at) VALUES(?,?,?,?,?)", j.ID, j.RepositoryID, j.State, j.IncomingPath, j.CreatedAt)
	return e
}
func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	var j Job
	e := s.db.QueryRowContext(ctx, "SELECT id,repository_id,state,incoming_path,error,created_at,COALESCE(started_at,''),COALESCE(finished_at,'') FROM publish_jobs WHERE id=?", id).Scan(&j.ID, &j.RepositoryID, &j.State, &j.IncomingPath, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	return j, e
}
func (s *Store) Claim(ctx context.Context) (Job, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var j Job
	e = tx.QueryRowContext(ctx, "SELECT id,repository_id,state,incoming_path,error,created_at,COALESCE(started_at,''),COALESCE(finished_at,'') FROM publish_jobs WHERE state='queued' ORDER BY created_at LIMIT 1").Scan(&j.ID, &j.RepositoryID, &j.State, &j.IncomingPath, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if e != nil {
		return Job{}, e
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, e = tx.ExecContext(ctx, "UPDATE publish_jobs SET state='running',started_at=? WHERE id=? AND state='queued'", now, j.ID); e != nil {
		return Job{}, e
	}
	if e = tx.Commit(); e != nil {
		return Job{}, e
	}
	j.State = "running"
	j.StartedAt = now
	return j, nil
}
func (s *Store) Finish(ctx context.Context, id, state, msg string) error {
	_, e := s.db.ExecContext(ctx, "UPDATE publish_jobs SET state=?,error=?,finished_at=? WHERE id=?", state, msg, time.Now().UTC().Format(time.RFC3339Nano), id)
	return e
}

var ErrNotFound = sql.ErrNoRows
