package store

import (
	"context"
	"fmt"
	"strings"
)

func (s *Store) AddRepositoryAPTSigner(ctx context.Context, signer RepositoryAPTSigner) error {
	fingerprint := strings.ToUpper(signer.Fingerprint)
	if signer.RepositoryID == "" || len(fingerprint) != 40 && len(fingerprint) != 64 || signer.PublicKey == nil || len(signer.PublicKey) == 0 || signer.CreatedAt == "" {
		return fmt.Errorf("invalid repository APT signer")
	}
	for _, r := range fingerprint {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'F') {
			return fmt.Errorf("invalid repository APT signer fingerprint")
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO repository_apt_signers(repository_id,fingerprint,public_key,created_at,revoked_at) VALUES(?,?,?,?, '')`, signer.RepositoryID, fingerprint, signer.PublicKey, signer.CreatedAt)
	return err
}

func (s *Store) RepositoryAPTSigners(ctx context.Context, repositoryID string) ([]RepositoryAPTSigner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id,fingerprint,public_key,created_at,revoked_at FROM repository_apt_signers WHERE repository_id=? AND revoked_at='' ORDER BY fingerprint`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var signers []RepositoryAPTSigner
	for rows.Next() {
		var signer RepositoryAPTSigner
		if err = rows.Scan(&signer.RepositoryID, &signer.Fingerprint, &signer.PublicKey, &signer.CreatedAt, &signer.RevokedAt); err != nil {
			return nil, err
		}
		signers = append(signers, signer)
	}
	return signers, rows.Err()
}

func (s *Store) RevokeRepositoryAPTSigner(ctx context.Context, repositoryID, fingerprint, revokedAt string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE repository_apt_signers SET revoked_at=? WHERE repository_id=? AND fingerprint=? AND revoked_at=''`, revokedAt, repositoryID, strings.ToUpper(fingerprint))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("trusted repository APT signer not found")
	}
	return nil
}
