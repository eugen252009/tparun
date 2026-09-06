package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

type OperatorCredentialRecord struct {
	ID         []byte
	Data       []byte
	Label      string
	CreatedAt  string
	LastUsedAt string
}

type OperatorRecoveryRecord struct {
	TokenHash            []byte
	CreatedAt, ExpiresAt string
}

func (s *Store) OperatorUserHandle(ctx context.Context) ([]byte, error) {
	var handle []byte
	err := s.db.QueryRowContext(ctx, `SELECT user_handle FROM operator_identity WHERE id=1`).Scan(&handle)
	if err == nil {
		return handle, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	handle = make([]byte, 32)
	if _, err = rand.Read(handle); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO operator_identity(id,user_handle) VALUES(1,?)`, handle)
	if err != nil {
		return nil, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT user_handle FROM operator_identity WHERE id=1`).Scan(&handle); err != nil {
		return nil, err
	}
	return handle, nil
}

func (s *Store) OperatorCredentials(ctx context.Context) ([]OperatorCredentialRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT credential_id,credential_data,label,created_at,last_used_at FROM operator_credentials ORDER BY created_at,credential_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperatorCredentialRecord
	for rows.Next() {
		var c OperatorCredentialRecord
		if err := rows.Scan(&c.ID, &c.Data, &c.Label, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddOperatorCredential(ctx context.Context, id, data []byte, label, createdAt string) error {
	if len(id) == 0 || len(data) == 0 {
		return fmt.Errorf("operator credential is empty")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO operator_credentials(credential_id,credential_data,label,created_at) VALUES(?,?,?,?)`, id, data, label, createdAt)
	return err
}

func (s *Store) TouchOperatorCredential(ctx context.Context, id, data []byte, usedAt string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE operator_credentials SET credential_data=?,last_used_at=? WHERE credential_id=?`, data, usedAt, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteOperatorCredential(ctx context.Context, id []byte) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM operator_credentials WHERE credential_id=? AND (SELECT COUNT(*) FROM operator_credentials)>1`, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func migrateOperatorRecoveryTimes(db *sql.DB) error {
	if err := ensureColumn(db, "operator_recovery", "expires_at_unix_nanos", "expires_at_unix_nanos INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT id,expires_at,expires_at_unix_nanos FROM operator_recovery`)
	if err != nil {
		return err
	}
	type row struct {
		id    int
		text  string
		nanos int64
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text, &r.nanos); err != nil {
			return err
		}
		if r.nanos == 0 {
			t, e := time.Parse(time.RFC3339Nano, r.text)
			if e != nil {
				pending = append(pending, row{id: r.id, nanos: -1})
			} else {
				pending = append(pending, row{id: r.id, nanos: t.UnixNano()})
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range pending {
		if r.nanos < 0 {
			if _, err := db.Exec(`DELETE FROM operator_recovery WHERE id=?`, r.id); err != nil {
				return err
			}
		} else if _, err := db.Exec(`UPDATE operator_recovery SET expires_at_unix_nanos=? WHERE id=?`, r.nanos, r.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) IssueOperatorRecovery(ctx context.Context, hash []byte, createdAt, expiresAt string) error {
	if len(hash) == 0 || createdAt == "" || expiresAt == "" {
		return fmt.Errorf("invalid operator recovery")
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return fmt.Errorf("invalid recovery expiry: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM operator_recovery`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operator_recovery(id,token_hash,created_at,expires_at,expires_at_unix_nanos) VALUES(1,?,?,?,?)`, hash, createdAt, expiresAt, expiry.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeOperatorRecovery(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM operator_recovery`)
	return err
}

func (s *Store) ConsumeOperatorRecovery(ctx context.Context, hash []byte, now string) error {
	current, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return fmt.Errorf("invalid recovery time: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM operator_recovery WHERE id=1 AND token_hash=? AND expires_at_unix_nanos>?`, hash, current.UnixNano())
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM operator_recovery WHERE id=1 AND expires_at_unix_nanos<=?`, current.UnixNano())
		return fmt.Errorf("invalid or expired recovery code")
	}
	return nil
}

type OperatorRecoveryInfo struct{ CreatedAt, ExpiresAt string }

func (s *Store) OperatorRecoveryInfo(ctx context.Context) (OperatorRecoveryInfo, error) {
	var out OperatorRecoveryInfo
	err := s.db.QueryRowContext(ctx, `SELECT created_at,expires_at FROM operator_recovery WHERE id=1`).Scan(&out.CreatedAt, &out.ExpiresAt)
	return out, err
}

func ParseOperatorTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
