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
	"sort"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type Repository struct{ ID, AccountID, Name, Visibility, CreatedAt string }
type Job struct{ ID, RepositoryID, State, IncomingPath, Error, CreatedAt, StartedAt, FinishedAt string }
type Account struct{ ID, Email, PasswordHash, Status, CreatedAt string }

const AccountStatusActive = "active"

type Session struct{ ID, AccountID, TokenPrefix string }
type ReaderCredential struct{ ID, RepositoryID, Name, TokenPrefix, CreatedAt string }
type Store struct{ db *sql.DB }

var ErrNotFound = sql.ErrNoRows
var migrationFault func(string) error
var migrationNow = time.Now

func migrationCheckpoint(stage string) error {
	if migrationFault != nil {
		return migrationFault(stage)
	}
	return nil
}

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
	_, e = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS schema_migrations(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT); CREATE TABLE IF NOT EXISTS repositories(id TEXT PRIMARY KEY,account_id TEXT, name TEXT NOT NULL,visibility TEXT NOT NULL DEFAULT 'public',created_at TEXT NOT NULL,UNIQUE(account_id,name),FOREIGN KEY(account_id) REFERENCES accounts(id)); CREATE TABLE IF NOT EXISTS publish_jobs(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),state TEXT NOT NULL,incoming_path TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT); CREATE TABLE IF NOT EXISTS reader_credentials(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),name TEXT NOT NULL,token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,revoked_at TEXT,UNIQUE(repository_id,name)); CREATE INDEX IF NOT EXISTS publish_jobs_queue ON publish_jobs(state,created_at); CREATE TABLE IF NOT EXISTS repository_egress_usage(repository_id TEXT NOT NULL REFERENCES repositories(id),period TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,period)); CREATE INDEX IF NOT EXISTS repository_egress_period ON repository_egress_usage(period); CREATE TABLE IF NOT EXISTS account_storage_high_water(account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,high_water_bytes INTEGER NOT NULL CHECK(high_water_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(account_id,period)); CREATE TABLE IF NOT EXISTS mock_billing_events(id TEXT NOT NULL,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(product_id,id),UNIQUE(account_id,product_id,period,event_type)); CREATE TABLE IF NOT EXISTS billing_provider_accounts(account_id TEXT NOT NULL REFERENCES accounts(id),provider TEXT NOT NULL,environment TEXT NOT NULL,provider_customer_id TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(account_id,provider,environment)); CREATE TABLE IF NOT EXISTS billing_provider_submissions(account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,product_id,period,provider,environment)); CREATE TABLE IF NOT EXISTS account_entitlements(account_id TEXT PRIMARY KEY REFERENCES accounts(id),plan_code TEXT NOT NULL,started_at TEXT NOT NULL,valid_from TEXT NOT NULL DEFAULT '',valid_until TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,source TEXT NOT NULL DEFAULT 'legacy',purchase_id TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS product_grants(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL DEFAULT '',period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),source TEXT NOT NULL,purchase_id TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(customer_id,product_id,capability_id)); CREATE TABLE IF NOT EXISTS purchase_grant_snapshots(purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),capability_id TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),PRIMARY KEY(purchase_id,capability_id)); CREATE TABLE IF NOT EXISTS purchase_grant_snapshot_sets(purchase_id TEXT PRIMARY KEY REFERENCES billing_purchases(id),expected_count INTEGER NOT NULL CHECK(expected_count>0),capability_digest TEXT NOT NULL); CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_immutable_update BEFORE UPDATE ON purchase_grant_snapshots BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are immutable'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_immutable_delete BEFORE DELETE ON purchase_grant_snapshots BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are immutable'); END; CREATE TABLE IF NOT EXISTS billing_purchases(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,plan_code TEXT NOT NULL,rate_card_id TEXT NOT NULL,rate_card_mode TEXT NOT NULL,tax_classification TEXT NOT NULL DEFAULT 'saas-personal',currency TEXT NOT NULL,amount_minor INTEGER NOT NULL CHECK(amount_minor >= 0),billing_environment TEXT NOT NULL,provider TEXT NOT NULL,status TEXT NOT NULL,entitlement_period_start TEXT NOT NULL,entitlement_period_end TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',checkout_url TEXT NOT NULL DEFAULT '',checkout_request_version TEXT NOT NULL DEFAULT 'v1',snapshot_finalized INTEGER NOT NULL DEFAULT 0 CHECK(snapshot_finalized IN (0,1)),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',failed_at TEXT NOT NULL DEFAULT '',expired_at TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS billing_provider_events(provider TEXT NOT NULL,environment TEXT NOT NULL,provider_event_id TEXT NOT NULL,event_type TEXT NOT NULL,received_at TEXT NOT NULL,processed_at TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',PRIMARY KEY(provider,environment,provider_event_id)); CREATE TABLE IF NOT EXISTS repository_egress_daily(repository_id TEXT NOT NULL REFERENCES repositories(id),usage_date TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,usage_date)); CREATE TABLE IF NOT EXISTS purchase_grant_instances(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(purchase_id,capability_id)); CREATE INDEX IF NOT EXISTS purchase_grant_instances_lookup ON purchase_grant_instances(customer_id,product_id,capability_id); CREATE INDEX IF NOT EXISTS repository_egress_daily_date ON repository_egress_daily(usage_date);`)
	if e != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", e)
	}
	if _, e = db.Exec(`CREATE TABLE IF NOT EXISTS repository_publications(repository_id TEXT PRIMARY KEY REFERENCES repositories(id),active_generation TEXT NOT NULL,updated_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS egress_accounting_uncertain(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),created_at TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '')`); e != nil {
		db.Close()
		return nil, fmt.Errorf("initialize publication state: %w", e)
	}
	for _, stmt := range []string{"ALTER TABLE account_entitlements ADD COLUMN source TEXT NOT NULL DEFAULT 'legacy'", "ALTER TABLE account_entitlements ADD COLUMN purchase_id TEXT NOT NULL DEFAULT ''", "ALTER TABLE billing_purchases ADD COLUMN tax_classification TEXT NOT NULL DEFAULT 'saas-personal'", "ALTER TABLE billing_purchases ADD COLUMN checkout_request_version TEXT NOT NULL DEFAULT 'v1'", "ALTER TABLE billing_purchases ADD COLUMN snapshot_finalized INTEGER NOT NULL DEFAULT 0", "ALTER TABLE billing_purchases ADD COLUMN checkout_url TEXT NOT NULL DEFAULT ''", "ALTER TABLE account_entitlements ADD COLUMN valid_from TEXT NOT NULL DEFAULT ''", "ALTER TABLE account_entitlements ADD COLUMN valid_until TEXT NOT NULL DEFAULT ''", "ALTER TABLE billing_purchases ADD COLUMN product_id TEXT NOT NULL DEFAULT 'tpa-hub'", "ALTER TABLE billing_provider_submissions ADD COLUMN product_id TEXT NOT NULL DEFAULT 'tpa-hub'", "ALTER TABLE mock_billing_events ADD COLUMN product_id TEXT NOT NULL DEFAULT 'tpa-hub'"} {
		if _, ae := db.Exec(stmt); ae != nil && !strings.Contains(strings.ToLower(ae.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate entitlements: %w", ae)
		}
	}
	if e = migrateProductScopedKeys(db); e != nil {
		db.Close()
		return nil, e
	}
	if _, e = db.Exec(`DROP INDEX IF EXISTS billing_purchases_pending; CREATE UNIQUE INDEX billing_purchases_pending ON billing_purchases(account_id,product_id,provider,billing_environment) WHERE status='pending'; UPDATE account_entitlements SET valid_from=started_at WHERE valid_from='';
UPDATE account_entitlements SET valid_from=COALESCE((SELECT entitlement_period_start FROM billing_purchases WHERE id=account_entitlements.purchase_id AND status='paid'),valid_from),valid_until=COALESCE((SELECT entitlement_period_end FROM billing_purchases WHERE id=account_entitlements.purchase_id AND status='paid'),'') WHERE source='purchase';`); e != nil {
		db.Close()
		return nil, fmt.Errorf("migrate entitlement validity: %w", e)
	}
	if _, e = db.Exec(`CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_no_late_insert BEFORE INSERT ON purchase_grant_snapshots WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=NEW.purchase_id AND snapshot_finalized=1) BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are finalized'); END; DROP TRIGGER IF EXISTS billing_purchase_terms_immutable; CREATE TRIGGER billing_purchase_terms_immutable AFTER UPDATE ON billing_purchases WHEN OLD.snapshot_finalized=1 AND (NEW.snapshot_finalized<>OLD.snapshot_finalized OR NEW.plan_code<>OLD.plan_code OR NEW.product_id<>OLD.product_id OR NEW.amount_minor<>OLD.amount_minor OR NEW.currency<>OLD.currency OR NEW.rate_card_id<>OLD.rate_card_id OR NEW.rate_card_mode<>OLD.rate_card_mode OR NEW.tax_classification<>OLD.tax_classification OR NEW.entitlement_period_start<>OLD.entitlement_period_start OR NEW.entitlement_period_end<>OLD.entitlement_period_end OR NEW.provider<>OLD.provider OR NEW.provider_reference<>OLD.provider_reference OR NEW.billing_environment<>OLD.billing_environment) BEGIN SELECT RAISE(ABORT,'billing purchase terms are immutable'); END; CREATE TABLE IF NOT EXISTS purchase_grant_snapshot_sets(purchase_id TEXT PRIMARY KEY REFERENCES billing_purchases(id),expected_count INTEGER NOT NULL CHECK(expected_count>0),capability_digest TEXT NOT NULL); CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshot_sets_immutable_update BEFORE UPDATE ON purchase_grant_snapshot_sets BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are immutable'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshot_sets_immutable_delete BEFORE DELETE ON purchase_grant_snapshot_sets BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are immutable'); END; CREATE TABLE IF NOT EXISTS purchase_grant_instances(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(purchase_id,capability_id)); CREATE INDEX IF NOT EXISTS purchase_grant_instances_lookup ON purchase_grant_instances(customer_id,product_id,capability_id); CREATE TRIGGER IF NOT EXISTS migration_grants_commercial_terms_immutable BEFORE UPDATE ON product_grants WHEN OLD.source='migration' AND (NEW.source<>OLD.source OR NEW.offer_id<>OLD.offer_id OR NEW.valid_from<>OLD.valid_from OR NEW.valid_until<>OLD.valid_until OR NEW.period_cadence<>OLD.period_cadence OR NEW.storage_limit_bytes<>OLD.storage_limit_bytes OR NEW.egress_limit_bytes<>OLD.egress_limit_bytes OR NEW.operation_metric<>OLD.operation_metric OR NEW.operation_limit<>OLD.operation_limit OR NEW.purchase_id<>OLD.purchase_id) BEGIN SELECT RAISE(ABORT,'migration grant terms are immutable'); END`); e != nil {
		db.Close()
		return nil, fmt.Errorf("initialize purchase grant instances: %w", e)
	}
	if e = migrateLegacyTPAGrants(db); e != nil {
		db.Close()
		return nil, e
	}
	if e = migrateLegacyAccessToSixMonthNano(db); e != nil {
		db.Close()
		return nil, e
	}
	if e = rebuildCompatibilityProjection(db); e != nil {
		db.Close()
		return nil, fmt.Errorf("rebuild compatibility projection: %w", e)
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
func migrateLegacyTPAGrants(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT DISTINCT plan_code FROM account_entitlements WHERE source='legacy'`)
	if err != nil {
		return fmt.Errorf("inspect legacy plans: %w", err)
	}
	for rows.Next() {
		var plan string
		if err = rows.Scan(&plan); err != nil {
			rows.Close()
			return fmt.Errorf("read legacy plan: %w", err)
		}
		if plan != "nano" && plan != "standard" && plan != "pro" {
			rows.Close()
			return fmt.Errorf("unknown legacy plan: %s", plan)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect legacy plans: %w", err)
	}
	rows.Close()
	sources, err := tx.Query(`SELECT DISTINCT source FROM account_entitlements`)
	if err != nil {
		return fmt.Errorf("inspect entitlement sources: %w", err)
	}
	for sources.Next() {
		var source string
		if err = sources.Scan(&source); err != nil {
			sources.Close()
			return fmt.Errorf("read entitlement source: %w", err)
		}
		if source != "legacy" && source != "migration" && source != "local" && source != "purchase" {
			sources.Close()
			return fmt.Errorf("unknown entitlement source: %s", source)
		}
	}
	if err = sources.Err(); err != nil {
		sources.Close()
		return fmt.Errorf("inspect entitlement sources: %w", err)
	}
	sources.Close()
	purchaseRows, err := tx.Query(`SELECT e.account_id,e.purchase_id,COALESCE(p.account_id,''),COALESCE(p.product_id,''),COALESCE(p.status,''),e.valid_from,e.valid_until FROM account_entitlements e LEFT JOIN billing_purchases p ON p.id=e.purchase_id WHERE e.source='purchase'`)
	if err != nil {
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	for purchaseRows.Next() {
		var accountID, purchaseID, purchaseAccount, productID, status, validFrom, validUntil string
		if err = purchaseRows.Scan(&accountID, &purchaseID, &purchaseAccount, &productID, &status, &validFrom, &validUntil); err != nil {
			purchaseRows.Close()
			return fmt.Errorf("read purchase-backed grant: %w", err)
		}
		from, fromErr := time.Parse(time.RFC3339Nano, validFrom)
		until, untilErr := time.Parse(time.RFC3339Nano, validUntil)
		if accountID == "" || purchaseID == "" || purchaseAccount != accountID || productID != string(commerce.ProductTPAHub) || status != "paid" || validFrom == "" || validUntil == "" || fromErr != nil || untilErr != nil || !until.After(from) {
			purchaseRows.Close()
			return fmt.Errorf("invalid purchase-backed entitlement: account=%s purchase=%s status=%s", accountID, purchaseID, status)
		}
	}
	if err = purchaseRows.Err(); err != nil {
		purchaseRows.Close()
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	purchaseRows.Close()
	purchaseRows, err = tx.Query(`SELECT g.customer_id,g.purchase_id,COALESCE(p.account_id,''),COALESCE(p.product_id,''),COALESCE(p.status,''),g.valid_from,g.valid_until FROM product_grants g LEFT JOIN billing_purchases p ON p.id=g.purchase_id WHERE g.source='purchase'`)
	if err != nil {
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	for purchaseRows.Next() {
		var accountID, purchaseID, purchaseAccount, productID, status, validFrom, validUntil string
		if err = purchaseRows.Scan(&accountID, &purchaseID, &purchaseAccount, &productID, &status, &validFrom, &validUntil); err != nil {
			purchaseRows.Close()
			return fmt.Errorf("read purchase-backed grant: %w", err)
		}
		from, fromErr := time.Parse(time.RFC3339Nano, validFrom)
		until, untilErr := time.Parse(time.RFC3339Nano, validUntil)
		if accountID == "" || purchaseID == "" || purchaseAccount != accountID || productID != string(commerce.ProductTPAHub) || status != "paid" || validFrom == "" || validUntil == "" || fromErr != nil || untilErr != nil || !until.After(from) {
			purchaseRows.Close()
			return fmt.Errorf("invalid purchase-backed grant: account=%s purchase=%s status=%s", accountID, purchaseID, status)
		}
	}
	if err = purchaseRows.Err(); err != nil {
		purchaseRows.Close()
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	purchaseRows.Close()
	if err = migrationCheckpoint("after-source-validation"); err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='commerce-product-grants-v1'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return tx.Commit()
	}
	if err = migrationCheckpoint("before-materialization"); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO account_entitlements(account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at) SELECT id,'nano',created_at,created_at,'',created_at,created_at FROM accounts;
INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.create',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,0,source,purchase_id,created_at,updated_at FROM account_entitlements;
INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.publish',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,CASE plan_code WHEN 'nano' THEN 5000000 WHEN 'standard' THEN 500000000 ELSE 9223372036854775807 END,0,source,purchase_id,created_at,updated_at FROM account_entitlements;
INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.download',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,CASE plan_code WHEN 'nano' THEN 12000000000 WHEN 'standard' THEN 20000000000 ELSE 9223372036854775807 END,source,purchase_id,created_at,updated_at FROM account_entitlements;
INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.read.private',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,0,source,purchase_id,created_at,updated_at FROM account_entitlements;
INSERT OR IGNORE INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit) SELECT p.id,g.capability_id,g.period_cadence,g.storage_limit_bytes,g.egress_limit_bytes,g.operation_metric,g.operation_limit FROM billing_purchases p JOIN product_grants g ON g.purchase_id=p.id WHERE p.status='paid' AND g.source='purchase';`); err != nil {
		return fmt.Errorf("migrate legacy TPA grants: %w", err)
	}
	if err = migrationCheckpoint("after-materialization-before-marker"); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES('commerce-product-grants-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		return fmt.Errorf("record legacy TPA migration: %w", err)
	}
	if err = migrationCheckpoint("before-commit"); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateLegacyAccessToSixMonthNano(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='legacy-six-month-nano-v1'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return tx.Commit()
	}
	anchor := migrationNow().UTC()
	from, until := anchor.Format(time.RFC3339Nano), anchor.AddDate(0, 6, 0).Format(time.RFC3339Nano)
	rows, err := tx.Query(`SELECT account_id FROM account_entitlements WHERE source='legacy' AND valid_from<=? AND (valid_until='' OR valid_until>?) UNION SELECT customer_id FROM product_grants WHERE source='legacy' AND product_id=? AND enabled=1 AND valid_from<=? AND (valid_until='' OR valid_until>?)`, from, from, string(commerce.ProductTPAHub), from, from)
	if err != nil {
		return err
	}
	var accounts []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		accounts = append(accounts, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err = migrationCheckpoint("bounded-after-eligibility"); err != nil {
		return err
	}
	offer := tpahubproduct.NanoOffer()
	for _, accountID := range accounts {
		if _, err = tx.Exec(`DELETE FROM product_grants WHERE customer_id=? AND product_id=? AND source='legacy'`, accountID, string(offer.ProductID)); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE account_entitlements SET plan_code='nano',started_at=?,valid_from=?,valid_until=?,source='migration',purchase_id='',updated_at=? WHERE account_id=? AND source='legacy'`, from, from, until, from, accountID); err != nil {
			return err
		}
		for _, template := range offer.Grants {
			if _, err = tx.Exec(`INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?,?,?,?,'migration','',?,?) ON CONFLICT(customer_id,product_id,capability_id) DO UPDATE SET offer_id=excluded.offer_id,enabled=1,valid_from=excluded.valid_from,valid_until=excluded.valid_until,period_cadence=excluded.period_cadence,storage_limit_bytes=excluded.storage_limit_bytes,egress_limit_bytes=excluded.egress_limit_bytes,operation_metric=excluded.operation_metric,operation_limit=excluded.operation_limit,source='migration',purchase_id='',updated_at=excluded.updated_at`, accountID, offer.ProductID, template.CapabilityID, offer.ID, from, until, template.Period, template.Limits.StorageBytes, template.Limits.EgressBytes, template.Limits.OperationMetric, template.Limits.OperationCount, from, from); err != nil {
				return err
			}
		}
		if err = migrationCheckpoint("bounded-after-account"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES('legacy-six-month-nano-v1',?)`, from); err != nil {
		return err
	}
	if err = migrationCheckpoint("bounded-before-commit"); err != nil {
		return err
	}
	return tx.Commit()
}

func rebuildCompatibilityProjection(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT p.account_id,p.plan_code,p.entitlement_period_start,p.entitlement_period_end,p.paid_at,p.id FROM billing_purchases p WHERE p.product_id=? AND p.status='paid' AND p.paid_at=(SELECT MAX(p2.paid_at) FROM billing_purchases p2 WHERE p2.account_id=p.account_id AND p2.product_id=? AND p2.status='paid')`, string(commerce.ProductTPAHub), string(commerce.ProductTPAHub))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, plan, from, until, paidAt, purchaseID string
		if err = rows.Scan(&accountID, &plan, &from, &until, &paidAt, &purchaseID); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at,source,purchase_id) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET plan_code=excluded.plan_code,started_at=excluded.started_at,valid_from=excluded.valid_from,valid_until=excluded.valid_until,updated_at=excluded.updated_at,source='purchase',purchase_id=excluded.purchase_id`, accountID, plan, from, from, until, paidAt, paidAt, "purchase", purchaseID); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateProductScopedKeys(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var submissionsSQL, eventsSQL string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='billing_provider_submissions'`).Scan(&submissionsSQL); err != nil {
		return err
	}
	if !strings.Contains(strings.ReplaceAll(submissionsSQL, " ", ""), "PRIMARYKEY(account_id,product_id,period,provider,environment)") {
		if _, err = tx.Exec(`CREATE TABLE billing_provider_submissions_new(account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,product_id,period,provider,environment)); INSERT INTO billing_provider_submissions_new SELECT account_id,product_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error FROM billing_provider_submissions; DROP TABLE billing_provider_submissions; ALTER TABLE billing_provider_submissions_new RENAME TO billing_provider_submissions;`); err != nil {
			return fmt.Errorf("migrate provider submissions: %w", err)
		}
	}
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='mock_billing_events'`).Scan(&eventsSQL); err != nil {
		return err
	}
	normalizedEventsSQL := strings.ReplaceAll(eventsSQL, " ", "")
	if !strings.Contains(normalizedEventsSQL, "PRIMARYKEY(product_id,id)") || !strings.Contains(normalizedEventsSQL, "UNIQUE(account_id,product_id,period,event_type)") {
		if _, err = tx.Exec(`CREATE TABLE mock_billing_events_new(id TEXT NOT NULL,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(product_id,id),UNIQUE(account_id,product_id,period,event_type)); INSERT INTO mock_billing_events_new SELECT id,account_id,product_id,period,event_type,payload,created_at FROM mock_billing_events; DROP TABLE mock_billing_events; ALTER TABLE mock_billing_events_new RENAME TO mock_billing_events;`); err != nil {
			return fmt.Errorf("migrate mock billing events: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) AddRepositoryEgress(c context.Context, repositoryID, period string, bytes int64, updatedAt string) error {
	if bytes <= 0 {
		return nil
	}
	_, e := s.db.ExecContext(c, `INSERT INTO repository_egress_usage(repository_id,period,egress_bytes,updated_at) VALUES(?,?,?,?) ON CONFLICT(repository_id,period) DO UPDATE SET egress_bytes=egress_bytes+excluded.egress_bytes,updated_at=excluded.updated_at`, repositoryID, period, bytes, updatedAt)
	return e
}
func (s *Store) BeginEgressDelivery(c context.Context, id, repositoryID, createdAt string) error {
	_, err := s.db.ExecContext(c, `INSERT INTO egress_accounting_uncertain(id,repository_id,created_at) VALUES(?,?,?)`, id, repositoryID, createdAt)
	return err
}
func (s *Store) CompleteEgressDelivery(c context.Context, id, repositoryID, usageDate string, bytes int64, updatedAt string) error {
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
func (s *Store) ClearAccountEgressUncertainty(c context.Context, accountID string) error {
	_, err := s.db.ExecContext(c, `DELETE FROM egress_accounting_uncertain WHERE repository_id IN (SELECT id FROM repositories WHERE account_id=?)`, accountID)
	return err
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
	return s.ProductMockBillingEventPayload(c, accountID, string(commerce.ProductTPAHub), period, eventType)
}
func (s *Store) ProductMockBillingEventPayload(c context.Context, accountID, productID, period, eventType string) (string, error) {
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
	if x.ProductID == "" {
		x.ProductID = string(commerce.ProductTPAHub)
	}
	_, e := s.db.ExecContext(c, `INSERT INTO billing_provider_submissions(account_id,product_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,product_id,period,provider,environment) DO UPDATE SET finalization_id=excluded.finalization_id,status=excluded.status,provider_reference=excluded.provider_reference,attempted_at=excluded.attempted_at,completed_at=excluded.completed_at,last_error=excluded.last_error`, x.AccountID, x.ProductID, x.Period, x.Provider, x.Environment, x.FinalizationID, x.Status, x.ProviderReference, x.AttemptedAt, x.CompletedAt, x.LastError)
	return e
}

type AccountEntitlement struct{ AccountID, PlanCode, StartedAt, ValidFrom, ValidUntil, CreatedAt, UpdatedAt, Source, PurchaseID string }

func parseEntitlementTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
	}
	return parsed, err
}
func (x AccountEntitlement) ActiveAt(now time.Time) (bool, error) {
	from, e := parseEntitlementTime(x.ValidFrom)
	if e != nil {
		return false, fmt.Errorf("invalid entitlement valid_from: %w", e)
	}
	if now.UTC().Before(from.UTC()) {
		return false, nil
	}
	if x.ValidUntil == "" {
		return x.Source == "legacy" || x.Source == "local", nil
	}
	until, e := parseEntitlementTime(x.ValidUntil)
	if e != nil {
		return false, fmt.Errorf("invalid entitlement valid_until: %w", e)
	}
	return now.UTC().Before(until.UTC()), nil
}
func scanEntitlement(row interface{ Scan(...any) error }) (AccountEntitlement, error) {
	var x AccountEntitlement
	e := row.Scan(&x.AccountID, &x.PlanCode, &x.StartedAt, &x.ValidFrom, &x.ValidUntil, &x.CreatedAt, &x.UpdatedAt, &x.Source, &x.PurchaseID)
	return x, e
}
func (s *Store) SetEntitlement(c context.Context, x AccountEntitlement) error {
	if x.ValidFrom == "" {
		x.ValidFrom = x.StartedAt
	}
	if x.Source == "" {
		x.Source = "local"
	}
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(c, `INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at,source,purchase_id) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET plan_code=excluded.plan_code,started_at=excluded.started_at,valid_from=excluded.valid_from,valid_until=excluded.valid_until,updated_at=excluded.updated_at,source=excluded.source,purchase_id=excluded.purchase_id`, x.AccountID, x.PlanCode, x.StartedAt, x.ValidFrom, x.ValidUntil, x.CreatedAt, x.UpdatedAt, x.Source, x.PurchaseID); e != nil {
		return e
	}
	if x.Source != "purchase" {
		if _, e = tx.ExecContext(c, `UPDATE product_grants SET offer_id=?,enabled=1,valid_from=?,valid_until=?,period_cadence=CASE WHEN ?='nano' THEN 'annual' ELSE 'monthly' END,storage_limit_bytes=CASE WHEN capability_id='repository.publish' THEN CASE ? WHEN 'nano' THEN 5000000 WHEN 'standard' THEN 500000000 ELSE 9223372036854775807 END ELSE storage_limit_bytes END,egress_limit_bytes=CASE WHEN capability_id='repository.download' THEN CASE ? WHEN 'nano' THEN 12000000000 WHEN 'standard' THEN 20000000000 ELSE 9223372036854775807 END ELSE egress_limit_bytes END,source=?,purchase_id=?,updated_at=? WHERE customer_id=? AND product_id='tpa-hub' AND source IN ('legacy','local')`, x.PlanCode, x.ValidFrom, x.ValidUntil, x.PlanCode, x.PlanCode, x.PlanCode, x.Source, x.PurchaseID, x.UpdatedAt, x.AccountID); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) Entitlement(c context.Context, accountID string) (AccountEntitlement, error) {
	return scanEntitlement(s.db.QueryRowContext(c, `SELECT account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at,source,purchase_id FROM account_entitlements WHERE account_id=?`, accountID))
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
func (s *Store) GetGrant(c context.Context, customer commerce.CustomerID, product commerce.ProductID, capability commerce.CapabilityID, at time.Time) (commerce.Grant, error) {
	const columns = `customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at`
	var contributors []commerce.Grant
	seen, enabledSeen := false, false
	base, err := scanGrant(s.db.QueryRowContext(c, `SELECT `+columns+` FROM product_grants WHERE customer_id=? AND product_id=? AND capability_id=?`, customer, product, capability))
	if err == nil {
		// Permanent historical legacy rows are audit-only after the bounded
		// migration. They never participate in runtime authorization.
		if base.Source == "legacy" && base.ProductID == commerce.ProductTPAHub {
			base.Enabled = false
		}
		seen = true
		if base.Enabled {
			enabledSeen = true
			if base.ActiveAt(at) {
				contributors = append(contributors, base)
			}
		}
	} else if err != sql.ErrNoRows {
		return commerce.Grant{}, err
	}
	rows, err := s.db.QueryContext(c, `SELECT customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,'purchase',purchase_id,created_at,updated_at FROM purchase_grant_instances WHERE customer_id=? AND product_id=? AND capability_id=? ORDER BY valid_from DESC,purchase_id DESC`, customer, product, capability)
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
	validUntil := ""
	if grant.ValidUntil != nil {
		validUntil = grant.ValidUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(c, `INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(customer_id,product_id,capability_id) DO UPDATE SET offer_id=excluded.offer_id,enabled=excluded.enabled,valid_from=excluded.valid_from,valid_until=excluded.valid_until,period_cadence=excluded.period_cadence,storage_limit_bytes=excluded.storage_limit_bytes,egress_limit_bytes=excluded.egress_limit_bytes,operation_metric=excluded.operation_metric,operation_limit=excluded.operation_limit,source=excluded.source,purchase_id=excluded.purchase_id,updated_at=excluded.updated_at`, grant.CustomerID, grant.ProductID, grant.CapabilityID, grant.OfferID, grant.Enabled, grant.ValidFrom.UTC().Format(time.RFC3339Nano), validUntil, grant.Period, grant.Limits.StorageBytes, grant.Limits.EgressBytes, grant.Limits.OperationMetric, grant.Limits.OperationCount, grant.Source, grant.PurchaseID, grant.CreatedAt.UTC().Format(time.RFC3339Nano), grant.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

type BillingPurchase struct {
	ID, AccountID, ProductID, PlanCode, RateCardID, RateCardMode, TaxClassification, Currency, BillingEnvironment, Provider, Status, EntitlementPeriodStart, EntitlementPeriodEnd, ProviderReference, CheckoutURL, CheckoutRequestVersion, CreatedAt, UpdatedAt, PaidAt, FailedAt, ExpiredAt string
	AmountMinor                                                                                                                                                                                                                                                                              int64
}
type billingProviderEvent struct{ Provider, Environment, ProviderEventID, EventType, ReceivedAt, ProcessedAt, Status, Error string }

func scanPurchase(row interface{ Scan(...any) error }) (BillingPurchase, error) {
	var p BillingPurchase
	e := row.Scan(&p.ID, &p.AccountID, &p.ProductID, &p.PlanCode, &p.RateCardID, &p.RateCardMode, &p.TaxClassification, &p.Currency, &p.AmountMinor, &p.BillingEnvironment, &p.Provider, &p.Status, &p.EntitlementPeriodStart, &p.EntitlementPeriodEnd, &p.ProviderReference, &p.CheckoutURL, &p.CheckoutRequestVersion, &p.CreatedAt, &p.UpdatedAt, &p.PaidAt, &p.FailedAt, &p.ExpiredAt)
	return p, e
}

const purchaseColumns = `id,account_id,product_id,plan_code,rate_card_id,rate_card_mode,tax_classification,currency,amount_minor,billing_environment,provider,status,entitlement_period_start,entitlement_period_end,provider_reference,checkout_url,checkout_request_version,created_at,updated_at,paid_at,failed_at,expired_at`

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
func snapshotDigest(templates []commerce.GrantTemplate) string {
	parts := make([]string, 0, len(templates))
	for _, x := range templates {
		parts = append(parts, fmt.Sprintf("%s|%s|%d|%d|%s|%d", x.CapabilityID, x.Period, x.Limits.StorageBytes, x.Limits.EgressBytes, x.Limits.OperationMetric, x.Limits.OperationCount))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
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
	if p.ProductID == "" {
		p.ProductID = string(commerce.ProductTPAHub)
	}
	if p.CheckoutRequestVersion == "" {
		p.CheckoutRequestVersion = "v2"
	}
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return BillingPurchase{}, e
	}
	defer tx.Rollback()
	var existing BillingPurchase
	existing, e = scanPurchase(tx.QueryRowContext(c, `SELECT `+purchaseColumns+` FROM billing_purchases WHERE account_id=? AND product_id=? AND provider=? AND billing_environment=? AND status='pending'`, p.AccountID, p.ProductID, p.Provider, p.BillingEnvironment))
	if e == nil {
		compatible := existing.AccountID == p.AccountID && existing.ProductID == p.ProductID && existing.PlanCode == p.PlanCode && existing.RateCardID == p.RateCardID && existing.RateCardMode == p.RateCardMode && existing.TaxClassification == p.TaxClassification && existing.Currency == p.Currency && existing.AmountMinor == p.AmountMinor && existing.Provider == p.Provider && existing.BillingEnvironment == p.BillingEnvironment && existing.CheckoutRequestVersion == p.CheckoutRequestVersion && (existing.ProviderReference == "" || existing.CheckoutURL != "")
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
	at, parseErr := time.Parse(time.RFC3339Nano, now)
	if parseErr != nil {
		return BillingPurchase{}, parseErr
	}
	var entitlementSource, entitlementFrom, entitlementUntil string
	if entitlementErr := tx.QueryRowContext(c, `SELECT source,valid_from,valid_until FROM account_entitlements WHERE account_id=?`, p.AccountID).Scan(&entitlementSource, &entitlementFrom, &entitlementUntil); entitlementErr != nil && entitlementErr != sql.ErrNoRows {
		return BillingPurchase{}, entitlementErr
	}
	if entitlementSource == "purchase" && entitlementUntil != "" {
		from, fromErr := parseEntitlementTime(entitlementFrom)
		until, untilErr := parseEntitlementTime(entitlementUntil)
		if fromErr != nil || untilErr != nil {
			return BillingPurchase{}, fmt.Errorf("invalid purchase entitlement validity")
		}
		if !at.Before(from) && at.Before(until) {
			return BillingPurchase{}, fmt.Errorf("purchase-already-active")
		}
	}
	grantRows, grantErr := tx.QueryContext(c, `SELECT enabled,valid_from,valid_until FROM product_grants WHERE customer_id=? AND product_id=? AND source='purchase'`, p.AccountID, p.ProductID)
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
	_, e = tx.ExecContext(c, `INSERT INTO billing_purchases(`+purchaseColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.AccountID, p.ProductID, p.PlanCode, p.RateCardID, p.RateCardMode, p.TaxClassification, p.Currency, p.AmountMinor, p.BillingEnvironment, p.Provider, p.Status, p.EntitlementPeriodStart, p.EntitlementPeriodEnd, p.ProviderReference, p.CheckoutURL, p.CheckoutRequestVersion, p.CreatedAt, p.UpdatedAt, p.PaidAt, p.FailedAt, p.ExpiredAt)
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
	_, e := s.db.ExecContext(c, `UPDATE billing_purchases SET status='failed',failed_at=?,updated_at=? WHERE id=? AND status='pending'`, failedAt, failedAt, id)
	return e
}

func (s *Store) UpdatePurchaseCheckout(c context.Context, id, reference, checkoutURL string, updatedAt string) error {
	if strings.TrimSpace(reference) == "" {
		return fmt.Errorf("purchase provider reference is empty")
	}
	purchase, err := s.Purchase(c, id)
	if err != nil {
		return err
	}
	from, fromErr := parseEntitlementTime(purchase.EntitlementPeriodStart)
	until, untilErr := parseEntitlementTime(purchase.EntitlementPeriodEnd)
	if fromErr != nil || untilErr != nil || !until.After(from) {
		return fmt.Errorf("purchase validity is invalid")
	}
	res, e := s.db.ExecContext(c, `UPDATE billing_purchases SET provider_reference=?,checkout_url=?,snapshot_finalized=1,updated_at=? WHERE id=? AND status='pending' AND snapshot_finalized=0`, reference, checkoutURL, updatedAt, id)
	if e != nil {
		return e
	}
	if n, e := res.RowsAffected(); e != nil {
		return e
	} else if n != 1 {
		return fmt.Errorf("purchase checkout state changed")
	}
	return nil
}
func (s *Store) applyBillingProviderEvent(c context.Context, event billingProviderEvent, purchaseID, providerReference string, kind string, amount int64, currency, paidAt, entitlementStart, entitlementEnd string) error {
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
		payableTerminal := (p.Status == "failed" || p.Status == "expired") && p.ProviderReference != ""
		if (p.Status != "pending" && p.Status != "superseded" && !payableTerminal) || p.AmountMinor != amount || p.Currency != currency {
			return fmt.Errorf("payment-verification-failed")
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
		_, e = tx.ExecContext(c, `UPDATE billing_purchases SET status='paid',paid_at=?,updated_at=?,entitlement_period_start=?,entitlement_period_end=? WHERE id=? AND status IN ('pending','superseded','failed','expired')`, paidAt, paidAt, entitlementStart, entitlementEnd, purchaseID)
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
					_, updateErr := tx.ExecContext(c, `INSERT INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(purchase_id,capability_id) DO NOTHING`, p.AccountID, p.ProductID, capability, purchaseID, p.PlanCode, true, entitlementStart, entitlementEnd, period, limits.StorageBytes, limits.EgressBytes, limits.OperationMetric, limits.OperationCount, paidAt, paidAt)
					if updateErr != nil {
						e = updateErr
						break
					}
					activated++
				}
				if closeErr := rows.Close(); e == nil && closeErr != nil {
					e = closeErr
				}
				if e == nil && (activated != snapshotCount || snapshotCount != expectedCount) {
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
		// account_entitlements is a legacy compatibility projection. Hosted
		// accounts intentionally have no row, so its absence is non-fatal.
		if e == nil && p.ProductID == string(commerce.ProductTPAHub) {
			_, e = tx.ExecContext(c, `INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at,source,purchase_id) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET plan_code=excluded.plan_code,started_at=excluded.started_at,valid_from=excluded.valid_from,valid_until=excluded.valid_until,updated_at=excluded.updated_at,source=excluded.source,purchase_id=excluded.purchase_id`, p.AccountID, p.PlanCode, entitlementStart, entitlementStart, entitlementEnd, paidAt, paidAt, "purchase", purchaseID)
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
	return s.RecordProductMockBillingEvent(c, id, accountID, string(commerce.ProductTPAHub), period, eventType, payload, createdAt)
}
func (s *Store) RecordProductMockBillingEvent(c context.Context, id, accountID, productID, period, eventType, payload, createdAt string) error {
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
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(c, "INSERT INTO accounts VALUES(?,?,?,?,?)", a.ID, a.Email, a.PasswordHash, a.Status, a.CreatedAt); e != nil {
		return e
	}
	for _, grant := range grants {
		if grant.ProductID != commerce.ProductTPAHub {
			continue
		}
		validUntil := ""
		if grant.ValidUntil != nil {
			validUntil = grant.ValidUntil.UTC().Format(time.RFC3339Nano)
		}
		from := grant.ValidFrom.UTC().Format(time.RFC3339Nano)
		if _, e = tx.ExecContext(c, `INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at,source,purchase_id) VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, grant.OfferID, from, from, validUntil, a.CreatedAt, a.CreatedAt, grant.Source, grant.PurchaseID); e != nil {
			return e
		}
		break
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
	e := s.db.QueryRowContext(c, "SELECT id,email,password_hash,status,created_at FROM accounts WHERE id=?", id).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
	return a, e
}
func (s *Store) AccountByEmail(c context.Context, email string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT id,email,password_hash,status,created_at FROM accounts WHERE email=?", email).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
	return a, e
}
func (s *Store) ActiveAccountByEmail(c context.Context, email string) (Account, error) {
	var a Account
	e := s.db.QueryRowContext(c, "SELECT id,email,password_hash,status,created_at FROM accounts WHERE email=? AND status=?", email, AccountStatusActive).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
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
func (s *Store) SetActiveGeneration(c context.Context, repositoryID, generation, updatedAt string) error {
	_, err := s.db.ExecContext(c, `INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES(?,?,?) ON CONFLICT(repository_id) DO UPDATE SET active_generation=excluded.active_generation,updated_at=excluded.updated_at`, repositoryID, generation, updatedAt)
	return err
}

func (s *Store) CommitPublication(c context.Context, repositoryID, jobID, productID, capabilityID, activeAt, updatedAt string) error {
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var authorized int
	err = tx.QueryRowContext(c, `SELECT COUNT(*) FROM repositories r JOIN accounts a ON a.id=r.account_id WHERE r.id=? AND a.status=? AND (EXISTS(SELECT 1 FROM product_grants g WHERE g.customer_id=r.account_id AND g.product_id=? AND g.capability_id=? AND g.enabled=1 AND g.source<>'legacy' AND g.valid_from<=? AND (g.valid_until='' OR g.valid_until>?)) OR EXISTS(SELECT 1 FROM purchase_grant_instances g WHERE g.customer_id=r.account_id AND g.product_id=? AND g.capability_id=? AND g.enabled=1 AND g.valid_from<=? AND g.valid_until>?))`, repositoryID, AccountStatusActive, productID, capabilityID, activeAt, activeAt, productID, capabilityID, activeAt, activeAt).Scan(&authorized)
	if err != nil {
		return err
	}
	if authorized != 1 {
		return fmt.Errorf("publication authorization changed before commit")
	}
	if _, err = tx.ExecContext(c, `INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES(?,?,?) ON CONFLICT(repository_id) DO UPDATE SET active_generation=excluded.active_generation,updated_at=excluded.updated_at`, repositoryID, jobID, updatedAt); err != nil {
		return err
	}
	result, err := tx.ExecContext(c, `UPDATE publish_jobs SET state='succeeded',error='',finished_at=? WHERE id=? AND repository_id=? AND state='running'`, updatedAt, jobID, repositoryID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("publication job state changed before commit")
	}
	return tx.Commit()
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
func (s *Store) CreateJob(c context.Context, j Job) error {
	_, e := s.db.ExecContext(c, "INSERT INTO publish_jobs(id,repository_id,state,incoming_path,created_at) VALUES(?,?,?,?,?)", j.ID, j.RepositoryID, j.State, j.IncomingPath, j.CreatedAt)
	return e
}
func (s *Store) AccountStagingPaths(c context.Context, accountID string) ([]string, error) {
	rows, err := s.db.QueryContext(c, `SELECT j.incoming_path FROM publish_jobs j JOIN repositories r ON r.id=j.repository_id WHERE r.account_id=? AND j.state IN ('queued','running')`, accountID)
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
	e := s.db.QueryRowContext(c, "SELECT 1 FROM reader_credentials rc JOIN repositories r ON r.id=rc.repository_id JOIN accounts a ON a.id=r.account_id WHERE rc.repository_id=? AND rc.token_hash=? AND rc.revoked_at IS NULL AND a.status=?", repo, hash, AccountStatusActive).Scan(&n)
	return e == nil, e
}
