package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type Repository struct{ ID, AccountID, Name, Visibility, CreatedAt string }
type Job struct {
	ID, RepositoryID, State, IncomingPath, Error, CreatedAt, StartedAt, FinishedAt, CleanupState, CleanupError, CleanupNextRetry string
	CleanupAttempts                                                                                                              int64
}
type Account struct{ ID, Email, PasswordHash, Status, CreatedAt, AccountClass string }

const AccountStatusActive = "active"

const (
	AccountClassProduction   = "production"
	AccountClassTest         = "test"
	AccountClassInternal     = "internal"
	AccountClassUnclassified = "unclassified"
)

type Session struct{ ID, AccountID, TokenPrefix string }
type ReaderCredential struct{ ID, RepositoryID, Name, TokenPrefix, CreatedAt string }
type Store struct{ db *sql.DB }

var ErrNotFound = sql.ErrNoRows
var ErrRepositoryBusy = errors.New("repository has an active publication lifecycle")
var migrationFault func(string) error
var providerEventFault func(string) error

func providerEventCheckpoint(stage string) error {
	if providerEventFault != nil {
		return providerEventFault(stage)
	}
	return nil
}

func migrationCheckpoint(stage string) error {
	if migrationFault != nil {
		return migrationFault(stage)
	}
	return nil
}

const currentSchemaVersion = 6

// CurrentSchemaVersion is the schema authority exposed to deployment tooling.
func CurrentSchemaVersion() int { return currentSchemaVersion }

// Open initializes Schema 6 directly or opens the current supported schema.
// Pre-release, future and invalid databases are rejected without mutation.
func Open(dir string) (*Store, error) { return open(dir, true, false) }

// Migrate performs the explicit Schema 5 → 6 upgrade or validates Schema 6.
// Schemas 1–4 are unsupported.
func Migrate(dir string) error {
	path := filepath.Join(dir, "tpahub.db")
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("migration target does not exist: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("migration target is not a non-empty regular file: %s", path)
	}
	s, err := open(dir, false, true)
	if err != nil {
		return err
	}
	return s.Close()
}

// OpenForAdmin opens the durable store without converting live delivery
// markers into restart uncertainty. This lets an administrator inspect an
// active process without changing its ownership state.
func OpenForAdmin(dir string) (*Store, error) {
	path := filepath.Join(dir, "tpahub.db")
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("administrative database not found at %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("administrative database is not a non-empty regular file: %s", path)
	}
	return open(dir, false, false)
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) AddRepositoryEgress(c context.Context, repositoryID, period string, bytes int64, updatedAt string) error {
	if bytes <= 0 {
		return nil
	}
	_, e := s.db.ExecContext(c, `INSERT INTO repository_egress_usage(repository_id,period,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,period) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, period, bytes, updatedAt)
	return e
}

type EgressDelivery struct {
	ID             string `json:"delivery_id"`
	AccountID      string `json:"account_id"`
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	State          string `json:"state"`
	PeriodStart    string `json:"period_start"`
	PeriodEnd      string `json:"period_end"`
	CreatedAt      string `json:"created_at"`
	Reason         string `json:"reason"`
	Version        int64  `json:"version"`
	ReservedBytes  int64  `json:"reserved_bytes"`
}

func (s *Store) BeginEgressDelivery(c context.Context, id, repositoryID, createdAt string) error {
	_, err := s.db.ExecContext(c, `INSERT INTO egress_accounting_uncertain(id,repository_id,created_at,state,version,period_start,period_end,reserved_bytes) VALUES(?,?,?,'streaming',1,'','',0)`, id, repositoryID, createdAt)
	return err
}

// UnlimitedEgressLimit is used by callers when no applicable entitlement
// exists. Zero remains a finite allowance and therefore reserves zero bytes.
const UnlimitedEgressLimit int64 = -1

func (s *Store) ReserveEgressDelivery(c context.Context, id, repositoryID, periodStart, periodEnd string, requested, limit int64, createdAt string) (bool, error) {
	periodStart, start, startErr := normalizeEgressInstant(periodStart)
	periodEnd, end, endErr := normalizeEgressInstant(periodEnd)
	if requested < 0 || limit < UnlimitedEgressLimit || startErr != nil || endErr != nil || !end.After(start) {
		return false, fmt.Errorf("invalid egress delivery reservation")
	}
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var accountID string
	if err = tx.QueryRowContext(c, `SELECT COALESCE(account_id,'') FROM repositories WHERE id=?`, repositoryID).Scan(&accountID); err != nil {
		return false, err
	}
	var used, reserved int64
	// Even an unlimited or denied grant needs the marker's period persisted so
	// an uncertain settlement cannot later be assigned to an arbitrary date.
	if limit >= 0 {
		if err = tx.QueryRowContext(c, `SELECT COALESCE(SUM(d.egress_bytes),0) FROM repository_egress_daily d JOIN repositories r ON r.id=d.repository_id WHERE r.account_id=? AND ((length(d.usage_date)=10 AND d.usage_date>=substr(?,1,10) AND (d.usage_date<substr(?,1,10) OR (d.usage_date=substr(?,1,10) AND substr(?,12)<>'00:00:00.000000000Z'))) OR (length(d.usage_date)<>10 AND d.usage_date>=? AND d.usage_date<?))`, accountID, periodStart, periodEnd, periodEnd, periodEnd, periodStart, periodEnd).Scan(&used); err != nil {
			return false, err
		}
		if err = tx.QueryRowContext(c, `SELECT COALESCE(SUM(u.reserved_bytes),0) FROM egress_accounting_uncertain u JOIN repositories r ON r.id=u.repository_id WHERE r.account_id=? AND u.period_start=? AND u.period_end=? AND u.state IN ('streaming','uncertain')`, accountID, periodStart, periodEnd).Scan(&reserved); err != nil {
			return false, err
		}
	}
	if limit >= 0 && (used < 0 || reserved < 0 || used > limit || reserved > limit-used) {
		reserved = limit - used
	}
	remaining := int64(0)
	if limit >= 0 {
		remaining = limit - used - reserved
	}
	if remaining < 0 {
		remaining = 0
	}
	reservation := requested
	if reservation > remaining {
		reservation = remaining
	}
	result, err := tx.ExecContext(c, `UPDATE egress_accounting_uncertain SET period_start=?,period_end=?,reserved_bytes=? WHERE id=? AND repository_id=? AND state='streaming' AND period_start='' AND period_end=''`, periodStart, periodEnd, reservation, id, repositoryID)
	if err != nil {
		return false, err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return false, countErr
	} else if count == 0 {
		if _, err = tx.ExecContext(c, `INSERT INTO egress_accounting_uncertain(id,repository_id,created_at,state,version,period_start,period_end,reserved_bytes) VALUES(?,?,?,'streaming',1,?,?,?)`, id, repositoryID, createdAt, periodStart, periodEnd, reservation); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return limit >= 0 && reservation < requested, nil
}

func (s *Store) CompleteEgressDelivery(c context.Context, id, repositoryID, usageDate string, bytes int64, updatedAt string) error {
	usageDate, usageAt, usageErr := normalizeEgressInstant(usageDate)
	if bytes < 0 || usageErr != nil {
		return fmt.Errorf("invalid egress delivery settlement")
	}
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, periodStart, periodEnd, createdAt string
	if err = tx.QueryRowContext(c, `SELECT state,period_start,period_end,created_at FROM egress_accounting_uncertain WHERE id=? AND repository_id=?`, id, repositoryID).Scan(&state, &periodStart, &periodEnd, &createdAt); err != nil {
		return err
	}
	if state != "streaming" && state != "uncertain" {
		return fmt.Errorf("egress delivery is not completable")
	}
	_, start, startErr := normalizeEgressInstant(periodStart)
	_, end, endErr := normalizeEgressInstant(periodEnd)
	periodValid := startErr == nil && endErr == nil && !usageAt.Before(start) && usageAt.Before(end)
	if periodStart == "" && periodEnd == "" {
		created, createdErr := time.Parse(time.RFC3339Nano, createdAt)
		periodValid = createdErr == nil && usageAt.Format("2006-01-02") == created.UTC().Format("2006-01-02")
	}
	if !periodValid {
		return fmt.Errorf("egress settlement instant is outside marker period")
	}
	if bytes > 0 {
		if _, err = tx.ExecContext(c, `INSERT INTO repository_egress_daily(repository_id,usage_date,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,usage_date) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, usageDate, bytes, updatedAt); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(c, `DELETE FROM egress_accounting_uncertain WHERE id=? AND repository_id=?`, id, repositoryID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("egress delivery uncertainty missing")
	}
	return tx.Commit()
}

func (s *Store) MarkEgressDeliveryUncertain(c context.Context, id, repositoryID, reason string) error {
	_, err := s.db.ExecContext(c, `UPDATE egress_accounting_uncertain SET state='uncertain',version=version+1,reason=? WHERE id=? AND repository_id=? AND state='streaming'`, reason, id, repositoryID)
	return err
}

func (s *Store) EgressDeliveries(c context.Context, accountID string) ([]EgressDelivery, error) {
	query := `SELECT u.id,COALESCE(r.account_id,''),u.repository_id,r.name,u.state,u.version,u.period_start,u.period_end,u.reserved_bytes,u.created_at,u.reason FROM egress_accounting_uncertain u JOIN repositories r ON r.id=u.repository_id`
	args := []any{}
	if accountID != "" {
		query += " WHERE r.account_id=?"
		args = append(args, accountID)
	}
	query += " ORDER BY u.created_at,u.id"
	rows, err := s.db.QueryContext(c, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []EgressDelivery
	for rows.Next() {
		var d EgressDelivery
		if err = rows.Scan(&d.ID, &d.AccountID, &d.RepositoryID, &d.RepositoryName, &d.State, &d.Version, &d.PeriodStart, &d.PeriodEnd, &d.ReservedBytes, &d.CreatedAt, &d.Reason); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func (s *Store) RecoverEgressDelivery(c context.Context, accountID, id, repositoryID string, version int64, usageDate string, bytes int64, updatedAt string) error {
	return s.RecoverEgressDeliveryWithAudit(c, accountID, id, repositoryID, version, usageDate, bytes, updatedAt, "internal")
}

func (s *Store) RecoverEgressDeliveryWithAudit(c context.Context, accountID, id, repositoryID string, version int64, usageDate string, bytes int64, updatedAt, operator string) error {
	normalizedUsage, usageAt, usageErr := normalizeEgressInstant(usageDate)
	if version <= 0 || bytes < 0 || usageErr != nil {
		return fmt.Errorf("invalid egress recovery")
	}
	usageDate = normalizedUsage
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, periodStart, periodEnd, createdAt string
	if err = tx.QueryRowContext(c, `SELECT u.state,u.period_start,u.period_end,u.created_at FROM egress_accounting_uncertain u JOIN repositories r ON r.id=u.repository_id WHERE u.id=? AND u.repository_id=? AND (r.account_id=? OR (r.account_id IS NULL AND ?='')) AND u.version=?`, id, repositoryID, accountID, accountID, version).Scan(&state, &periodStart, &periodEnd, &createdAt); err != nil {
		_ = insertAdminAudit(tx, id, accountID, repositoryID, "missing", version, "rejected", operator, updatedAt, err.Error())
		if commitErr := tx.Commit(); commitErr != nil {
			return commitErr
		}
		return err
	}
	if state != "uncertain" {
		if auditErr := insertAdminAudit(tx, id, accountID, repositoryID, state, version, "rejected", operator, updatedAt, "egress delivery is still active"); auditErr != nil {
			return auditErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return commitErr
		}
		return fmt.Errorf("egress delivery is still active")
	}
	_, start, startErr := normalizeEgressInstant(periodStart)
	_, end, endErr := normalizeEgressInstant(periodEnd)
	periodValid := startErr == nil && endErr == nil && !usageAt.Before(start) && usageAt.Before(end)
	if periodStart == "" && periodEnd == "" {
		created, createdErr := time.Parse(time.RFC3339Nano, createdAt)
		periodValid = createdErr == nil && usageAt.Format("2006-01-02") == created.UTC().Format("2006-01-02")
	}
	if !periodValid {
		details := fmt.Sprintf("usage date outside marker period: usage_date=%s period_start=%s period_end=%s", usageDate, periodStart, periodEnd)
		if auditErr := insertAdminAudit(tx, id, accountID, repositoryID, "uncertain", version, "rejected", operator, updatedAt, details); auditErr != nil {
			return auditErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return commitErr
		}
		return fmt.Errorf("usage date must be within marker period")
	}
	if bytes > 0 {
		if _, err = tx.ExecContext(c, `INSERT INTO repository_egress_daily(repository_id,usage_date,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,usage_date) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, usageDate, bytes, updatedAt); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(c, `DELETE FROM egress_accounting_uncertain WHERE id=? AND repository_id=? AND version=? AND state='uncertain'`, id, repositoryID, version)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		if countErr != nil {
			return countErr
		}
		return fmt.Errorf("egress delivery changed during recovery")
	}
	if err = insertAdminAudit(tx, id, accountID, repositoryID, "uncertain", version, "recovered", operator, updatedAt, fmt.Sprintf("accounted_bytes=%d usage_date=%s", bytes, usageDate)); err != nil {
		return err
	}
	return tx.Commit()
}

func insertAdminAudit(tx *sql.Tx, markerID, accountID, repositoryID, previousState string, previousVersion int64, result, operator, createdAt, details string) error {
	if operator == "" {
		operator = "unknown"
	}
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(`INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("egress-recovery:%s:%d:%s", markerID, previousVersion, createdAt), "egress-recovery", markerID, accountID, repositoryID, previousState, previousVersion, result, operator, createdAt, details)
	return err
}
func (s *Store) AccountEgressUncertain(c context.Context, accountID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(c, `SELECT COUNT(*) FROM egress_accounting_uncertain u JOIN repositories r ON r.id=u.repository_id WHERE r.account_id=?`, accountID).Scan(&n)
	return n > 0, err
}
func (s *Store) RepositoryEgressUncertain(c context.Context, repositoryID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(c, `SELECT COUNT(*) FROM egress_accounting_uncertain WHERE repository_id=?`, repositoryID).Scan(&n)
	return n > 0, err
}
func (s *Store) AddRepositoryEgressDaily(c context.Context, repositoryID, usageDate string, bytes int64, updatedAt string) error {
	if bytes <= 0 {
		return nil
	}
	usageDate, _, err := normalizeEgressInstant(usageDate)
	if err != nil {
		return err
	}
	_, e := s.db.ExecContext(c, `INSERT INTO repository_egress_daily(repository_id,usage_date,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,usage_date) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, usageDate, bytes, updatedAt)
	return e
}
func (s *Store) AccountEgressWindow(c context.Context, accountID string, start, end time.Time) (int64, error) {
	var total int64
	startText, endText := formatEgressInstant(start), formatEgressInstant(end)
	e := s.db.QueryRowContext(c, `SELECT COALESCE(SUM(u.egress_bytes),0) FROM repository_egress_daily u JOIN repositories r ON r.id=u.repository_id WHERE r.account_id=? AND ((length(u.usage_date)=10 AND u.usage_date>=substr(?,1,10) AND (u.usage_date<substr(?,1,10) OR (u.usage_date=substr(?,1,10) AND substr(?,12)<>'00:00:00.000000000Z'))) OR (length(u.usage_date)<>10 AND u.usage_date>=? AND u.usage_date<?))`, accountID, startText, endText, endText, endText, startText, endText).Scan(&total)
	return total, e
}

func normalizeEgressInstant(value string) (string, time.Time, error) {
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		instant, err = time.Parse("2006-01-02", value)
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("invalid egress instant")
	}
	instant = instant.UTC()
	return formatEgressInstant(instant), instant, nil
}

func formatEgressInstant(instant time.Time) string {
	return instant.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
func (s *Store) AccountEgress(c context.Context, accountID, period string) (int64, error) {
	var total int64
	e := s.db.QueryRowContext(c, `SELECT COALESCE(SUM(u.egress_bytes),0) FROM repository_egress_usage u JOIN repositories r ON r.id=u.repository_id WHERE r.account_id=? AND u.period=?`, accountID, period).Scan(&total)
	return total, e
}
func (s *Store) ObserveStorageHighWater(c context.Context, accountID, period string, bytes int64, updatedAt string) error {
	if bytes < 0 {
		return fmt.Errorf("negative storage usage")
	}
	_, e := s.db.ExecContext(c, `INSERT INTO account_storage_high_water(account_id,period,high_water_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(account_id,period) DO UPDATE SET high_water_bytes=MAX(high_water_bytes,excluded.high_water_bytes),updated_at=excluded.updated_at`, accountID, period, bytes, updatedAt)
	return e
}
func (s *Store) StorageHighWater(c context.Context, accountID, period string) (int64, error) {
	var bytes int64
	e := s.db.QueryRowContext(c, `SELECT high_water_bytes FROM account_storage_high_water WHERE account_id=? AND period=?`, accountID, period).Scan(&bytes)
	if e == sql.ErrNoRows {
		return 0, nil
	}
	return bytes, e
}

// LegacyTPAMockBillingEventPayload is a compatibility wrapper for the old
// TPA Hub-only mock billing API. New callers must select a product explicitly.
func (s *Store) LegacyTPAMockBillingEventPayload(c context.Context, accountID, period, eventType string) (string, error) {
	return s.ProductMockBillingEventPayload(c, accountID, string(tpahubproduct.ProductID), period, eventType)
}
func (s *Store) ProductMockBillingEventPayload(c context.Context, accountID, productID, period, eventType string) (string, error) {
	if strings.TrimSpace(productID) == "" {
		return "", fmt.Errorf("mock billing event product ID is empty")
	}
	var payload string
	e := s.db.QueryRowContext(c, `SELECT payload FROM mock_billing_events WHERE account_id=? AND product_id=? AND period=? AND event_type=?`, accountID, productID, period, eventType).Scan(&payload)
	return payload, e
}

type BillingProviderAccount struct{ AccountID, Provider, Environment, ProviderCustomerID, CreatedAt string }
type BillingProviderSubmission struct{ AccountID, ProductID, Period, Provider, Environment, FinalizationID, Status, ProviderReference, AttemptedAt, CompletedAt, LastError string }

func (s *Store) ProviderAccount(c context.Context, accountID, provider, environment string) (BillingProviderAccount, error) {
	var a BillingProviderAccount
	e := s.db.QueryRowContext(c, `SELECT account_id,provider,environment,provider_customer_id,created_at FROM billing_provider_accounts WHERE account_id=? AND provider=? AND environment=?`, accountID, provider, environment).Scan(&a.AccountID, &a.Provider, &a.Environment, &a.ProviderCustomerID, &a.CreatedAt)
	return a, e
}
func (s *Store) SaveProviderAccount(c context.Context, a BillingProviderAccount) error {
	_, e := s.db.ExecContext(c, `INSERT INTO billing_provider_accounts(account_id,provider,environment,provider_customer_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT(account_id,provider,environment) DO UPDATE SET provider_customer_id=excluded.provider_customer_id`, a.AccountID, a.Provider, a.Environment, a.ProviderCustomerID, a.CreatedAt)
	return e
}
func (s *Store) GetPaymentCustomerMapping(c context.Context, customer commerce.CustomerID, provider string, environment payments.Environment) (payments.CustomerMapping, error) {
	a, err := s.ProviderAccount(c, string(customer), provider, string(environment))
	if err == sql.ErrNoRows {
		return payments.CustomerMapping{}, payments.ErrCustomerMappingNotFound
	}
	if err != nil {
		return payments.CustomerMapping{}, err
	}
	created, err := parseEntitlementTime(a.CreatedAt)
	if err != nil {
		return payments.CustomerMapping{}, err
	}
	return payments.CustomerMapping{CustomerID: customer, Provider: a.Provider, Environment: a.Environment, ProviderCustomerReference: a.ProviderCustomerID, CreatedAt: created}, nil
}
func (s *Store) SavePaymentCustomerMapping(c context.Context, mapping payments.CustomerMapping) error {
	return s.SaveProviderAccount(c, BillingProviderAccount{AccountID: string(mapping.CustomerID), Provider: mapping.Provider, Environment: mapping.Environment, ProviderCustomerID: mapping.ProviderCustomerReference, CreatedAt: mapping.CreatedAt.UTC().Format(time.RFC3339Nano)})
}
func (s *Store) ProviderSubmission(c context.Context, accountID, productID, period, provider, environment string) (BillingProviderSubmission, error) {
	var x BillingProviderSubmission
	e := s.db.QueryRowContext(c, `SELECT account_id,product_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error FROM billing_provider_submissions WHERE account_id=? AND product_id=? AND period=? AND provider=? AND environment=?`, accountID, productID, period, provider, environment).Scan(&x.AccountID, &x.ProductID, &x.Period, &x.Provider, &x.Environment, &x.FinalizationID, &x.Status, &x.ProviderReference, &x.AttemptedAt, &x.CompletedAt, &x.LastError)
	return x, e
}
func (s *Store) SaveProviderSubmission(c context.Context, x BillingProviderSubmission) error {
	if strings.TrimSpace(x.ProductID) == "" {
		return fmt.Errorf("provider submission product ID is empty")
	}
	_, e := s.db.ExecContext(c, `INSERT INTO billing_provider_submissions(account_id,product_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,product_id,period,provider,environment) DO UPDATE SET finalization_id=excluded.finalization_id,status=excluded.status,provider_reference=excluded.provider_reference,attempted_at=excluded.attempted_at,completed_at=excluded.completed_at,last_error=excluded.last_error`, x.AccountID, x.ProductID, x.Period, x.Provider, x.Environment, x.FinalizationID, x.Status, x.ProviderReference, x.AttemptedAt, x.CompletedAt, x.LastError)
	return e
}

func parseEntitlementTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
	}
	return parsed, err
}

func knownGrantSource(source string) bool {
	switch source {
	case "local", "administrative":
		return true
	default:
		return false
	}
}

func scanGrant(row interface{ Scan(...any) error }) (commerce.Grant, error) {
	var grant commerce.Grant
	var customerID, productID, capabilityID, validFrom, validUntil, createdAt, updatedAt string
	var enabled int
	err := row.Scan(&customerID, &productID, &capabilityID, &grant.OfferID, &enabled, &validFrom, &validUntil, &grant.Period, &grant.Limits.StorageBytes, &grant.Limits.EgressBytes, &grant.Limits.OperationMetric, &grant.Limits.OperationCount, &grant.Source, &grant.PurchaseID, &createdAt, &updatedAt)
	if err != nil {
		return commerce.Grant{}, err
	}
	grant.CustomerID, grant.ProductID, grant.CapabilityID, grant.Enabled = commerce.CustomerID(customerID), commerce.ProductID(productID), commerce.CapabilityID(capabilityID), enabled == 1
	grant.ValidFrom, err = parseEntitlementTime(validFrom)
	if err != nil {
		return commerce.Grant{}, fmt.Errorf("invalid grant valid_from: %w", err)
	}
	if validUntil != "" {
		until, parseErr := parseEntitlementTime(validUntil)
		if parseErr != nil {
			return commerce.Grant{}, fmt.Errorf("invalid grant valid_until: %w", parseErr)
		}
		grant.ValidUntil = &until
	}
	grant.CreatedAt, err = parseEntitlementTime(createdAt)
	if err != nil {
		return commerce.Grant{}, fmt.Errorf("invalid grant created_at: %w", err)
	}
	grant.UpdatedAt, err = parseEntitlementTime(updatedAt)
	if err != nil {
		return commerce.Grant{}, fmt.Errorf("invalid grant updated_at: %w", err)
	}
	return grant, nil
}

// GetGrant is the canonical current entitlement resolution path (I-003):
// authoritative product grants and paid grant instances are validated and
// aggregated here using active, enabled contributors and component-wise maxima.
// Local policy is independent authority, never suppressed by a paid grant.
func (s *Store) GetGrant(c context.Context, customer commerce.CustomerID, product commerce.ProductID, capability commerce.CapabilityID, at time.Time) (commerce.Grant, error) {
	const columns = `customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at`
	var contributors []commerce.Grant
	seen, enabledSeen := false, false
	baseRows, err := s.db.QueryContext(c, `SELECT `+columns+` FROM product_grants WHERE customer_id=? AND product_id=? AND capability_id=? ORDER BY source`, customer, product, capability)
	if err != nil {
		return commerce.Grant{}, err
	}
	for baseRows.Next() {
		base, scanErr := scanGrant(baseRows)
		if scanErr != nil {
			baseRows.Close()
			return commerce.Grant{}, scanErr
		}
		if !knownGrantSource(base.Source) {
			baseRows.Close()
			return commerce.Grant{}, fmt.Errorf("unknown grant source %q", base.Source)
		}
		seen = true
		if base.Enabled {
			enabledSeen = true
			if base.ActiveAt(at) {
				contributors = append(contributors, base)
			}
		}
	}
	err = baseRows.Err()
	baseRows.Close()
	if err != nil {
		return commerce.Grant{}, err
	}
	rows, err := s.db.QueryContext(c, `SELECT i.customer_id,i.product_id,i.capability_id,i.offer_id,i.enabled,i.valid_from,i.valid_until,i.period_cadence,i.storage_limit_bytes,i.egress_limit_bytes,i.operation_metric,i.operation_limit,'purchase',i.purchase_id,i.created_at,i.updated_at FROM purchase_grant_instances i JOIN billing_purchases p ON p.id=i.purchase_id AND p.status='paid' WHERE i.customer_id=? AND i.product_id=? AND i.capability_id=? ORDER BY i.valid_from DESC,i.purchase_id DESC`, customer, product, capability)
	if err != nil {
		return commerce.Grant{}, err
	}
	defer rows.Close()
	for rows.Next() {
		candidate, scanErr := scanGrant(rows)
		if scanErr != nil {
			return commerce.Grant{}, scanErr
		}
		seen = true
		if !candidate.Enabled {
			continue
		}
		enabledSeen = true
		if candidate.ActiveAt(at) {
			contributors = append(contributors, candidate)
		}
	}
	if err = rows.Err(); err != nil {
		return commerce.Grant{}, err
	}
	if len(contributors) == 0 {
		if !seen {
			return commerce.Grant{}, commerce.ErrGrantNotFound
		}
		if !enabledSeen {
			return commerce.Grant{}, commerce.ErrGrantDisabled
		}
		return commerce.Grant{}, commerce.ErrGrantInactive
	}
	sort.Slice(contributors, func(i, j int) bool {
		left := fmt.Sprintf("%s|%s|%s|%s|%s", contributors[i].Source, contributors[i].PurchaseID, contributors[i].OfferID, contributors[i].ValidFrom.UTC().Format(time.RFC3339Nano), contributors[i].Period)
		right := fmt.Sprintf("%s|%s|%s|%s|%s", contributors[j].Source, contributors[j].PurchaseID, contributors[j].OfferID, contributors[j].ValidFrom.UTC().Format(time.RFC3339Nano), contributors[j].Period)
		return left < right
	})
	metric := commerce.MetricID("")
	for _, grant := range contributors {
		if grant.Limits.OperationCount > 0 {
			if grant.Limits.OperationMetric == "" {
				return commerce.Grant{}, commerce.ErrIncompatibleGrantMetrics
			}
			if metric != "" && metric != grant.Limits.OperationMetric {
				return commerce.Grant{}, commerce.ErrIncompatibleGrantMetrics
			}
			metric = grant.Limits.OperationMetric
		}
	}
	effective := commerce.Grant{CustomerID: customer, ProductID: product, CapabilityID: capability, Enabled: true, Source: "effective"}
	storageContributor, egressContributor, operationContributor := 0, 0, 0
	for i, grant := range contributors {
		if grant.Limits.StorageBytes > effective.Limits.StorageBytes {
			effective.Limits.StorageBytes = grant.Limits.StorageBytes
			storageContributor = i
		}
		if grant.Limits.EgressBytes > effective.Limits.EgressBytes {
			effective.Limits.EgressBytes = grant.Limits.EgressBytes
			egressContributor = i
		}
		if grant.Limits.OperationCount > effective.Limits.OperationCount {
			effective.Limits.OperationCount = grant.Limits.OperationCount
			effective.Limits.OperationMetric = grant.Limits.OperationMetric
			operationContributor = i
		}
	}
	if len(contributors) > 1 {
		periodContributor := storageContributor
		if effective.Limits.OperationCount > 0 {
			periodContributor = operationContributor
		}
		if effective.Limits.EgressBytes > 0 {
			periodContributor = egressContributor
		}
		effective.Effective = &commerce.EffectiveGrant{Contributors: contributors, PeriodContributor: periodContributor}
		return effective, nil
	}
	return contributors[0], nil
}
func (s *Store) PutGrant(c context.Context, grant commerce.Grant) error {
	if strings.TrimSpace(string(grant.ProductID)) == "" {
		return fmt.Errorf("grant product ID is empty")
	}
	validUntil := ""
	if grant.ValidUntil != nil {
		validUntil = grant.ValidUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(c, `INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(customer_id,product_id,capability_id,source) DO UPDATE SET offer_id=excluded.offer_id,enabled=excluded.enabled,valid_from=excluded.valid_from,valid_until=excluded.valid_until,period_cadence=excluded.period_cadence,storage_limit_bytes=excluded.storage_limit_bytes,egress_limit_bytes=excluded.egress_limit_bytes,operation_metric=excluded.operation_metric,operation_limit=excluded.operation_limit,source=excluded.source,purchase_id=excluded.purchase_id,updated_at=excluded.updated_at`, grant.CustomerID, grant.ProductID, grant.CapabilityID, grant.OfferID, grant.Enabled, grant.ValidFrom.UTC().Format(time.RFC3339Nano), validUntil, grant.Period, grant.Limits.StorageBytes, grant.Limits.EgressBytes, grant.Limits.OperationMetric, grant.Limits.OperationCount, grant.Source, grant.PurchaseID, grant.CreatedAt.UTC().Format(time.RFC3339Nano), grant.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// BillingPurchase is retained as a persistence-facing alias. The canonical
// commercial fact is commerce.Purchase.
type BillingPurchase = commerce.Purchase
type billingProviderEvent struct{ Provider, Environment, ProviderEventID, EventType, ReceivedAt, ProcessedAt, Status, Error string }

func scanPurchase(row interface{ Scan(...any) error }) (BillingPurchase, error) {
	var p BillingPurchase
	e := row.Scan(&p.ID, &p.AccountID, &p.ProductID, &p.PlanCode, &p.OfferVersion, &p.RateCardID, &p.RateCardMode, &p.TaxClassification, &p.Currency, &p.AmountMinor, &p.BillingEnvironment, &p.Provider, &p.Status, &p.EntitlementPeriodStart, &p.EntitlementPeriodEnd, &p.ProviderReference, &p.CheckoutURL, &p.CheckoutRequestVersion, &p.CreatedAt, &p.UpdatedAt, &p.PaidAt, &p.FailedAt, &p.ExpiredAt, &p.ProviderState, &p.ReconciliationReason)
	return p, e
}

const purchaseColumns = `id,account_id,product_id,plan_code,offer_version,rate_card_id,rate_card_mode,tax_classification,currency,amount_minor,billing_environment,provider,status,entitlement_period_start,entitlement_period_end,provider_reference,checkout_url,checkout_request_version,created_at,updated_at,paid_at,failed_at,expired_at,provider_state,reconciliation_reason`

func (s *Store) PurchaseGrantInstanceCount(c context.Context, customerID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(c, `SELECT COUNT(*) FROM purchase_grant_instances WHERE customer_id=?`, customerID).Scan(&count)
	return count, err
}

func (s *Store) Purchase(c context.Context, id string) (BillingPurchase, error) {
	return scanPurchase(s.db.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE id=?`, id))
}
func (s *Store) PurchasePaymentState(c context.Context, id string) (commerce.PurchasePaymentState, error) {
	purchase, err := s.Purchase(c, id)
	if err != nil {
		return commerce.PurchasePaymentState{}, err
	}
	return commerce.PurchasePaymentState{PurchaseID: purchase.ID, CustomerID: commerce.CustomerID(purchase.AccountID), ProductID: commerce.ProductID(purchase.ProductID), Provider: purchase.Provider, Environment: purchase.BillingEnvironment, ProviderReference: purchase.ProviderReference, AmountMinor: purchase.AmountMinor, Currency: purchase.Currency}, nil
}

func (s *Store) PurchaseByProviderReference(c context.Context, provider, environment, reference string) (BillingPurchase, error) {
	if provider == "" || environment == "" || reference == "" {
		return BillingPurchase{}, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE provider=? AND billing_environment=? AND provider_reference=?`, provider, environment, reference)
	if err != nil {
		return BillingPurchase{}, err
	}
	defer rows.Close()
	var purchase BillingPurchase
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return BillingPurchase{}, err
		}
		return BillingPurchase{}, sql.ErrNoRows
	}
	if purchase, err = scanPurchase(rows); err != nil {
		return BillingPurchase{}, err
	}
	if rows.Next() {
		return BillingPurchase{}, fmt.Errorf("provider reference matches multiple purchases")
	}
	return purchase, rows.Err()
}
func snapshotDigest(templates []commerce.GrantTemplate) string {
	parts := make([]string, 0, len(templates))
	for _, x := range templates {
		parts = append(parts, fmt.Sprintf("%s|%s|%d|%d|%s|%d", x.CapabilityID, x.Period, x.Limits.StorageBytes, x.Limits.EgressBytes, x.Limits.OperationMetric, x.Limits.OperationCount))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

type purchaseAuthorityQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func validatePurchaseCommercialAuthority(c context.Context, q purchaseAuthorityQuerier, purchase BillingPurchase, requireFinalized bool) ([]commerce.GrantTemplate, error) {
	required := []struct{ name, value string }{
		{"purchase identity", purchase.ID}, {"account", purchase.AccountID}, {"product", purchase.ProductID},
		{"offer", purchase.PlanCode}, {"rate card", purchase.RateCardID}, {"rate card mode", purchase.RateCardMode},
		{"tax classification", purchase.TaxClassification}, {"currency", purchase.Currency},
		{"provider", purchase.Provider}, {"billing environment", purchase.BillingEnvironment},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("commercial authority missing %s", field.name)
		}
	}
	if purchase.AmountMinor < 0 {
		return nil, fmt.Errorf("commercial authority has invalid amount")
	}
	from, fromErr := parseEntitlementTime(purchase.EntitlementPeriodStart)
	until, untilErr := parseEntitlementTime(purchase.EntitlementPeriodEnd)
	if fromErr != nil || untilErr != nil || !until.After(from) {
		return nil, fmt.Errorf("commercial authority has invalid validity")
	}
	var finalized int
	if err := q.QueryRowContext(c, `SELECT snapshot_finalized FROM billing_purchases WHERE id=?`, purchase.ID).Scan(&finalized); err != nil {
		return nil, fmt.Errorf("read commercial authority finalization: %w", err)
	}
	if requireFinalized && finalized != 1 {
		return nil, fmt.Errorf("commercial authority is not finalized")
	}
	if finalized != 0 && finalized != 1 {
		return nil, fmt.Errorf("commercial authority has invalid finalization state")
	}
	var expectedCount int
	var expectedDigest string
	if err := q.QueryRowContext(c, `SELECT expected_count,capability_digest FROM purchase_grant_snapshot_sets WHERE purchase_id=?`, purchase.ID).Scan(&expectedCount, &expectedDigest); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("commercial authority snapshot set missing")
		}
		return nil, fmt.Errorf("read commercial authority snapshot set: %w", err)
	}
	if expectedCount <= 0 || strings.TrimSpace(expectedDigest) == "" {
		return nil, fmt.Errorf("commercial authority snapshot set is invalid")
	}
	rows, err := q.QueryContext(c, `SELECT capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit FROM purchase_grant_snapshots WHERE purchase_id=? ORDER BY capability_id`, purchase.ID)
	if err != nil {
		return nil, fmt.Errorf("read commercial authority snapshots: %w", err)
	}
	defer rows.Close()
	templates := make([]commerce.GrantTemplate, 0, expectedCount)
	for rows.Next() {
		var template commerce.GrantTemplate
		if err = rows.Scan(&template.CapabilityID, &template.Period, &template.Limits.StorageBytes, &template.Limits.EgressBytes, &template.Limits.OperationMetric, &template.Limits.OperationCount); err != nil {
			return nil, fmt.Errorf("read commercial authority snapshot: %w", err)
		}
		if strings.TrimSpace(string(template.CapabilityID)) == "" {
			return nil, fmt.Errorf("commercial authority snapshot capability is empty")
		}
		if template.Period != commerce.PeriodAnnual && template.Period != commerce.PeriodMonthly && template.Period != commerce.PeriodContinuous {
			return nil, fmt.Errorf("commercial authority snapshot cadence is invalid")
		}
		if template.Limits.StorageBytes < 0 || template.Limits.EgressBytes < 0 || template.Limits.OperationCount < 0 || (template.Limits.OperationCount > 0 && template.Limits.OperationMetric == "") {
			return nil, fmt.Errorf("commercial authority snapshot limits are invalid")
		}
		// Current purchases arrive with catalog-authorized snapshots. Keep the
		// TPA capability check only for legacy/unversioned recovery records,
		// where it protects interpretation of historical data.
		if commerce.ProductID(purchase.ProductID) == tpahubproduct.ProductID && !tpahubproduct.ValidCapability(template.CapabilityID) {
			return nil, fmt.Errorf("commercial authority snapshot capability does not belong to product")
		}
		templates = append(templates, template)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read commercial authority snapshots: %w", err)
	}
	if len(templates) != expectedCount {
		return nil, fmt.Errorf("commercial authority snapshot count mismatch: expected=%d actual=%d", expectedCount, len(templates))
	}
	if snapshotDigest(templates) != expectedDigest {
		return nil, fmt.Errorf("commercial authority snapshot digest mismatch")
	}
	return templates, nil
}

func markPurchaseReconciliationRequired(c context.Context, tx *sql.Tx, purchase BillingPurchase, reference, updatedAt string, authorityErr error) error {
	reason := "commercial authority unavailable"
	if authorityErr != nil {
		reason = authorityErr.Error()
	}
	result, err := tx.ExecContext(c, `UPDATE billing_purchases SET provider_reference=?,checkout_url='',status='reconciliation_required',provider_state='checkout_recovered_reconciliation_required',reconciliation_reason=?,updated_at=? WHERE id=? AND provider=? AND billing_environment=? AND (provider_reference='' OR provider_reference=?) AND status IN ('pending','superseded','failed','expired','reconciliation_required')`, reference, reason, updatedAt, purchase.ID, purchase.Provider, purchase.BillingEnvironment, reference)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return countErr
	} else if count != 1 {
		return fmt.Errorf("purchase checkout recovery state changed")
	}
	return nil
}

// ApplyProviderPaymentEvent correlates and applies a verified provider event in
// one transaction. In particular, operation-ID webhook recovery must not commit
// reconciliation before its provider event evidence.
func (s *Store) ApplyProviderPaymentEvent(c context.Context, provider, environment string, event commerce.PaymentEvent) error {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(environment) == "" || strings.TrimSpace(event.OperationID) == "" || strings.TrimSpace(event.ProviderEventID) == "" || strings.TrimSpace(event.ProviderReference) == "" {
		return fmt.Errorf("provider event correlation is incomplete")
	}
	kind := map[commerce.PaymentEventType]string{commerce.PaymentSucceeded: "success", commerce.PaymentFailed: "failed", commerce.PaymentExpired: "expired"}[event.Type]
	if kind == "" {
		return fmt.Errorf("unsupported-payment-event")
	}
	// The store transaction below reloads the purchase and verifies amount,
	// currency, provider, environment, and reference against that same view;
	// do not perform a preflight read in a separate transaction.
	return s.applyBillingProviderEvent(c, billingProviderEvent{Provider: provider, Environment: environment, ProviderEventID: event.ProviderEventID, EventType: string(event.Type), ReceivedAt: event.OccurredAt.UTC().Format(time.RFC3339Nano)}, event.OperationID, event.ProviderReference, kind, event.AmountMinor, event.Currency, event.OccurredAt.UTC().Format(time.RFC3339Nano), "", "")
}

func (s *Store) ApplyPaymentTransition(c context.Context, transition commerce.PaymentTransition) error {
	state, err := s.PurchasePaymentState(c, transition.Event.OperationID)
	if err != nil {
		return err
	}
	if transition.Provider != state.Provider || transition.Environment != state.Environment || transition.Event.ProviderReference != state.ProviderReference || transition.AmountMinor != state.AmountMinor || transition.Currency != state.Currency || transition.Event.AmountMinor != state.AmountMinor || transition.Event.Currency != state.Currency {
		return fmt.Errorf("payment transition does not match finalized purchase authority")
	}
	received := transition.Event.OccurredAt.UTC().Format(time.RFC3339Nano)
	return s.applyBillingProviderEvent(c, billingProviderEvent{Provider: transition.Provider, Environment: transition.Environment, ProviderEventID: transition.Event.ProviderEventID, EventType: string(transition.Event.Type), ReceivedAt: received}, transition.Event.OperationID, transition.Event.ProviderReference, transition.Kind, transition.AmountMinor, transition.Currency, received, transition.ValidFrom.UTC().Format(time.RFC3339Nano), transition.ValidUntil.UTC().Format(time.RFC3339Nano))
}
func (s *Store) CreatePendingPurchase(c context.Context, p BillingPurchase, now string, templates ...commerce.GrantTemplate) (BillingPurchase, error) {
	if strings.TrimSpace(p.ProductID) == "" {
		return BillingPurchase{}, fmt.Errorf("purchase product ID is empty")
	}
	if strings.TrimSpace(p.OfferVersion) == "" {
		// Older/generic purchase callers predate offer versions. Preserve that
		// fact explicitly; never substitute the current product version.
		p.OfferVersion = "unversioned-v1"
	}
	if p.CheckoutRequestVersion == "" {
		p.CheckoutRequestVersion = "v2"
	}
	if p.ProviderState == "" {
		p.ProviderState = "open"
	}
	at, parseErr := time.Parse(time.RFC3339Nano, now)
	from, fromErr := parseEntitlementTime(p.EntitlementPeriodStart)
	until, untilErr := parseEntitlementTime(p.EntitlementPeriodEnd)
	if parseErr != nil || fromErr != nil || untilErr != nil || at.Before(from) || !at.Before(until) {
		return BillingPurchase{}, fmt.Errorf("purchase validity is not current")
	}
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return BillingPurchase{}, e
	}
	defer tx.Rollback()
	var reconciliationRequired int
	if e = tx.QueryRowContext(c, `SELECT COUNT(*) FROM billing_purchases WHERE account_id=? AND product_id=? AND provider=? AND billing_environment=? AND status='reconciliation_required'`, p.AccountID, p.ProductID, p.Provider, p.BillingEnvironment).Scan(&reconciliationRequired); e != nil {
		return BillingPurchase{}, e
	}
	if reconciliationRequired != 0 {
		return BillingPurchase{}, fmt.Errorf("purchase-reconciliation-required")
	}
	var existing BillingPurchase
	existing, e = scanPurchase(tx.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE account_id=? AND product_id=? AND provider=? AND billing_environment=? AND status='pending'`, p.AccountID, p.ProductID, p.Provider, p.BillingEnvironment))
	if e == nil {
		existingUntil, existingUntilErr := parseEntitlementTime(existing.EntitlementPeriodEnd)
		compatible := existing.AccountID == p.AccountID && existing.ProductID == p.ProductID && existing.PlanCode == p.PlanCode && existing.RateCardID == p.RateCardID && existing.RateCardMode == p.RateCardMode && existing.TaxClassification == p.TaxClassification && existing.Currency == p.Currency && existing.AmountMinor == p.AmountMinor && existing.Provider == p.Provider && existing.BillingEnvironment == p.BillingEnvironment && existing.CheckoutRequestVersion == p.CheckoutRequestVersion && existing.EntitlementPeriodStart == p.EntitlementPeriodStart && existing.EntitlementPeriodEnd == p.EntitlementPeriodEnd && existingUntilErr == nil && at.Before(existingUntil) && (existing.ProviderReference == "" || existing.CheckoutURL != "")
		if compatible {
			if e = tx.Commit(); e != nil {
				return BillingPurchase{}, e
			}
			return existing, nil
		}
		status := "failed"
		if existing.ProviderReference != "" || existing.CheckoutURL != "" {
			// The provider may still accept this checkout. Keep it payable and
			// reconciliable even though a newer local intent occupies the slot.
			status = "superseded"
		}
		if _, e = tx.ExecContext(c, `UPDATE billing_purchases SET status=?,failed_at=?,updated_at=? WHERE id=? AND status='pending'`, status, now, now, existing.ID); e != nil {
			return BillingPurchase{}, e
		}
	}
	if e != sql.ErrNoRows && e != nil {
		return BillingPurchase{}, e
	}
	// Purchase admission is scoped to this customer's product grant instances.
	grantRows, grantErr := tx.QueryContext(c, `SELECT enabled,valid_from,valid_until FROM purchase_grant_instances WHERE customer_id=? AND product_id=?`, p.AccountID, p.ProductID)
	if grantErr != nil {
		return BillingPurchase{}, grantErr
	}
	defer grantRows.Close()
	for grantRows.Next() {
		var grantFrom, grantUntil string
		var grantEnabled int
		if grantErr = grantRows.Scan(&grantEnabled, &grantFrom, &grantUntil); grantErr != nil {
			return BillingPurchase{}, grantErr
		}
		if grantEnabled != 1 {
			continue
		}
		from, parseErr := parseEntitlementTime(grantFrom)
		if parseErr != nil {
			return BillingPurchase{}, parseErr
		}
		if grantUntil == "" {
			return BillingPurchase{}, fmt.Errorf("purchase grant missing validity end")
		}
		until, parseErr := parseEntitlementTime(grantUntil)
		if parseErr != nil {
			return BillingPurchase{}, parseErr
		}
		if !at.Before(from) && at.Before(until) {
			return BillingPurchase{}, fmt.Errorf("purchase-already-active")
		}
	}
	if grantErr = grantRows.Err(); grantErr != nil {
		return BillingPurchase{}, grantErr
	}
	if grantErr = grantRows.Close(); grantErr != nil {
		return BillingPurchase{}, grantErr
	}
	_, e = tx.ExecContext(c, `INSERT INTO billing_purchases(`+purchaseColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.AccountID, p.ProductID, p.PlanCode, p.OfferVersion, p.RateCardID, p.RateCardMode, p.TaxClassification, p.Currency, p.AmountMinor, p.BillingEnvironment, p.Provider, p.Status, p.EntitlementPeriodStart, p.EntitlementPeriodEnd, p.ProviderReference, p.CheckoutURL, p.CheckoutRequestVersion, p.CreatedAt, p.UpdatedAt, p.PaidAt, p.FailedAt, p.ExpiredAt, p.ProviderState, p.ReconciliationReason)
	if e != nil {
		return BillingPurchase{}, e
	}
	seenCapabilities := make(map[commerce.CapabilityID]struct{}, len(templates))
	for _, template := range templates {
		if template.CapabilityID == "" {
			return BillingPurchase{}, fmt.Errorf("purchase grant snapshot capability is empty")
		}
		if _, exists := seenCapabilities[template.CapabilityID]; exists {
			return BillingPurchase{}, fmt.Errorf("duplicate purchase grant snapshot capability: %s", template.CapabilityID)
		}
		seenCapabilities[template.CapabilityID] = struct{}{}
		if _, e = tx.ExecContext(c, `INSERT INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit) VALUES(?,?,?,?,?,?,?)`, p.ID, template.CapabilityID, template.Period, template.Limits.StorageBytes, template.Limits.EgressBytes, template.Limits.OperationMetric, template.Limits.OperationCount); e != nil {
			return BillingPurchase{}, e
		}
	}
	if len(templates) == 0 {
		return BillingPurchase{}, fmt.Errorf("purchase grant snapshot is empty")
	}
	if _, e = tx.ExecContext(c, `INSERT INTO purchase_grant_snapshot_sets(purchase_id,expected_count,capability_digest) VALUES(?,?,?)`, p.ID, len(templates), snapshotDigest(templates)); e != nil {
		return BillingPurchase{}, e
	}
	if e = tx.Commit(); e != nil {
		return BillingPurchase{}, e
	}
	return p, nil
}
func (s *Store) FailPendingPurchase(c context.Context, id, failedAt string) error {
	_, e := s.db.ExecContext(c, `UPDATE billing_purchases SET status='failed',provider_state='failed',failed_at=?,updated_at=? WHERE id=? AND status='pending'`, failedAt, failedAt, id)
	return e
}

func (s *Store) UpdatePurchaseCheckout(c context.Context, id, reference, checkoutURL string, updatedAt string) error {
	if strings.TrimSpace(reference) == "" {
		return fmt.Errorf("purchase provider reference is empty")
	}
	at, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return fmt.Errorf("purchase checkout time is invalid: %w", err)
	}
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	purchase, err := scanPurchase(tx.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE id=?`, id))
	if err != nil {
		return err
	}
	from, fromErr := parseEntitlementTime(purchase.EntitlementPeriodStart)
	until, untilErr := parseEntitlementTime(purchase.EntitlementPeriodEnd)
	if fromErr != nil || untilErr != nil || at.Before(from) || !at.Before(until) {
		return fmt.Errorf("purchase validity is invalid")
	}
	if _, authorityErr := validatePurchaseCommercialAuthority(c, tx, purchase, false); authorityErr != nil {
		if err = markPurchaseReconciliationRequired(c, tx, purchase, reference, updatedAt, authorityErr); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return fmt.Errorf("commercial authority unavailable: %w", authorityErr)
	}
	res, e := tx.ExecContext(c, `UPDATE billing_purchases SET provider_reference=?,checkout_url=?,snapshot_finalized=1,provider_state='checkout_created',reconciliation_reason='',updated_at=? WHERE id=? AND status='pending' AND snapshot_finalized=0`, reference, checkoutURL, updatedAt, id)
	if e != nil {
		return e
	}
	if n, e := res.RowsAffected(); e != nil {
		return e
	} else if n != 1 {
		return fmt.Errorf("purchase checkout state changed")
	}
	return tx.Commit()
}

// RecoverPurchaseCheckout finalizes a provider-created checkout whose local
// reference was not persisted before a process interruption. The signed
// provider event supplies only provider linkage: commercial validity remains
// exactly the product-owned purchase terms persisted before CreateCheckout.
// Provider event, delivery, replay, and current wall-clock times must never
// define or extend those terms.
func (s *Store) RecoverPurchaseCheckout(c context.Context, id, provider, environment, reference, updatedAt string) error {
	if strings.TrimSpace(reference) == "" {
		return fmt.Errorf("purchase provider reference is empty")
	}
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	purchase, err := scanPurchase(tx.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE id=?`, id))
	if err != nil {
		return err
	}
	if purchase.Provider != provider || purchase.BillingEnvironment != environment || (purchase.ProviderReference != "" && purchase.ProviderReference != reference) {
		return fmt.Errorf("purchase checkout recovery authority mismatch")
	}
	if purchase.Status == "reconciliation_required" && purchase.ProviderReference == reference {
		return tx.Commit()
	}
	_, authorityErr := validatePurchaseCommercialAuthority(c, tx, purchase, false)
	if authorityErr != nil {
		// Provider linkage is evidence, never replacement commercial authority.
		// Preserve the exact local terms and route every intrinsic authority defect
		// to durable, zero-access reconciliation before activation is attempted.
		if err = markPurchaseReconciliationRequired(c, tx, purchase, reference, updatedAt, authorityErr); err != nil {
			return err
		}
		return tx.Commit()
	}
	var snapshotFinalized int
	if err = tx.QueryRowContext(c, `SELECT snapshot_finalized FROM billing_purchases WHERE id=?`, id).Scan(&snapshotFinalized); err != nil {
		return err
	}
	if purchase.ProviderReference == reference && snapshotFinalized == 1 {
		return tx.Commit()
	}
	result, err := tx.ExecContext(c, `UPDATE billing_purchases SET provider_reference=?,checkout_url='',snapshot_finalized=1,provider_state='checkout_created',reconciliation_reason='',updated_at=? WHERE id=? AND provider=? AND billing_environment=? AND (provider_reference='' OR provider_reference=?) AND snapshot_finalized=0 AND status IN ('pending','superseded','failed','expired')`, reference, updatedAt, id, provider, environment, reference)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return countErr
	} else if count != 1 {
		return fmt.Errorf("purchase checkout recovery state changed")
	}
	return tx.Commit()
}
func (s *Store) applyBillingProviderEvent(c context.Context, event billingProviderEvent, purchaseID, providerReference string, kind string, amount int64, currency, paidAt, entitlementStart, entitlementEnd string) error {
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(c, `INSERT OR IGNORE INTO billing_provider_events(provider,environment,provider_event_id,event_type,received_at,status,purchase_id,provider_reference) VALUES(?,?,?,?,?,'processing',?,?)`, event.Provider, event.Environment, event.ProviderEventID, event.EventType, event.ReceivedAt, purchaseID, providerReference)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return tx.Commit()
	}
	p, e := scanPurchase(tx.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE id=?`, purchaseID))
	if e != nil {
		return e
	}
	if p.Provider != event.Provider || p.BillingEnvironment != event.Environment || (p.ProviderReference != "" && p.ProviderReference != providerReference) {
		return fmt.Errorf("provider-reference-mismatch")
	}
	if e = providerEventCheckpoint("after-event-insert"); e != nil {
		return e
	}
	if p.ProviderReference == "" && p.Status != "reconciliation_required" {
		_, authorityErr := validatePurchaseCommercialAuthority(c, tx, p, false)
		if authorityErr != nil {
			if e = markPurchaseReconciliationRequired(c, tx, p, providerReference, event.ReceivedAt, authorityErr); e != nil {
				return e
			}
			if e = providerEventCheckpoint("after-reconciliation"); e != nil {
				return e
			}
			providerState := "provider_" + kind + "_unreconciled"
			if _, e = tx.ExecContext(c, `UPDATE billing_purchases SET provider_state=?,updated_at=? WHERE id=? AND status='reconciliation_required'`, providerState, event.ReceivedAt, purchaseID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(c, `UPDATE billing_provider_events SET processed_at=?,status='reconciliation_required',error=? WHERE provider=? AND environment=? AND provider_event_id=?`, event.ReceivedAt, authorityErr.Error(), event.Provider, event.Environment, event.ProviderEventID); e != nil {
				return e
			}
			return tx.Commit()
		}
		if _, e = tx.ExecContext(c, `UPDATE billing_purchases SET provider_reference=?,checkout_url='',snapshot_finalized=1,provider_state='checkout_created',reconciliation_reason='',updated_at=? WHERE id=? AND provider=? AND billing_environment=? AND provider_reference='' AND snapshot_finalized=0 AND status IN ('pending','superseded','failed','expired')`, providerReference, event.ReceivedAt, purchaseID, event.Provider, event.Environment); e != nil {
			return e
		}
		p.ProviderReference = providerReference
	}
	if p.Status == "reconciliation_required" {
		providerState := "provider_" + kind + "_unreconciled"
		// Positive payment evidence wins over later failure/expiry notifications,
		// matching the precedence of a normally activated paid purchase.
		if p.ProviderState == "provider_success_unreconciled" {
			providerState = p.ProviderState
		}
		if _, e = tx.ExecContext(c, `UPDATE billing_purchases SET provider_state=?,updated_at=? WHERE id=? AND status='reconciliation_required'`, providerState, event.ReceivedAt, purchaseID); e != nil {
			return e
		}
		reason := p.ReconciliationReason
		if reason == "" {
			reason = "commercial authority unavailable"
		}
		if _, e = tx.ExecContext(c, `UPDATE billing_provider_events SET processed_at=?,status='reconciliation_required',error=? WHERE provider=? AND environment=? AND provider_event_id=?`, event.ReceivedAt, reason, event.Provider, event.Environment, event.ProviderEventID); e != nil {
			return e
		}
		return tx.Commit()
	}
	if kind == "success" {
		if p.Status == "paid" {
			_, e = tx.ExecContext(c, `UPDATE billing_provider_events SET processed_at=?,status='processed' WHERE provider=? AND environment=? AND provider_event_id=?`, event.ReceivedAt, event.Provider, event.Environment, event.ProviderEventID)
			if e != nil {
				return e
			}
			return tx.Commit()
		}
		payableTerminal := (p.Status == "failed" || p.Status == "expired") && p.ProviderReference != ""
		if (p.Status != "pending" && p.Status != "superseded" && !payableTerminal) || p.AmountMinor != amount || p.Currency != currency {
			return fmt.Errorf("payment-verification-failed")
		}
		if _, authorityErr := validatePurchaseCommercialAuthority(c, tx, p, true); authorityErr != nil {
			if e = markPurchaseReconciliationRequired(c, tx, p, providerReference, event.ReceivedAt, authorityErr); e != nil {
				return e
			}
			if _, e = tx.ExecContext(c, `UPDATE billing_purchases SET provider_state='provider_success_unreconciled',updated_at=? WHERE id=? AND status='reconciliation_required'`, event.ReceivedAt, purchaseID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(c, `UPDATE billing_provider_events SET processed_at=?,status='reconciliation_required',error=? WHERE provider=? AND environment=? AND provider_event_id=?`, event.ReceivedAt, authorityErr.Error(), event.Provider, event.Environment, event.ProviderEventID); e != nil {
				return e
			}
			return tx.Commit()
		}
		var snapshotFinalized int
		if e = tx.QueryRowContext(c, `SELECT snapshot_finalized FROM billing_purchases WHERE id=?`, purchaseID).Scan(&snapshotFinalized); e != nil {
			return e
		}
		if snapshotFinalized != 1 || p.ProviderReference == "" || p.EntitlementPeriodStart == "" || p.EntitlementPeriodEnd == "" {
			return fmt.Errorf("finalized purchase terms incomplete")
		}
		finalizedFrom, fromErr := parseEntitlementTime(p.EntitlementPeriodStart)
		finalizedUntil, untilErr := parseEntitlementTime(p.EntitlementPeriodEnd)
		if fromErr != nil || untilErr != nil || !finalizedUntil.After(finalizedFrom) {
			return fmt.Errorf("finalized purchase validity invalid")
		}
		entitlementStart, entitlementEnd = p.EntitlementPeriodStart, p.EntitlementPeriodEnd
		var snapshotCount, expectedCount int
		var expectedDigest string
		if e = tx.QueryRowContext(c, `SELECT expected_count,capability_digest FROM purchase_grant_snapshot_sets WHERE purchase_id=?`, purchaseID).Scan(&expectedCount, &expectedDigest); e != nil {
			return e
		}
		if e = tx.QueryRowContext(c, `SELECT COUNT(*) FROM purchase_grant_snapshots WHERE purchase_id=?`, purchaseID).Scan(&snapshotCount); e != nil {
			return e
		}
		if snapshotCount == 0 {
			return fmt.Errorf("grant activation unavailable: purchase grant snapshot missing")
		}
		if e == nil {
			rows, queryErr := tx.QueryContext(c, `SELECT capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit FROM purchase_grant_snapshots WHERE purchase_id=? ORDER BY capability_id`, purchaseID)
			if queryErr != nil {
				e = queryErr
			} else {
				activated := 0
				var digestParts []string
				for rows.Next() {
					var capability commerce.CapabilityID
					var period commerce.PeriodCadence
					var limits commerce.Limits
					if scanErr := rows.Scan(&capability, &period, &limits.StorageBytes, &limits.EgressBytes, &limits.OperationMetric, &limits.OperationCount); scanErr != nil {
						e = scanErr
						break
					}
					digestParts = append(digestParts, fmt.Sprintf("%s|%s|%d|%d|%s|%d", capability, period, limits.StorageBytes, limits.EgressBytes, limits.OperationMetric, limits.OperationCount))
					var existing struct {
						customerID, productID, offerID, validFrom, validUntil, existingPeriod, operationMetric, createdAt, updatedAt string
						enabled, storageBytes, egressBytes, operationLimit                                                           int64
					}
					lookupErr := tx.QueryRowContext(c, `SELECT customer_id,product_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,created_at,updated_at FROM purchase_grant_instances WHERE purchase_id=? AND capability_id=?`, purchaseID, capability).Scan(&existing.customerID, &existing.productID, &existing.offerID, &existing.enabled, &existing.validFrom, &existing.validUntil, &existing.existingPeriod, &existing.storageBytes, &existing.egressBytes, &existing.operationMetric, &existing.operationLimit, &existing.createdAt, &existing.updatedAt)
					if lookupErr == sql.ErrNoRows {
						_, lookupErr = tx.ExecContext(c, `INSERT INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.AccountID, p.ProductID, capability, purchaseID, p.PlanCode, true, entitlementStart, entitlementEnd, period, limits.StorageBytes, limits.EgressBytes, limits.OperationMetric, limits.OperationCount, paidAt, paidAt)
					} else if lookupErr == nil && (existing.customerID != p.AccountID || existing.productID != p.ProductID || existing.offerID != p.PlanCode || existing.enabled != 1 || existing.validFrom != entitlementStart || existing.validUntil != entitlementEnd || existing.existingPeriod != string(period) || existing.storageBytes != limits.StorageBytes || existing.egressBytes != limits.EgressBytes || existing.operationMetric != string(limits.OperationMetric) || existing.operationLimit != limits.OperationCount || existing.createdAt != paidAt || existing.updatedAt != paidAt) {
						lookupErr = fmt.Errorf("pre-existing purchase grant instance conflicts with finalized purchase")
					}
					if lookupErr != nil {
						e = lookupErr
						break
					}
					activated++
				}
				if closeErr := rows.Close(); e == nil && closeErr != nil {
					e = closeErr
				}
				var instanceCount int
				if e == nil {
					e = tx.QueryRowContext(c, `SELECT COUNT(*) FROM purchase_grant_instances WHERE purchase_id=?`, purchaseID).Scan(&instanceCount)
				}
				if e == nil && (activated != snapshotCount || snapshotCount != expectedCount || instanceCount != expectedCount) {
					e = fmt.Errorf("grant activation incomplete")
				}
				if e == nil {
					sort.Strings(digestParts)
					sum := sha256.Sum256([]byte(strings.Join(digestParts, "\n")))
					if hex.EncodeToString(sum[:]) != expectedDigest {
						e = fmt.Errorf("purchase grant snapshot set mismatch")
					}
				}
			}
		}
		if e == nil {
			_, e = tx.ExecContext(c, `UPDATE billing_purchases SET status='paid',provider_state='paid',paid_at=?,updated_at=?,entitlement_period_start=?,entitlement_period_end=? WHERE id=? AND status IN ('pending','superseded','failed','expired')`, paidAt, paidAt, entitlementStart, entitlementEnd, purchaseID)
		}
		// Activation records immutable purchase grant instances and evidence;
		// local and administrative grants remain independent authority.
	} else if p.Status == "pending" || p.Status == "superseded" {
		statusField := "failed_at"
		status := "failed"
		if kind == "expired" {
			statusField = "expired_at"
			status = "expired"
		}
		_, e = tx.ExecContext(c, `UPDATE billing_purchases SET status=?,provider_state=?,`+statusField+`=?,updated_at=? WHERE id=? AND status IN ('pending','superseded')`, status, status, event.ReceivedAt, event.ReceivedAt, purchaseID)
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(c, `UPDATE billing_provider_events SET processed_at=?,status='processed' WHERE provider=? AND environment=? AND provider_event_id=?`, event.ReceivedAt, event.Provider, event.Environment, event.ProviderEventID)
	if e != nil {
		return e
	}
	return tx.Commit()
}

// LegacyTPARecordMockBillingEvent is a compatibility wrapper for the old
// TPA Hub-only mock billing API. New callers must select a product explicitly.
func (s *Store) LegacyTPARecordMockBillingEvent(c context.Context, id, accountID, period, eventType, payload, createdAt string) error {
	return s.RecordProductMockBillingEvent(c, id, accountID, string(tpahubproduct.ProductID), period, eventType, payload, createdAt)
}
func (s *Store) RecordProductMockBillingEvent(c context.Context, id, accountID, productID, period, eventType, payload, createdAt string) error {
	if strings.TrimSpace(productID) == "" {
		return fmt.Errorf("mock billing event product ID is empty")
	}
	_, e := s.db.ExecContext(c, `INSERT INTO mock_billing_events(id,account_id,product_id,period,event_type,payload,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,product_id,period,event_type) DO NOTHING`, id, accountID, productID, period, eventType, payload, createdAt)
	return e
}
func (s *Store) RepositoryEgress(c context.Context, repositoryID, period string) (int64, error) {
	var total int64
	e := s.db.QueryRowContext(c, `SELECT COALESCE(egress_bytes,0) FROM repository_egress_usage WHERE repository_id=? AND period=?`, repositoryID, period).Scan(&total)
	if e == nil && total > 0 {
		if len(period) == len("2006-01") {
			var daily int64
			de := s.db.QueryRowContext(c, `SELECT COALESCE(SUM(egress_bytes),0) FROM repository_egress_daily WHERE repository_id=? AND usage_date>=? AND usage_date<?`, repositoryID, period+"-01", period+"-32").Scan(&daily)
			if de == nil {
				return total + daily, nil
			}
		}
		return total, nil
	}
	// Preserve this historical query API while daily accounting is authoritative.
	if len(period) == len("2006-01") {
		e = s.db.QueryRowContext(c, `SELECT COALESCE(SUM(egress_bytes),0) FROM repository_egress_daily WHERE repository_id=? AND usage_date>=? AND usage_date<?`, repositoryID, period+"-01", period+"-32").Scan(&total)
		return total, e
	}
	if e == sql.ErrNoRows {
		return 0, nil
	}
	return total, e
}
func (s *Store) CreateAccount(c context.Context, a Account, grants ...commerce.Grant) error {
	for _, grant := range grants {
		if strings.TrimSpace(string(grant.ProductID)) == "" {
			return fmt.Errorf("grant product ID is empty")
		}
	}
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(c, "INSERT INTO accounts VALUES(?,?,?,?,?)", a.ID, a.Email, a.PasswordHash, a.Status, a.CreatedAt); e != nil {
		return e
	}
	class := a.AccountClass
	if class == "" {
		class = AccountClassUnclassified
	}
	if !validAccountClass(class) {
		return fmt.Errorf("invalid account class")
	}
	if _, e = tx.ExecContext(c, "INSERT INTO account_metadata(account_id,account_class) VALUES(?,?)", a.ID, class); e != nil {
		return e
	}
	for _, grant := range grants {
		grant.CustomerID = commerce.CustomerID(a.ID)
		validUntil := ""
		if grant.ValidUntil != nil {
			validUntil = grant.ValidUntil.UTC().Format(time.RFC3339Nano)
		}
		if _, e = tx.ExecContext(c, `INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, grant.CustomerID, grant.ProductID, grant.CapabilityID, grant.OfferID, grant.Enabled, grant.ValidFrom.UTC().Format(time.RFC3339Nano), validUntil, grant.Period, grant.Limits.StorageBytes, grant.Limits.EgressBytes, grant.Limits.OperationMetric, grant.Limits.OperationCount, grant.Source, grant.PurchaseID, grant.CreatedAt.UTC().Format(time.RFC3339Nano), grant.UpdatedAt.UTC().Format(time.RFC3339Nano)); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) AccountByID(c context.Context, id string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT a.id,a.email,a.password_hash,a.status,a.created_at,COALESCE(m.account_class,?) FROM accounts a LEFT JOIN account_metadata m ON m.account_id=a.id WHERE a.id=?", AccountClassUnclassified, id).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt, &a.AccountClass)
	return a, e
}
func (s *Store) AccountByEmail(c context.Context, email string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT a.id,a.email,a.password_hash,a.status,a.created_at,COALESCE(m.account_class,?) FROM accounts a LEFT JOIN account_metadata m ON m.account_id=a.id WHERE a.email=?", AccountClassUnclassified, email).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt, &a.AccountClass)
	return a, e
}
func (s *Store) ActiveAccountByEmail(c context.Context, email string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT a.id,a.email,a.password_hash,a.status,a.created_at,COALESCE(m.account_class,?) FROM accounts a LEFT JOIN account_metadata m ON m.account_id=a.id WHERE a.email=? AND a.status=?", AccountClassUnclassified, email, AccountStatusActive).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt, &a.AccountClass)
	return a, e
}
func (s *Store) SetAccountStatus(c context.Context, id, status string) error {
	_, e := s.db.ExecContext(c, `UPDATE accounts SET status=? WHERE id=?`, status, id)
	return e
}
func (s *Store) CreateSession(c context.Context, x Session, hash string) error {
	_, e := s.db.ExecContext(c, "INSERT INTO sessions(id,account_id,token_prefix,token_hash,created_at) VALUES(?,?,?,?,?)", x.ID, x.AccountID, x.TokenPrefix, hash, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func (s *Store) AccountByToken(c context.Context, hash string) (string, error) {
	var id string
	e := s.db.QueryRowContext(c, `SELECT s.account_id FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.token_hash=? AND s.revoked_at IS NULL AND a.status=?`, hash, AccountStatusActive).Scan(&id)
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
	if r.Visibility == "" {
		r.Visibility = "public"
	}
	if r.AccountID == "" {
		return fmt.Errorf("repository owner is required")
	}
	_, e := s.db.ExecContext(c, "INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES(?,?,?,?,?)", r.ID, r.AccountID, r.Name, r.Visibility, r.CreatedAt)
	return e
}

func (s *Store) SetRepositoryVisibility(c context.Context, id, visibility string) error {
	if visibility != "private" && visibility != "public" {
		return fmt.Errorf("invalid repository visibility")
	}
	result, err := s.db.ExecContext(c, "UPDATE repositories SET visibility=? WHERE id=?", visibility, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

// DeleteRepository removes all repository-owned durable rows in one
// transaction. The caller must hold the server publication lock and remove
// the validated filesystem root after this succeeds.
func (s *Store) DeleteRepository(c context.Context, id string) error {
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(c, "SELECT 1 FROM repositories WHERE id=?", id).Scan(&exists); err != nil {
		return err
	}
	var active int
	if err = tx.QueryRowContext(c, `SELECT COUNT(*) FROM publish_jobs WHERE repository_id=? AND (state NOT IN ('succeeded','failed') OR cleanup_state='pending')`, id).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrRepositoryBusy
	}
	var uncertain int
	if err = tx.QueryRowContext(c, "SELECT COUNT(*) FROM egress_accounting_uncertain WHERE repository_id=?", id).Scan(&uncertain); err != nil {
		return err
	}
	if uncertain != 0 {
		return fmt.Errorf("repository has uncertain egress accounting")
	}
	var bootstrap int
	if err = tx.QueryRowContext(c, "SELECT COUNT(*) FROM global_bootstrap_repository WHERE repository_id=?", id).Scan(&bootstrap); err != nil {
		return err
	}
	if bootstrap != 0 {
		return fmt.Errorf("global bootstrap repository cannot be deleted")
	}
	for _, statement := range []string{
		"DELETE FROM egress_accounting_uncertain WHERE repository_id=?",
		"DELETE FROM generation_cleanup_tasks WHERE repository_id=?",
		"DELETE FROM reader_credentials WHERE repository_id=?",
		"DELETE FROM repository_egress_daily WHERE repository_id=?",
		"DELETE FROM repository_egress_usage WHERE repository_id=?",
		"DELETE FROM repository_publications WHERE repository_id=?",
		"DELETE FROM publish_jobs WHERE repository_id=?",
		"DELETE FROM repositories WHERE id=?",
	} {
		if _, err = tx.ExecContext(c, statement, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) SetActiveGeneration(c context.Context, repositoryID, generation, updatedAt string) error {
	_, err := s.db.ExecContext(c, `INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES(?,?,?) ON CONFLICT(repository_id) DO UPDATE SET active_generation=excluded.active_generation,updated_at=excluded.updated_at`, repositoryID, generation, updatedAt)
	return err
}

// AuthorizedPublication contains only durable facts established by the
// product policy boundary. Store verifies the repository identity is still
// bound to the same owner, then performs the atomic publication transition.
type AuthorizedPublication struct {
	RepositoryID, JobID, OwnerAccountID string
}

func (s *Store) CommitAuthorizedPublication(c context.Context, publication AuthorizedPublication, activeAt, updatedAt string) error {
	repositoryID, jobID := publication.RepositoryID, publication.JobID
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(c, `SELECT COALESCE(account_id,'') FROM repositories WHERE id=?`, repositoryID).Scan(&owner); err != nil {
		return err
	}
	if owner == "" || owner != publication.OwnerAccountID {
		return fmt.Errorf("publication repository owner changed before commit")
	}
	if _, err = tx.ExecContext(c, `INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES(?,?,?) ON CONFLICT(repository_id) DO UPDATE SET active_generation=excluded.active_generation,updated_at=excluded.updated_at`, repositoryID, jobID, updatedAt); err != nil {
		return err
	}
	result, err := tx.ExecContext(c, `UPDATE publish_jobs SET state='succeeded',error='',finished_at=?,cleanup_state='pending',cleanup_error='' WHERE id=? AND repository_id=? AND state='running'`, updatedAt, jobID, repositoryID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected != 1 {
		return fmt.Errorf("publication job state changed before commit")
	}
	return tx.Commit()
}

// CommitPublication is retained for old low-level callers and migration-era
// tests. Current application publication uses CommitAuthorizedPublication;
// this compatibility method performs no product authorization.
func (s *Store) CommitPublication(c context.Context, repositoryID, jobID, productID, capabilityID, activeAt, updatedAt string) error {
	r, err := s.Repository(c, repositoryID)
	if err != nil {
		return err
	}
	return s.CommitAuthorizedPublication(c, AuthorizedPublication{RepositoryID: repositoryID, JobID: jobID, OwnerAccountID: r.AccountID}, activeAt, updatedAt)
}

func (s *Store) ActiveGeneration(c context.Context, repositoryID string) (string, error) {
	var generation string
	err := s.db.QueryRowContext(c, `SELECT active_generation FROM repository_publications WHERE repository_id=?`, repositoryID).Scan(&generation)
	return generation, err
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

// PublicRepositories returns only public repository identities suitable for
// anonymous discovery. Private repositories are intentionally excluded here.
func (s *Store) PublicRepositories(c context.Context) ([]Repository, error) {
	rows, err := s.db.QueryContext(c, "SELECT id,COALESCE(account_id,''),name,visibility,created_at FROM repositories WHERE visibility='public' ORDER BY name,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repositories []Repository
	for rows.Next() {
		var repository Repository
		if err = rows.Scan(&repository.ID, &repository.AccountID, &repository.Name, &repository.Visibility, &repository.CreatedAt); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) AllRepositories(c context.Context) ([]Repository, error) {
	rows, err := s.db.QueryContext(c, "SELECT id,COALESCE(account_id,''),name,visibility,created_at FROM repositories ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repositories []Repository
	for rows.Next() {
		var repository Repository
		if err = rows.Scan(&repository.ID, &repository.AccountID, &repository.Name, &repository.Visibility, &repository.CreatedAt); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

type GenerationCleanupTask struct {
	RepositoryID, NextRetry, LastError string
	Attempts                           int64
}

func (s *Store) PendingGenerationCleanup(c context.Context) ([]GenerationCleanupTask, error) {
	rows, err := s.db.QueryContext(c, `SELECT repository_id,attempts,next_retry,last_error FROM generation_cleanup_tasks ORDER BY updated_at,repository_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []GenerationCleanupTask
	for rows.Next() {
		var task GenerationCleanupTask
		if err = rows.Scan(&task.RepositoryID, &task.Attempts, &task.NextRetry, &task.LastError); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) SetGenerationCleanupRetry(c context.Context, repositoryID string, cleanupErr error, nextRetry, updatedAt time.Time) error {
	if cleanupErr == nil {
		return s.ClearGenerationCleanup(c, repositoryID)
	}
	_, err := s.db.ExecContext(c, `INSERT INTO generation_cleanup_tasks(repository_id,attempts,next_retry,last_error,updated_at) VALUES(?,1,?,?,?) ON CONFLICT(repository_id) DO UPDATE SET attempts=attempts+1,next_retry=excluded.next_retry,last_error=excluded.last_error,updated_at=excluded.updated_at`, repositoryID, nextRetry.UTC().Format(time.RFC3339Nano), cleanupErr.Error(), updatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ClearGenerationCleanup(c context.Context, repositoryID string) error {
	_, err := s.db.ExecContext(c, `DELETE FROM generation_cleanup_tasks WHERE repository_id=?`, repositoryID)
	return err
}
func (s *Store) CreateJob(c context.Context, j Job) error {
	_, e := s.db.ExecContext(c, "INSERT INTO publish_jobs(id,repository_id,state,incoming_path,created_at) VALUES(?,?,?,?,?)", j.ID, j.RepositoryID, j.State, j.IncomingPath, j.CreatedAt)
	return e
}
func (s *Store) AccountStagingPaths(c context.Context, accountID string) ([]string, error) {
	rows, err := s.db.QueryContext(c, `SELECT j.incoming_path FROM publish_jobs j JOIN repositories r ON r.id=j.repository_id WHERE r.account_id=? AND (j.state IN ('queued','running') OR j.cleanup_state='pending')`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (s *Store) ActiveStagingPaths(c context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(c, `SELECT incoming_path FROM publish_jobs WHERE state IN ('queued','running') AND incoming_path<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (s *Store) HasNonterminalJobs(c context.Context, repositoryID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(c, `SELECT COUNT(*) FROM publish_jobs WHERE repository_id=? AND state NOT IN ('succeeded','failed')`, repositoryID).Scan(&count)
	return count > 0, err
}

func (s *Store) TerminalJobIncomingPaths(c context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(c, `SELECT incoming_path FROM publish_jobs WHERE state IN ('failed','succeeded') AND incoming_path<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (s *Store) Job(c context.Context, id string) (Job, error) {
	var j Job
	e := s.db.QueryRowContext(c, "SELECT id,repository_id,state,incoming_path,error,created_at,COALESCE(started_at,''),COALESCE(finished_at,''),cleanup_state,cleanup_error,cleanup_attempts,cleanup_next_retry FROM publish_jobs WHERE id=?", id).Scan(&j.ID, &j.RepositoryID, &j.State, &j.IncomingPath, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.CleanupState, &j.CleanupError, &j.CleanupAttempts, &j.CleanupNextRetry)
	return j, e
}
func (s *Store) Claim(c context.Context) (Job, error) {
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var j Job
	e = tx.QueryRowContext(c, "SELECT id,repository_id,state,incoming_path,error,created_at,COALESCE(started_at,''),COALESCE(finished_at,''),cleanup_state,cleanup_error,cleanup_attempts,cleanup_next_retry FROM publish_jobs WHERE state='queued' ORDER BY created_at LIMIT 1").Scan(&j.ID, &j.RepositoryID, &j.State, &j.IncomingPath, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.CleanupState, &j.CleanupError, &j.CleanupAttempts, &j.CleanupNextRetry)
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
	cleanup := "complete"
	nextRetry := ""
	if state == "succeeded" || state == "failed" {
		cleanup = "pending"
		nextRetry = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, e := s.db.ExecContext(c, "UPDATE publish_jobs SET state=?,error=?,finished_at=?,cleanup_state=?,cleanup_error='',cleanup_attempts=0,cleanup_next_retry=? WHERE id=?", state, msg, time.Now().UTC().Format(time.RFC3339Nano), cleanup, nextRetry, id)
	return e
}

type CleanupJob struct {
	ID, RepositoryID, IncomingPath, CleanupError, NextRetry string
	Attempts                                                int64
}

type AdminAuditEntry struct {
	ID, Action, MarkerID, AccountID, RepositoryID, PreviousState, Result, Operator, CreatedAt, Details string
	PreviousVersion                                                                                    int64
}

func (s *Store) AdminAuditEntries(c context.Context, markerID string) ([]AdminAuditEntry, error) {
	rows, err := s.db.QueryContext(c, `SELECT id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at,details FROM admin_audit_log WHERE marker_id=? ORDER BY created_at,id`, markerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AdminAuditEntry
	for rows.Next() {
		var entry AdminAuditEntry
		if err = rows.Scan(&entry.ID, &entry.Action, &entry.MarkerID, &entry.AccountID, &entry.RepositoryID, &entry.PreviousState, &entry.PreviousVersion, &entry.Result, &entry.Operator, &entry.CreatedAt, &entry.Details); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) PendingCleanupJobs(c context.Context) ([]CleanupJob, error) {
	rows, err := s.db.QueryContext(c, `SELECT id,repository_id,incoming_path,cleanup_error,cleanup_attempts,cleanup_next_retry FROM publish_jobs WHERE state IN ('succeeded','failed') AND cleanup_state='pending' ORDER BY finished_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []CleanupJob
	for rows.Next() {
		var j CleanupJob
		if err = rows.Scan(&j.ID, &j.RepositoryID, &j.IncomingPath, &j.CleanupError, &j.Attempts, &j.NextRetry); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) SetCleanupResult(c context.Context, id string, cleanupErr error) error {
	state, message := "complete", ""
	if cleanupErr != nil {
		return s.SetCleanupRetry(c, id, cleanupErr, time.Now().UTC().Add(100*time.Millisecond))
	}
	_, err := s.db.ExecContext(c, `UPDATE publish_jobs SET cleanup_state=?,cleanup_error=?,cleanup_attempts=0,cleanup_next_retry='' WHERE id=? AND state IN ('succeeded','failed')`, state, message, id)
	return err
}

func (s *Store) SetCleanupRetry(c context.Context, id string, cleanupErr error, nextRetry time.Time) error {
	if cleanupErr == nil {
		return s.SetCleanupResult(c, id, nil)
	}
	_, err := s.db.ExecContext(c, `UPDATE publish_jobs SET cleanup_state='pending',cleanup_error=?,cleanup_attempts=cleanup_attempts+1,cleanup_next_retry=? WHERE id=? AND state IN ('succeeded','failed')`, cleanupErr.Error(), nextRetry.UTC().Format(time.RFC3339Nano), id)
	return err
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
	e := s.db.QueryRowContext(c, "SELECT 1 FROM reader_credentials rc JOIN repositories r ON r.id=rc.repository_id JOIN accounts a ON a.id=r.account_id WHERE rc.repository_id=? AND rc.token_hash=? AND rc.revoked_at IS NULL AND a.status=?", repo, hash, AccountStatusActive).Scan(&n)
	return e == nil, e
}
