package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type BootstrapRepository struct {
	RepositoryID string
	AccountID    string
}

func (s *Store) GlobalBootstrapRepository(ctx context.Context) (BootstrapRepository, bool, error) {
	var b BootstrapRepository
	err := s.db.QueryRowContext(ctx, `SELECT repository_id,account_id FROM global_bootstrap_repository WHERE singleton=1`).Scan(&b.RepositoryID, &b.AccountID)
	if err == sql.ErrNoRows {
		return BootstrapRepository{}, false, nil
	}
	return b, err == nil, err
}

// EnableGlobalBootstrap atomically claims the singleton role. It deliberately
// requires an internal account and never changes repository ownership or
// visibility.
func (s *Store) EnableGlobalBootstrap(ctx context.Context, repositoryID, operator, now string) error {
	if strings.TrimSpace(repositoryID) == "" || strings.TrimSpace(operator) == "" {
		return fmt.Errorf("bootstrap repository and operator are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID, class, status string
	if err = tx.QueryRowContext(ctx, `SELECT r.account_id,COALESCE(m.account_class,?),a.status FROM repositories r JOIN accounts a ON a.id=r.account_id LEFT JOIN account_metadata m ON m.account_id=r.account_id WHERE r.id=?`, AccountClassUnclassified, repositoryID).Scan(&accountID, &class, &status); err != nil {
		return fmt.Errorf("find bootstrap repository: %w", err)
	}
	if accountID == "" || class != AccountClassInternal || status != AccountStatusActive {
		return fmt.Errorf("bootstrap repository requires an internal service account")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO global_bootstrap_repository(singleton,repository_id,account_id,created_at) VALUES(1,?,?,?)`, repositoryID, accountID, now); err != nil {
		return fmt.Errorf("enable global bootstrap repository: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "bootstrap-enable:"+repositoryID+":"+now, "bootstrap-role-enabled", "bootstrap-role", accountID, repositoryID, "ordinary", 0, "enabled", operator, now, "global bootstrap repository"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DisableGlobalBootstrap(ctx context.Context, operator, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b BootstrapRepository
	if err = tx.QueryRowContext(ctx, `SELECT repository_id,account_id FROM global_bootstrap_repository WHERE singleton=1`).Scan(&b.RepositoryID, &b.AccountID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM global_bootstrap_repository WHERE singleton=1`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "bootstrap-disable:"+b.RepositoryID+":"+now, "bootstrap-role-removed", "bootstrap-role", b.AccountID, b.RepositoryID, "bootstrap", 0, "ordinary", operator, now, "global bootstrap repository"); err != nil {
		return err
	}
	return tx.Commit()
}

type BootstrapCapacityAudit struct {
	AccountID, RepositoryID, Operator, Action, Details string
	OldStorage, NewStorage, OldEgress, NewEgress       int64
	At                                                 time.Time
}

func (s *Store) SetBootstrapCapacity(ctx context.Context, accountID, repositoryID, operator string, storage, egress int64, details string, at time.Time) error {
	if storage <= 0 || egress <= 0 || accountID == "" || repositoryID == "" || operator == "" {
		return fmt.Errorf("invalid bootstrap capacity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var registeredAccount string
	if err = tx.QueryRowContext(ctx, `SELECT account_id FROM global_bootstrap_repository WHERE singleton=1 AND repository_id=?`, repositoryID).Scan(&registeredAccount); err != nil {
		return fmt.Errorf("bootstrap repository is not designated: %w", err)
	}
	if registeredAccount != accountID {
		return fmt.Errorf("bootstrap repository owner mismatch")
	}
	var oldStorage, oldEgress int64
	_ = tx.QueryRowContext(ctx, `SELECT storage_limit_bytes FROM product_grants WHERE customer_id=? AND product_id=? AND capability_id=?`, accountID, tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish).Scan(&oldStorage)
	_ = tx.QueryRowContext(ctx, `SELECT egress_limit_bytes FROM product_grants WHERE customer_id=? AND product_id=? AND capability_id=?`, accountID, tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryDownload).Scan(&oldEgress)
	for _, g := range []commerce.Grant{
		{CustomerID: commerce.CustomerID(accountID), ProductID: tpahubproduct.ProductID, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "administrative-service", Enabled: true, ValidFrom: at, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: storage}, Source: "administrative", CreatedAt: at, UpdatedAt: at},
		{CustomerID: commerce.CustomerID(accountID), ProductID: tpahubproduct.ProductID, CapabilityID: tpahubproduct.CapabilityRepositoryDownload, OfferID: "administrative-service", Enabled: true, ValidFrom: at, Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: egress}, Source: "administrative", CreatedAt: at, UpdatedAt: at},
	} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(customer_id,product_id,capability_id) DO UPDATE SET offer_id=excluded.offer_id,enabled=excluded.enabled,valid_from=excluded.valid_from,valid_until=excluded.valid_until,period_cadence=excluded.period_cadence,storage_limit_bytes=excluded.storage_limit_bytes,egress_limit_bytes=excluded.egress_limit_bytes,operation_metric=excluded.operation_metric,operation_limit=excluded.operation_limit,source=excluded.source,purchase_id=excluded.purchase_id,updated_at=excluded.updated_at`, g.CustomerID, g.ProductID, g.CapabilityID, g.OfferID, g.Enabled, g.ValidFrom.UTC().Format(time.RFC3339Nano), "", g.Period, g.Limits.StorageBytes, g.Limits.EgressBytes, g.Limits.OperationMetric, g.Limits.OperationCount, g.Source, g.PurchaseID, g.CreatedAt.UTC().Format(time.RFC3339Nano), g.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("bootstrap-capacity:%s:%d", repositoryID, at.UnixNano()), "bootstrap-capacity-changed", "bootstrap-capacity", accountID, repositoryID, fmt.Sprintf("storage=%d;egress=%d", oldStorage, oldEgress), 0, fmt.Sprintf("storage=%d;egress=%d", storage, egress), operator, at.UTC().Format(time.RFC3339Nano), details); err != nil {
		return err
	}
	return tx.Commit()
}
