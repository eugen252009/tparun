package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DueAccounts returns a bounded, lifecycle-specific discovery batch. Discovery
// is advisory; PurgeAccount revalidates all facts before deletion.
func (s *Store) DueAccounts(ctx context.Context, kind AccountLifecycleKind, now time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM accounts WHERE lifecycle_kind=? AND purge_after IS NOT NULL AND purge_after<=? ORDER BY purge_after,id LIMIT ?`, kind, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PurgeAccount is the one canonical operational account purge primitive. It
// removes durable operational state transactionally and returns repository IDs
// whose filesystem roots must be removed by the owning server.
func (s *Store) PurgeAccount(ctx context.Context, accountID string, now time.Time) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var kind AccountLifecycleKind
	var purge sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle_kind,purge_after FROM accounts WHERE id=?`, accountID).Scan(&kind, &purge); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if kind == AccountLifecycleInternal {
		return nil, fmt.Errorf("internal account cannot be purged")
	}
	if !purge.Valid || purge.String == "" {
		return nil, fmt.Errorf("account has no purge deadline")
	}
	deadline, err := time.Parse(time.RFC3339Nano, purge.String)
	if err != nil || now.UTC().Before(deadline) {
		return nil, fmt.Errorf("account is not purge due")
	}
	var billingRefs int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM billing_purchases WHERE account_id=?`, accountID).Scan(&billingRefs); err != nil {
		return nil, err
	}
	if billingRefs != 0 {
		return nil, fmt.Errorf("account has retained billing history")
	}
	var repos []string
	rows, err := tx.QueryContext(ctx, `SELECT id FROM repositories WHERE account_id=? ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		repos = append(repos, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range repos {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM publish_jobs WHERE repository_id=? AND (state NOT IN ('succeeded','failed') OR cleanup_state='pending')) + (SELECT COUNT(*) FROM publication_sessions WHERE repository_id=? AND state='open')`, id, id).Scan(&n); err != nil {
			return nil, err
		}
		if n != 0 {
			return nil, ErrRepositoryBusy
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM global_bootstrap_repository WHERE repository_id=?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n != 0 {
			return nil, fmt.Errorf("bootstrap repository cannot be purged")
		}
	}
	for _, id := range repos {
		for _, statement := range []string{
			"DELETE FROM publication_sessions WHERE repository_id=?",
			"DELETE FROM egress_accounting_uncertain WHERE repository_id=?",
			"DELETE FROM generation_cleanup_tasks WHERE repository_id=?",
			"DELETE FROM reader_credentials WHERE repository_id=?",
			"DELETE FROM repository_egress_daily WHERE repository_id=?",
			"DELETE FROM repository_egress_usage WHERE repository_id=?",
			"DELETE FROM repository_publications WHERE repository_id=?",
			"DELETE FROM publish_jobs WHERE repository_id=?",
			"DELETE FROM repositories WHERE id=?",
		} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return nil, err
			}
		}
	}
	for _, statement := range []string{
		"DELETE FROM sessions WHERE account_id=?",
		"DELETE FROM account_ssh_credentials WHERE account_id=?",
		"DELETE FROM account_ssh_credential_audit WHERE account_id=?",
		"DELETE FROM product_grants WHERE customer_id=?",
		"DELETE FROM purchase_grant_instances WHERE customer_id=?",
		"DELETE FROM account_storage_high_water WHERE account_id=?",
		"DELETE FROM billing_provider_submissions WHERE account_id=?",
		"DELETE FROM billing_provider_accounts WHERE account_id=?",
		"DELETE FROM mock_billing_events WHERE account_id=?",
		"DELETE FROM account_metadata WHERE account_id=?",
		"DELETE FROM accounts WHERE id=?",
	} {
		if _, err := tx.ExecContext(ctx, statement, accountID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return repos, nil
}
