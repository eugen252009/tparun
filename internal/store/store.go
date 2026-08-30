package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Repository struct{ ID, AccountID, Name, Visibility, CreatedAt string }
type Job struct{ ID, RepositoryID, State, IncomingPath, Error, CreatedAt, StartedAt, FinishedAt string }
type Account struct{ ID, Email, PasswordHash, Status, CreatedAt string }
type Session struct{ ID, AccountID, TokenPrefix string }
type ReaderCredential struct{ ID, RepositoryID, Name, TokenPrefix, CreatedAt string }
type Store struct{ db *sql.DB }

var ErrNotFound = sql.ErrNoRows

func Open(dir string) (*Store, error) {
	if e := os.MkdirAll(dir, 0755); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	_, e = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT); CREATE TABLE IF NOT EXISTS repositories(id TEXT PRIMARY KEY,account_id TEXT, name TEXT NOT NULL,visibility TEXT NOT NULL DEFAULT 'public',created_at TEXT NOT NULL,UNIQUE(account_id,name),FOREIGN KEY(account_id) REFERENCES accounts(id)); CREATE TABLE IF NOT EXISTS publish_jobs(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),state TEXT NOT NULL,incoming_path TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT); CREATE TABLE IF NOT EXISTS reader_credentials(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),name TEXT NOT NULL,token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,revoked_at TEXT,UNIQUE(repository_id,name)); CREATE INDEX IF NOT EXISTS publish_jobs_queue ON publish_jobs(state,created_at); CREATE TABLE IF NOT EXISTS repository_egress_usage(repository_id TEXT NOT NULL REFERENCES repositories(id),period TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,period)); CREATE INDEX IF NOT EXISTS repository_egress_period ON repository_egress_usage(period);`)
	if e != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", e)
	}
	var hasOwner, hasVisibility bool
	rows, pe := db.Query(`PRAGMA table_info(repositories)`)
	if pe == nil {
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			_ = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk)
			if name == "account_id" {
				hasOwner = true
			}
			if name == "visibility" {
				hasVisibility = true
			}
		}
		rows.Close()
	}
	if !hasOwner {
		if _, e = db.Exec(`ALTER TABLE repositories ADD COLUMN account_id TEXT`); e != nil {
			db.Close()
			return nil, e
		}
	}
	if !hasVisibility {
		if _, e = db.Exec(`ALTER TABLE repositories ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'`); e != nil {
			db.Close()
			return nil, e
		}
	}
	_, e = db.Exec(`UPDATE publish_jobs SET state='failed',error='interrupted by service restart',finished_at=? WHERE state='running'`, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		db.Close()
	}
	return s, e
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) AddRepositoryEgress(c context.Context, repositoryID, period string, bytes int64, updatedAt string) error {
	if bytes <= 0 {
		return nil
	}
	_, e := s.db.ExecContext(c, `INSERT INTO repository_egress_usage(repository_id,period,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,period) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, period, bytes, updatedAt)
	return e
}
func (s *Store) AccountEgress(c context.Context, accountID, period string) (int64, error) {
	var total int64
	e := s.db.QueryRowContext(c, `SELECT COALESCE(SUM(u.egress_bytes),0) FROM repository_egress_usage u JOIN repositories r ON r.id=u.repository_id WHERE r.account_id=? AND u.period=?`, accountID, period).Scan(&total)
	return total, e
}
func (s *Store) RepositoryEgress(c context.Context, repositoryID, period string) (int64, error) {
	var total int64
	e := s.db.QueryRowContext(c, `SELECT COALESCE(egress_bytes,0) FROM repository_egress_usage WHERE repository_id=? AND period=?`, repositoryID, period).Scan(&total)
	if e == sql.ErrNoRows {
		return 0, nil
	}
	return total, e
}
func (s *Store) CreateAccount(c context.Context, a Account) error {
	_, e := s.db.ExecContext(c, "INSERT INTO accounts VALUES(?,?,?,?,?)", a.ID, a.Email, a.PasswordHash, a.Status, a.CreatedAt)
	return e
}
func (s *Store) AccountByID(c context.Context, id string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT id,email,password_hash,status,created_at FROM accounts WHERE id=?", id).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
	return a, e
}
func (s *Store) AccountByEmail(c context.Context, email string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT id,email,password_hash,status,created_at FROM accounts WHERE email=?", email).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
	return a, e
}
func (s *Store) CreateSession(c context.Context, x Session, hash string) error {
	_, e := s.db.ExecContext(c, "INSERT INTO sessions(id,account_id,token_prefix,token_hash,created_at) VALUES(?,?,?,?,?)", x.ID, x.AccountID, x.TokenPrefix, hash, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func (s *Store) AccountByToken(c context.Context, hash string) (string, error) {
	var id string
	e := s.db.QueryRowContext(c, "SELECT account_id FROM sessions WHERE token_hash=? AND revoked_at IS NULL", hash).Scan(&id)
	if e == nil {
		_, _ = s.db.ExecContext(c, "UPDATE sessions SET last_used_at=? WHERE token_hash=?", time.Now().UTC().Format(time.RFC3339Nano), hash)
	}
	return id, e
}
func (s *Store) RevokeSession(c context.Context, hash string) error {
	_, e := s.db.ExecContext(c, "UPDATE sessions SET revoked_at=? WHERE token_hash=?", time.Now().UTC().Format(time.RFC3339Nano), hash)
	return e
}
func HashToken(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func (s *Store) CreateRepository(c context.Context, r Repository) error {
	var owner any = r.AccountID
	if r.Visibility == "" {
		r.Visibility = "public"
	}
	if r.AccountID == "" {
		owner = nil
	}
	_, e := s.db.ExecContext(c, "INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES(?,?,?,?,?)", r.ID, owner, r.Name, r.Visibility, r.CreatedAt)
	return e
}
func (s *Store) Repository(c context.Context, id string) (Repository, error) {
	var r Repository
	e := s.db.QueryRowContext(c, "SELECT id,COALESCE(account_id,''),name,visibility,created_at FROM repositories WHERE id=?", id).Scan(&r.ID, &r.AccountID, &r.Name, &r.Visibility, &r.CreatedAt)
	return r, e
}
func (s *Store) OwnedRepository(c context.Context, account, idOrName string) (Repository, error) {
	var r Repository
	e := s.db.QueryRowContext(c, "SELECT id,COALESCE(account_id,''),name,visibility,created_at FROM repositories WHERE account_id=? AND (id=? OR name=?)", account, idOrName, idOrName).Scan(&r.ID, &r.AccountID, &r.Name, &r.Visibility, &r.CreatedAt)
	return r, e
}
func (s *Store) Repositories(c context.Context, account string) ([]Repository, error) {
	rows, e := s.db.QueryContext(c, "SELECT id,account_id,name,visibility,created_at FROM repositories WHERE account_id=? ORDER BY name", account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Repository
	for rows.Next() {
		var r Repository
		if e = rows.Scan(&r.ID, &r.AccountID, &r.Name, &r.Visibility, &r.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) CreateJob(c context.Context, j Job) error {
	_, e := s.db.ExecContext(c, "INSERT INTO publish_jobs(id,repository_id,state,incoming_path,created_at) VALUES(?,?,?,?,?)", j.ID, j.RepositoryID, j.State, j.IncomingPath, j.CreatedAt)
	return e
}
func (s *Store) Job(c context.Context, id string) (Job, error) {
	var j Job
	e := s.db.QueryRowContext(c, "SELECT id,repository_id,state,incoming_path,error,created_at,COALESCE(started_at,''),COALESCE(finished_at,'') FROM publish_jobs WHERE id=?", id).Scan(&j.ID, &j.RepositoryID, &j.State, &j.IncomingPath, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	return j, e
}
func (s *Store) Claim(c context.Context) (Job, error) {
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var j Job
	e = tx.QueryRowContext(c, "SELECT id,repository_id,state,incoming_path,error,created_at,COALESCE(started_at,''),COALESCE(finished_at,'') FROM publish_jobs WHERE state='queued' ORDER BY created_at LIMIT 1").Scan(&j.ID, &j.RepositoryID, &j.State, &j.IncomingPath, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if e != nil {
		return Job{}, e
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, e = tx.ExecContext(c, "UPDATE publish_jobs SET state='running',started_at=? WHERE id=?", now, j.ID); e != nil {
		return Job{}, e
	}
	if e = tx.Commit(); e != nil {
		return Job{}, e
	}
	j.State = "running"
	j.StartedAt = now
	return j, nil
}
func (s *Store) Finish(c context.Context, id, state, msg string) error {
	_, e := s.db.ExecContext(c, "UPDATE publish_jobs SET state=?,error=?,finished_at=? WHERE id=?", state, msg, time.Now().UTC().Format(time.RFC3339Nano), id)
	return e
}
func (s *Store) CreateCredential(c context.Context, x ReaderCredential, hash string) error {
	_, e := s.db.ExecContext(c, "INSERT INTO reader_credentials(id,repository_id,name,token_prefix,token_hash,created_at) VALUES(?,?,?,?,?,?)", x.ID, x.RepositoryID, x.Name, x.TokenPrefix, hash, x.CreatedAt)
	return e
}
func (s *Store) Credentials(c context.Context, account, repo string) ([]ReaderCredential, error) {
	rows, e := s.db.QueryContext(c, "SELECT c.id,c.repository_id,c.name,c.token_prefix,c.created_at FROM reader_credentials c JOIN repositories r ON r.id=c.repository_id WHERE r.account_id=? AND r.id=? AND c.revoked_at IS NULL", account, repo)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReaderCredential
	for rows.Next() {
		var x ReaderCredential
		if e = rows.Scan(&x.ID, &x.RepositoryID, &x.Name, &x.TokenPrefix, &x.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) RevokeCredential(c context.Context, account, repo, cid string) error {
	_, e := s.db.ExecContext(c, "UPDATE reader_credentials SET revoked_at=? WHERE id=? AND repository_id=? AND EXISTS(SELECT 1 FROM repositories WHERE id=? AND account_id=?)", time.Now().UTC().Format(time.RFC3339Nano), cid, repo, repo, account)
	return e
}
func (s *Store) ReaderRepository(c context.Context, repo, hash string) (bool, error) {
	var n int
	e := s.db.QueryRowContext(c, "SELECT 1 FROM reader_credentials WHERE repository_id=? AND token_hash=? AND revoked_at IS NULL", repo, hash).Scan(&n)
	return e == nil, e
}
