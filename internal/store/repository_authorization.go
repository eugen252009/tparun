package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const RepositoryPublishDelegation = "repository.publish"

type RepositoryDelegation struct {
	AccountID, RepositoryID, CapabilityID, Operator, CreatedAt, UpdatedAt string
	Enabled                                                               bool
}

// TransferRepositoryOwner changes only the repository owner. It does not alter
// the global bootstrap service account, publication, or repository contents.
func (s *Store) TransferRepositoryOwner(ctx context.Context, repositoryID, newOwnerID, operator, at string) error {
	if strings.TrimSpace(repositoryID) == "" || strings.TrimSpace(newOwnerID) == "" || strings.TrimSpace(operator) == "" || strings.TrimSpace(at) == "" {
		return fmt.Errorf("repository, owner, operator, and timestamp are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldOwner, visibility string
	if err = tx.QueryRowContext(ctx, `SELECT account_id,visibility FROM repositories WHERE id=?`, repositoryID).Scan(&oldOwner, &visibility); err != nil {
		return fmt.Errorf("repository lookup: %w", err)
	}
	if oldOwner == newOwnerID {
		return fmt.Errorf("repository already has the requested owner")
	}
	var status, lifecycle string
	if err = tx.QueryRowContext(ctx, `SELECT status,lifecycle_kind FROM accounts WHERE id=?`, newOwnerID).Scan(&status, &lifecycle); err != nil {
		return fmt.Errorf("new owner lookup: %w", err)
	}
	if status != AccountStatusActive || lifecycle == string(AccountLifecycleEphemeral) {
		return fmt.Errorf("new owner is not eligible")
	}
	var globalRepository string
	if err = tx.QueryRowContext(ctx, `SELECT repository_id FROM global_bootstrap_repository WHERE singleton=1`).Scan(&globalRepository); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("bootstrap lookup: %w", err)
	}
	if globalRepository != repositoryID {
		return fmt.Errorf("repository is not the configured global bootstrap repository")
	}
	var conflict int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE account_id=? AND name=(SELECT name FROM repositories WHERE id=?)`, newOwnerID, repositoryID).Scan(&conflict); err != nil {
		return err
	}
	if conflict != 0 {
		return fmt.Errorf("new owner already has a repository with this name")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE repositories SET account_id=? WHERE id=? AND account_id=?`, newOwnerID, repositoryID, oldOwner); err != nil {
		return fmt.Errorf("transfer repository owner: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "repository-owner-transfer:"+repositoryID+":"+at, "repository-owner-transferred", "repository-owner", newOwnerID, repositoryID, oldOwner, 0, "transferred", operator, at, "old_owner="+oldOwner+";new_owner="+newOwnerID+";visibility="+visibility); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetRepositoryDelegation(ctx context.Context, accountID, repositoryID, capability, operator, at string, enabled bool) error {
	if capability != RepositoryPublishDelegation || strings.TrimSpace(accountID) == "" || strings.TrimSpace(repositoryID) == "" || strings.TrimSpace(operator) == "" || strings.TrimSpace(at) == "" {
		return fmt.Errorf("invalid repository delegation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, lifecycle string
	if err = tx.QueryRowContext(ctx, `SELECT status,lifecycle_kind FROM accounts WHERE id=?`, accountID).Scan(&status, &lifecycle); err != nil {
		return fmt.Errorf("delegated account lookup: %w", err)
	}
	if status != AccountStatusActive || lifecycle != string(AccountLifecycleInternal) {
		return fmt.Errorf("delegated publisher must be an active internal account")
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE id=?`, repositoryID).Scan(&exists); err != nil || exists != 1 {
		return fmt.Errorf("repository not found")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO repository_delegations(account_id,repository_id,capability_id,enabled,operator,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,repository_id,capability_id) DO UPDATE SET enabled=excluded.enabled,operator=excluded.operator,updated_at=excluded.updated_at`, accountID, repositoryID, capability, enabled, operator, at, at)
	if err != nil {
		return fmt.Errorf("set repository delegation: %w", err)
	}
	action, result := "repository-delegation-revoked", "revoked"
	if enabled {
		action, result = "repository-delegation-granted", "granted"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, action+":"+accountID+":"+repositoryID+":"+at, action, "repository-delegation", accountID, repositoryID, "", 0, result, operator, at, "capability="+capability); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HasRepositoryDelegation(ctx context.Context, accountID, repositoryID, capability string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM repository_delegations WHERE account_id=? AND repository_id=? AND capability_id=?`, accountID, repositoryID, capability).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return enabled, err
}
