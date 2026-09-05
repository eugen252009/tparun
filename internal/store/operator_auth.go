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
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_credentials`).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return fmt.Errorf("cannot remove the last operator credential")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM operator_credentials WHERE credential_id=?`, id)
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

func (s *Store) IssueOperatorRecovery(ctx context.Context, hash []byte, createdAt, expiresAt string) error {
	if len(hash) == 0 || createdAt == "" || expiresAt == "" {
		return fmt.Errorf("invalid operator recovery")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM operator_recovery`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operator_recovery(id,token_hash,created_at,expires_at) VALUES(1,?,?,?)`, hash, createdAt, expiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeOperatorRecovery(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM operator_recovery`)
	return err
}

func (s *Store) ConsumeOperatorRecovery(ctx context.Context, hash []byte, now string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM operator_recovery WHERE id=1 AND token_hash=? AND expires_at>?`, hash, now)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM operator_recovery WHERE id=1 AND expires_at<=?`, now)
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
