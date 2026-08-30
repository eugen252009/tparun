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
	"strings"
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
	_, e = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT); CREATE TABLE IF NOT EXISTS repositories(id TEXT PRIMARY KEY,account_id TEXT, name TEXT NOT NULL,visibility TEXT NOT NULL DEFAULT 'public',created_at TEXT NOT NULL,UNIQUE(account_id,name),FOREIGN KEY(account_id) REFERENCES accounts(id)); CREATE TABLE IF NOT EXISTS publish_jobs(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),state TEXT NOT NULL,incoming_path TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT); CREATE TABLE IF NOT EXISTS reader_credentials(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),name TEXT NOT NULL,token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,revoked_at TEXT,UNIQUE(repository_id,name)); CREATE INDEX IF NOT EXISTS publish_jobs_queue ON publish_jobs(state,created_at); CREATE TABLE IF NOT EXISTS repository_egress_usage(repository_id TEXT NOT NULL REFERENCES repositories(id),period TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,period)); CREATE INDEX IF NOT EXISTS repository_egress_period ON repository_egress_usage(period); CREATE TABLE IF NOT EXISTS account_storage_high_water(account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,high_water_bytes INTEGER NOT NULL CHECK(high_water_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(account_id,period)); CREATE TABLE IF NOT EXISTS mock_billing_events(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(account_id,period,event_type)); CREATE TABLE IF NOT EXISTS billing_provider_accounts(account_id TEXT NOT NULL REFERENCES accounts(id),provider TEXT NOT NULL,environment TEXT NOT NULL,provider_customer_id TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(account_id,provider,environment)); CREATE TABLE IF NOT EXISTS billing_provider_submissions(account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,period,provider,environment)); CREATE TABLE IF NOT EXISTS account_entitlements(account_id TEXT PRIMARY KEY REFERENCES accounts(id),plan_code TEXT NOT NULL,started_at TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,source TEXT NOT NULL DEFAULT 'legacy',purchase_id TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS billing_purchases(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),plan_code TEXT NOT NULL,rate_card_id TEXT NOT NULL,rate_card_mode TEXT NOT NULL,currency TEXT NOT NULL,amount_minor INTEGER NOT NULL CHECK(amount_minor >= 0),billing_environment TEXT NOT NULL,provider TEXT NOT NULL,status TEXT NOT NULL,entitlement_period_start TEXT NOT NULL,entitlement_period_end TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',failed_at TEXT NOT NULL DEFAULT '',expired_at TEXT NOT NULL DEFAULT ''); CREATE UNIQUE INDEX IF NOT EXISTS billing_purchases_pending ON billing_purchases(account_id,provider,billing_environment) WHERE status='pending'; CREATE TABLE IF NOT EXISTS billing_provider_events(provider TEXT NOT NULL,environment TEXT NOT NULL,provider_event_id TEXT NOT NULL,event_type TEXT NOT NULL,received_at TEXT NOT NULL,processed_at TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',PRIMARY KEY(provider,environment,provider_event_id)); CREATE TABLE IF NOT EXISTS repository_egress_daily(repository_id TEXT NOT NULL REFERENCES repositories(id),usage_date TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,usage_date)); CREATE INDEX IF NOT EXISTS repository_egress_daily_date ON repository_egress_daily(usage_date);`)
	if e != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", e)
	}
	for _, stmt := range []string{"ALTER TABLE account_entitlements ADD COLUMN source TEXT NOT NULL DEFAULT 'legacy'", "ALTER TABLE account_entitlements ADD COLUMN purchase_id TEXT NOT NULL DEFAULT ''"} {
		if _, ae := db.Exec(stmt); ae != nil && !strings.Contains(strings.ToLower(ae.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate entitlements: %w", ae)
		}
	}
	if _, e = db.Exec(`INSERT OR IGNORE INTO account_entitlements(account_id,plan_code,started_at,created_at,updated_at) SELECT id,'nano',created_at,created_at,created_at FROM accounts`); e != nil {
		db.Close()
		return nil, fmt.Errorf("initialize entitlements: %w", e)
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
func (s *Store) AddRepositoryEgressDaily(c context.Context, repositoryID, usageDate string, bytes int64, updatedAt string) error {
	if bytes <= 0 {
		return nil
	}
	_, e := s.db.ExecContext(c, `INSERT INTO repository_egress_daily(repository_id,usage_date,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,usage_date) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, usageDate, bytes, updatedAt)
	return e
}
func (s *Store) AccountEgressWindow(c context.Context, accountID string, start, end time.Time) (int64, error) {
	var total int64
	e := s.db.QueryRowContext(c, `SELECT COALESCE(SUM(u.egress_bytes),0) FROM repository_egress_daily u JOIN repositories r ON r.id=u.repository_id WHERE r.account_id=? AND u.usage_date>=? AND u.usage_date<?`, accountID, start.UTC().Format("2006-01-02"), end.UTC().Format("2006-01-02")).Scan(&total)
	return total, e
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
func (s *Store) MockBillingEventPayload(c context.Context, accountID, period, eventType string) (string, error) {
	var payload string
	e := s.db.QueryRowContext(c, `SELECT payload FROM mock_billing_events WHERE account_id=? AND period=? AND event_type=?`, accountID, period, eventType).Scan(&payload)
	return payload, e
}

type BillingProviderAccount struct{ AccountID, Provider, Environment, ProviderCustomerID, CreatedAt string }
type BillingProviderSubmission struct{ AccountID, Period, Provider, Environment, FinalizationID, Status, ProviderReference, AttemptedAt, CompletedAt, LastError string }

func (s *Store) ProviderAccount(c context.Context, accountID, provider, environment string) (BillingProviderAccount, error) {
	var a BillingProviderAccount
	e := s.db.QueryRowContext(c, `SELECT account_id,provider,environment,provider_customer_id,created_at FROM billing_provider_accounts WHERE account_id=? AND provider=? AND environment=?`, accountID, provider, environment).Scan(&a.AccountID, &a.Provider, &a.Environment, &a.ProviderCustomerID, &a.CreatedAt)
	return a, e
}
func (s *Store) SaveProviderAccount(c context.Context, a BillingProviderAccount) error {
	_, e := s.db.ExecContext(c, `INSERT INTO billing_provider_accounts(account_id,provider,environment,provider_customer_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT(account_id,provider,environment) DO UPDATE SET provider_customer_id=excluded.provider_customer_id`, a.AccountID, a.Provider, a.Environment, a.ProviderCustomerID, a.CreatedAt)
	return e
}
func (s *Store) ProviderSubmission(c context.Context, accountID, period, provider, environment string) (BillingProviderSubmission, error) {
	var x BillingProviderSubmission
	e := s.db.QueryRowContext(c, `SELECT account_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error FROM billing_provider_submissions WHERE account_id=? AND period=? AND provider=? AND environment=?`, accountID, period, provider, environment).Scan(&x.AccountID, &x.Period, &x.Provider, &x.Environment, &x.FinalizationID, &x.Status, &x.ProviderReference, &x.AttemptedAt, &x.CompletedAt, &x.LastError)
	return x, e
}
func (s *Store) SaveProviderSubmission(c context.Context, x BillingProviderSubmission) error {
	_, e := s.db.ExecContext(c, `INSERT INTO billing_provider_submissions(account_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,period,provider,environment) DO UPDATE SET finalization_id=excluded.finalization_id,status=excluded.status,provider_reference=excluded.provider_reference,attempted_at=excluded.attempted_at,completed_at=excluded.completed_at,last_error=excluded.last_error`, x.AccountID, x.Period, x.Provider, x.Environment, x.FinalizationID, x.Status, x.ProviderReference, x.AttemptedAt, x.CompletedAt, x.LastError)
	return e
}

type AccountEntitlement struct{ AccountID, PlanCode, StartedAt, CreatedAt, UpdatedAt, Source, PurchaseID string }

func (s *Store) SetEntitlement(c context.Context, x AccountEntitlement) error {
	_, e := s.db.ExecContext(c, `INSERT INTO account_entitlements(account_id,plan_code,started_at,created_at,updated_at,source,purchase_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET plan_code=excluded.plan_code,started_at=excluded.started_at,updated_at=excluded.updated_at,source=excluded.source,purchase_id=excluded.purchase_id`, x.AccountID, x.PlanCode, x.StartedAt, x.CreatedAt, x.UpdatedAt, x.Source, x.PurchaseID)
	return e
}
func (s *Store) Entitlement(c context.Context, accountID string) (AccountEntitlement, error) {
	var x AccountEntitlement
	e := s.db.QueryRowContext(c, `SELECT account_id,plan_code,started_at,created_at,updated_at,source,purchase_id FROM account_entitlements WHERE account_id=?`, accountID).Scan(&x.AccountID, &x.PlanCode, &x.StartedAt, &x.CreatedAt, &x.UpdatedAt, &x.Source, &x.PurchaseID)
	return x, e
}

type BillingPurchase struct {
	ID, AccountID, PlanCode, RateCardID, RateCardMode, Currency, BillingEnvironment, Provider, Status, EntitlementPeriodStart, EntitlementPeriodEnd, ProviderReference, CreatedAt, UpdatedAt, PaidAt, FailedAt, ExpiredAt string
	AmountMinor                                                                                                                                                                                                           int64
}
type BillingProviderEvent struct{ Provider, Environment, ProviderEventID, EventType, ReceivedAt, ProcessedAt, Status, Error string }

func scanPurchase(row interface{ Scan(...any) error }) (BillingPurchase, error) {
	var p BillingPurchase
	e := row.Scan(&p.ID, &p.AccountID, &p.PlanCode, &p.RateCardID, &p.RateCardMode, &p.Currency, &p.AmountMinor, &p.BillingEnvironment, &p.Provider, &p.Status, &p.EntitlementPeriodStart, &p.EntitlementPeriodEnd, &p.ProviderReference, &p.CreatedAt, &p.UpdatedAt, &p.PaidAt, &p.FailedAt, &p.ExpiredAt)
	return p, e
}

const purchaseColumns = `id,account_id,plan_code,rate_card_id,rate_card_mode,currency,amount_minor,billing_environment,provider,status,entitlement_period_start,entitlement_period_end,provider_reference,created_at,updated_at,paid_at,failed_at,expired_at`

func (s *Store) Purchase(c context.Context, id string) (BillingPurchase, error) {
	return scanPurchase(s.db.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE id=?`, id))
}
func (s *Store) CreatePendingPurchase(c context.Context, p BillingPurchase, now string) (BillingPurchase, error) {
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return BillingPurchase{}, e
	}
	defer tx.Rollback()
	var existing BillingPurchase
	existing, e = scanPurchase(tx.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE account_id=? AND provider=? AND billing_environment=? AND status='pending'`, p.AccountID, p.Provider, p.BillingEnvironment))
	if e == nil {
		_ = tx.Commit()
		return existing, nil
	}
	if e != sql.ErrNoRows {
		return BillingPurchase{}, e
	}
	var source, end string
	e = tx.QueryRowContext(c, `SELECT source,entitlement_period_end FROM account_entitlements WHERE account_id=?`, p.AccountID).Scan(&source, &end)
	if e == nil && source == "purchase" && end > now {
		return BillingPurchase{}, fmt.Errorf("purchase-already-active")
	}
	_, e = tx.ExecContext(c, `INSERT INTO billing_purchases(`+purchaseColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.AccountID, p.PlanCode, p.RateCardID, p.RateCardMode, p.Currency, p.AmountMinor, p.BillingEnvironment, p.Provider, p.Status, p.EntitlementPeriodStart, p.EntitlementPeriodEnd, p.ProviderReference, p.CreatedAt, p.UpdatedAt, p.PaidAt, p.FailedAt, p.ExpiredAt)
	if e != nil {
		return BillingPurchase{}, e
	}
	if e = tx.Commit(); e != nil {
		return BillingPurchase{}, e
	}
	return p, nil
}
func (s *Store) UpdatePurchaseCheckout(c context.Context, id, reference string, updatedAt string) error {
	_, e := s.db.ExecContext(c, `UPDATE billing_purchases SET provider_reference=?,updated_at=? WHERE id=? AND status='pending'`, reference, updatedAt, id)
	return e
}
func (s *Store) ApplyBillingProviderEvent(c context.Context, event BillingProviderEvent, purchaseID, providerReference string, kind string, amount int64, currency, paidAt, entitlementStart, entitlementEnd string) error {
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(c, `INSERT OR IGNORE INTO billing_provider_events(provider,environment,provider_event_id,event_type,received_at,status) VALUES(?,?,?,?,?,'processing')`, event.Provider, event.Environment, event.ProviderEventID, event.EventType, event.ReceivedAt)
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
	if p.ProviderReference != "" && p.ProviderReference != providerReference {
		return fmt.Errorf("provider-reference-mismatch")
	}
	if kind == "success" {
		if p.Status == "paid" {
			_, e = tx.ExecContext(c, `UPDATE billing_provider_events SET processed_at=?,status='processed' WHERE provider=? AND environment=? AND provider_event_id=?`, event.ReceivedAt, event.Provider, event.Environment, event.ProviderEventID)
			if e != nil {
				return e
			}
			return tx.Commit()
		}
		if p.Status != "pending" || p.AmountMinor != amount || p.Currency != currency {
			return fmt.Errorf("payment-verification-failed")
		}
		_, e = tx.ExecContext(c, `UPDATE billing_purchases SET status='paid',paid_at=?,updated_at=?,entitlement_period_start=?,entitlement_period_end=? WHERE id=? AND status='pending'`, paidAt, paidAt, entitlementStart, entitlementEnd, purchaseID)
		if e == nil {
			res, ue := tx.ExecContext(c, `UPDATE account_entitlements SET started_at=?,updated_at=?,source='purchase',purchase_id=? WHERE account_id=?`, entitlementStart, paidAt, purchaseID, p.AccountID)
			if ue != nil {
				e = ue
			} else {
				n, ne := res.RowsAffected()
				if ne != nil {
					e = ne
				} else if n != 1 {
					e = fmt.Errorf("entitlement activation unavailable")
				}
			}
		}
	} else if p.Status == "pending" {
		statusField := "failed_at"
		status := "failed"
		if kind == "expired" {
			statusField = "expired_at"
			status = "expired"
		}
		_, e = tx.ExecContext(c, `UPDATE billing_purchases SET status=?,`+statusField+`=?,updated_at=? WHERE id=? AND status='pending'`, status, event.ReceivedAt, event.ReceivedAt, purchaseID)
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
func (s *Store) RecordMockBillingEvent(c context.Context, id, accountID, period, eventType, payload, createdAt string) error {
	_, e := s.db.ExecContext(c, `INSERT INTO mock_billing_events(id,account_id,period,event_type,payload,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,period,event_type) DO NOTHING`, id, accountID, period, eventType, payload, createdAt)
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
func (s *Store) CreateAccount(c context.Context, a Account) error {
	_, e := s.db.ExecContext(c, "INSERT INTO accounts VALUES(?,?,?,?,?)", a.ID, a.Email, a.PasswordHash, a.Status, a.CreatedAt)
	if e != nil {
		return e
	}
	anchor := a.CreatedAt
	if _, err := time.Parse(time.RFC3339Nano, anchor); err != nil {
		anchor = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, e = s.db.ExecContext(c, `INSERT OR IGNORE INTO account_entitlements(account_id,plan_code,started_at,created_at,updated_at) VALUES(?,?,?,?,?)`, a.ID, "nano", anchor, a.CreatedAt, a.CreatedAt)
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
