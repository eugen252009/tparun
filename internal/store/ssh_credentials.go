package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

type SSHCredential struct {
	ID, AccountID, CredentialType string
	PublicKey                     []byte
	Fingerprint, Label            string
	Flags                         CredentialFlags
	CreatedAt, RevokedAt          string
}
type SSHCredentialAudit struct {
	ID, AccountID, CredentialID, Action, CreatedAt string
	OldFlags, NewFlags                             CredentialFlags
}

func newStoreID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func (s *Store) CreateSSHCredential(ctx context.Context, c SSHCredential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_ssh_credentials(id,account_id,credential_type,public_key,fingerprint,label,flags,created_at,revoked_at) VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, c.AccountID, c.CredentialType, c.PublicKey, c.Fingerprint, c.Label, uint64(c.Flags), c.CreatedAt, c.RevokedAt)
	return err
}
func (s *Store) CreateSSHCredentialAudited(ctx context.Context, c SSHCredential) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_ssh_credentials(id,account_id,credential_type,public_key,fingerprint,label,flags,created_at,revoked_at) VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, c.AccountID, c.CredentialType, c.PublicKey, c.Fingerprint, c.Label, uint64(c.Flags), c.CreatedAt, c.RevokedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_ssh_credential_audit(id,account_id,credential_id,old_flags,new_flags,action,created_at) VALUES(?,?,?,?,?,?,?)`, newStoreID(), c.AccountID, c.ID, 0, uint64(c.Flags), "credential registered", c.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SSHCredentialByFingerprint(ctx context.Context, fingerprint string) (SSHCredential, error) {
	var c SSHCredential
	err := s.db.QueryRowContext(ctx, `SELECT id,account_id,credential_type,public_key,fingerprint,label,flags,created_at,revoked_at FROM account_ssh_credentials WHERE fingerprint=?`, fingerprint).Scan(&c.ID, &c.AccountID, &c.CredentialType, &c.PublicKey, &c.Fingerprint, &c.Label, &c.Flags, &c.CreatedAt, &c.RevokedAt)
	return c, err
}
func (s *Store) SSHCredentialByID(ctx context.Context, accountID, id string) (SSHCredential, error) {
	var c SSHCredential
	err := s.db.QueryRowContext(ctx, `SELECT id,account_id,credential_type,public_key,fingerprint,label,flags,created_at,revoked_at FROM account_ssh_credentials WHERE account_id=? AND id=?`, accountID, id).Scan(&c.ID, &c.AccountID, &c.CredentialType, &c.PublicKey, &c.Fingerprint, &c.Label, &c.Flags, &c.CreatedAt, &c.RevokedAt)
	return c, err
}
func (s *Store) ListSSHCredentials(ctx context.Context, accountID string) ([]SSHCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,account_id,credential_type,public_key,fingerprint,label,flags,created_at,revoked_at FROM account_ssh_credentials WHERE account_id=? ORDER BY created_at,id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SSHCredential
	for rows.Next() {
		var c SSHCredential
		if err := rows.Scan(&c.ID, &c.AccountID, &c.CredentialType, &c.PublicKey, &c.Fingerprint, &c.Label, &c.Flags, &c.CreatedAt, &c.RevokedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *Store) UpdateSSHCredential(ctx context.Context, accountID, id, label string, flags CredentialFlags) error {
	res, err := s.db.ExecContext(ctx, `UPDATE account_ssh_credentials SET label=?,flags=? WHERE account_id=? AND id=? AND revoked_at=''`, label, uint64(flags), accountID, id)
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
func (s *Store) UpdateSSHCredentialAudited(ctx context.Context, accountID, id, label string, flags CredentialFlags, action, at string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old uint64
	if err = tx.QueryRowContext(ctx, `SELECT flags FROM account_ssh_credentials WHERE account_id=? AND id=? AND revoked_at=''`, accountID, id).Scan(&old); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE account_ssh_credentials SET label=?,flags=? WHERE account_id=? AND id=? AND revoked_at=''`, label, uint64(flags), accountID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_ssh_credential_audit(id,account_id,credential_id,old_flags,new_flags,action,created_at) VALUES(?,?,?,?,?,?,?)`, newStoreID(), accountID, id, old, uint64(flags), action, at); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RevokeSSHCredentialAudited(ctx context.Context, accountID, id, at string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old uint64
	if err = tx.QueryRowContext(ctx, `SELECT flags FROM account_ssh_credentials WHERE account_id=? AND id=? AND revoked_at=''`, accountID, id).Scan(&old); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE account_ssh_credentials SET revoked_at=? WHERE account_id=? AND id=? AND revoked_at=''`, at, accountID, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_ssh_credential_audit(id,account_id,credential_id,old_flags,new_flags,action,created_at) VALUES(?,?,?,?,?,?,?)`, newStoreID(), accountID, id, old, old, "credential revoked", at); err != nil {
		return err
	}
	return tx.Commit()
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
func (s *Store) CreateSSHCredentialAudit(ctx context.Context, a SSHCredentialAudit) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_ssh_credential_audit(id,account_id,credential_id,old_flags,new_flags,action,created_at) VALUES(?,?,?,?,?,?,?)`, a.ID, a.AccountID, a.CredentialID, uint64(a.OldFlags), uint64(a.NewFlags), a.Action, a.CreatedAt)
	return err
}
func (s *Store) SSHCredentialAuditCount(ctx context.Context, credentialID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_ssh_credential_audit WHERE credential_id=?`, credentialID).Scan(&n)
	return n, err
}
