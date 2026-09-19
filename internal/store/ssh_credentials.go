package store

import (
	"context"
	"database/sql"
)

type SSHCredential struct {
	ID, AccountID, CredentialType string
	PublicKey                     []byte
	Fingerprint, Label            string
	CreatedAt, RevokedAt          string
}

func (s *Store) CreateSSHCredential(ctx context.Context, c SSHCredential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_ssh_credentials(id,account_id,credential_type,public_key,fingerprint,label,created_at,revoked_at) VALUES(?,?,?,?,?,?,?,?)`, c.ID, c.AccountID, c.CredentialType, c.PublicKey, c.Fingerprint, c.Label, c.CreatedAt, c.RevokedAt)
	return err
}

func (s *Store) SSHCredentialByFingerprint(ctx context.Context, fingerprint string) (SSHCredential, error) {
	var c SSHCredential
	err := s.db.QueryRowContext(ctx, `SELECT id,account_id,credential_type,public_key,fingerprint,label,created_at,revoked_at FROM account_ssh_credentials WHERE fingerprint=?`, fingerprint).Scan(&c.ID, &c.AccountID, &c.CredentialType, &c.PublicKey, &c.Fingerprint, &c.Label, &c.CreatedAt, &c.RevokedAt)
	return c, err
}

func (s *Store) ListSSHCredentials(ctx context.Context, accountID string) ([]SSHCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,account_id,credential_type,public_key,fingerprint,label,created_at,revoked_at FROM account_ssh_credentials WHERE account_id=? ORDER BY created_at,id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SSHCredential
	for rows.Next() {
		var c SSHCredential
		if err := rows.Scan(&c.ID, &c.AccountID, &c.CredentialType, &c.PublicKey, &c.Fingerprint, &c.Label, &c.CreatedAt, &c.RevokedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *Store) RevokeSSHCredential(ctx context.Context, accountID, id, revokedAt string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE account_ssh_credentials SET revoked_at=? WHERE id=? AND account_id=? AND revoked_at=''`, revokedAt, id, accountID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
