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

type migrationExecutor interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

var ErrNotFound = sql.ErrNoRows
var migrationFault func(string) error
var providerEventFault func(string) error
var migrationNow = time.Now

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

const currentSchemaVersion = 2

// Open creates a new database at the current baseline or opens an already
// migrated database. It never performs schema or historical/business
// migration; callers must invoke Migrate explicitly for an older database.
func Open(dir string) (*Store, error) { return open(dir, true, false) }

// Migrate explicitly upgrades an existing database to the current baseline.
// It is intentionally separate from normal service startup.
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
	probe, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open administrative database probe: %w", err)
	}
	defer probe.Close()
	var count int
	if err = probe.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('schema_migrations','accounts','operator_identity','operator_credentials','operator_recovery')`).Scan(&count); err != nil {
		return nil, fmt.Errorf("validate administrative database: %w", err)
	}
	if count != 5 {
		return nil, fmt.Errorf("not a complete TPA Hub database: %s", path)
	}
	return open(dir, false, false)
}

func ensureColumn(db migrationExecutor, table, name, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition)
	return err
}

func migrateIntentState(exec migrationExecutor) error {
	var columnCount int
	if err := exec.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('billing_purchases') WHERE name='intent_state'`).Scan(&columnCount); err != nil {
		return fmt.Errorf("inspect billing_purchases.intent_state: %w", err)
	}
	if columnCount == 0 {
		return nil
	}
	rows, err := exec.Query(`SELECT id,status,intent_state FROM billing_purchases WHERE status IS NULL OR intent_state IS NULL OR NOT ((status IN ('pending','failed','expired','paid') AND intent_state='current') OR (status='superseded' AND intent_state='superseded') OR (status='reconciliation_required' AND intent_state='reconciliation_required'))`)
	if err != nil {
		return fmt.Errorf("validate purchase intent state mapping: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var id, status, intent string
		if err := rows.Scan(&id, &status, &intent); err != nil {
			return fmt.Errorf("read purchase intent state mapping: %w", err)
		}
		return fmt.Errorf("invalid purchase lifecycle mapping: id=%s status=%q intent_state=%q", id, status, intent)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate purchase intent state mapping: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close purchase intent state mapping: %w", err)
	}
	if _, err := exec.Exec(`ALTER TABLE billing_purchases DROP COLUMN intent_state`); err != nil {
		return fmt.Errorf("remove billing_purchases.intent_state: %w", err)
	}
	if _, err := exec.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES('purchase-intentstate-elimination-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		return fmt.Errorf("record intent state migration: %w", err)
	}
	return nil
}

func validateSchemaAuthority(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_xinfo(tpahub_schema)`)
	if err != nil {
		return fmt.Errorf("inspect schema authority columns: %w", err)
	}
	defer rows.Close()
	var singletonPK, versionNotNull bool
	var columns int
	for rows.Next() {
		var cid, notNull, pk, hidden int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk, &hidden); err != nil {
			return fmt.Errorf("read schema authority columns: %w", err)
		}
		columns++
		if hidden != 0 {
			return fmt.Errorf("schema authority has hidden or generated column %q", name)
		}
		if name == "singleton" {
			if !strings.Contains(strings.ToUpper(columnType), "INT") || pk != 1 {
				return fmt.Errorf("schema authority singleton column is not an integer primary key")
			}
			singletonPK = true
		} else if name == "version" {
			if !strings.Contains(strings.ToUpper(columnType), "INT") || notNull != 1 {
				return fmt.Errorf("schema authority version column is not a non-null integer")
			}
			versionNotNull = true
		} else {
			return fmt.Errorf("schema authority has unexpected column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect schema authority columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close schema authority columns: %w", err)
	}
	if columns != 2 || !singletonPK || !versionNotNull {
		return fmt.Errorf("schema authority has invalid column structure")
	}
	var rowsCount, singleton, version int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(singleton),0),COALESCE(MAX(version),0) FROM tpahub_schema`).Scan(&rowsCount, &singleton, &version); err != nil {
		return fmt.Errorf("read schema authority row: %w", err)
	}
	if rowsCount != 1 || singleton != 1 || version <= 0 {
		return fmt.Errorf("schema authority has invalid row state")
	}
	if _, err := db.Exec(`SAVEPOINT tpahub_schema_authority_validation`); err != nil {
		return fmt.Errorf("begin schema authority validation: %w", err)
	}
	defer func() {
		_, _ = db.Exec(`ROLLBACK TO tpahub_schema_authority_validation`)
		_, _ = db.Exec(`RELEASE tpahub_schema_authority_validation`)
	}()
	probe := func(singleton any, version any, label string) error {
		if _, err := db.Exec(`DELETE FROM tpahub_schema; INSERT INTO tpahub_schema(singleton,version) VALUES(?,?)`, singleton, version); err == nil {
			return fmt.Errorf("schema authority does not enforce %s", label)
		}
		return nil
	}
	if err := probe(2, 1, "singleton=1"); err != nil {
		return err
	}
	if err := probe(1, 0, "version>0"); err != nil {
		return err
	}
	if err := probe(1, nil, "version NOT NULL"); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM tpahub_schema; INSERT INTO tpahub_schema(singleton,version) VALUES(1,1); INSERT INTO tpahub_schema(singleton,version) VALUES(1,1)`); err == nil {
		return fmt.Errorf("schema authority does not enforce singleton uniqueness")
	}
	return nil
}

func validateCurrentPurchaseSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_xinfo(billing_purchases)`)
	if err != nil {
		return fmt.Errorf("inspect purchase columns: %w", err)
	}
	defer rows.Close()
	want := []struct {
		name, typ   string
		notNull, pk int
	}{
		{"id", "TEXT", 0, 1}, {"account_id", "TEXT", 1, 0}, {"product_id", "TEXT", 1, 0},
		{"plan_code", "TEXT", 1, 0}, {"offer_version", "TEXT", 1, 0}, {"rate_card_id", "TEXT", 1, 0},
		{"rate_card_mode", "TEXT", 1, 0}, {"tax_classification", "TEXT", 1, 0}, {"currency", "TEXT", 1, 0},
		{"amount_minor", "INTEGER", 1, 0}, {"billing_environment", "TEXT", 1, 0}, {"provider", "TEXT", 1, 0},
		{"status", "TEXT", 1, 0}, {"entitlement_period_start", "TEXT", 1, 0}, {"entitlement_period_end", "TEXT", 1, 0},
		{"provider_reference", "TEXT", 1, 0}, {"checkout_url", "TEXT", 1, 0}, {"checkout_request_version", "TEXT", 1, 0},
		{"created_at", "TEXT", 1, 0}, {"updated_at", "TEXT", 1, 0}, {"paid_at", "TEXT", 1, 0},
		{"failed_at", "TEXT", 1, 0}, {"expired_at", "TEXT", 1, 0}, {"provider_state", "TEXT", 1, 0},
		{"reconciliation_reason", "TEXT", 1, 0}, {"snapshot_finalized", "INTEGER", 1, 0},
	}
	seen := make(map[string]bool, len(want))
	for rows.Next() {
		var cid, notNull, pk, hidden int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk, &hidden); err != nil {
			return fmt.Errorf("read purchase columns: %w", err)
		}
		if hidden != 0 || seen[name] {
			return fmt.Errorf("purchase schema has invalid or duplicate column %q", name)
		}
		seen[name] = true
		var expected *struct {
			name, typ   string
			notNull, pk int
		}
		for i := range want {
			if want[i].name == name {
				expected = &want[i]
				break
			}
		}
		if expected == nil || !strings.EqualFold(typ, expected.typ) || notNull != expected.notNull || pk != expected.pk {
			return fmt.Errorf("purchase schema column %q is invalid: got %q notnull=%d pk=%d", name, typ, notNull, pk)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect purchase columns: %w", err)
	}
	if len(seen) != len(want) {
		return fmt.Errorf("purchase schema column count is invalid")
	}
	return nil
}

func validateCurrentSchema(db *sql.DB) error {
	// tpahub_schema is the current-schema authority; schema_migrations is only
	// the historical ledger of individual migrations.
	requiredTables := []string{"accounts", "account_metadata", "operator_identity", "operator_credentials", "operator_recovery", "sessions", "repositories", "publish_jobs", "repository_publications", "egress_accounting_uncertain", "generation_cleanup_tasks", "admin_audit_log", "product_grants", "billing_purchases", "purchase_grant_instances", "purchase_grant_snapshots", "billing_provider_events", "schema_migrations", "tpahub_schema"}
	for _, name := range requiredTables {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("current schema is missing table %q", name)
		}
	}
	if err := validateSchemaAuthority(db); err != nil {
		return err
	}
	if err := validateCurrentPurchaseSchema(db); err != nil {
		return err
	}
	requiredTriggers := []string{"purchase_grant_snapshots_immutable_update", "purchase_grant_snapshots_immutable_delete", "purchase_grant_snapshots_no_late_insert", "billing_purchase_terms_immutable", "purchase_grant_instances_immutable_update", "purchase_grant_instances_immutable_delete"}
	for _, name := range requiredTriggers {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("current schema is missing trigger %q", name)
		}
	}
	return nil
}

func open(dir string, reconcileRunning, explicitMigration bool) (*Store, error) {
	path := filepath.Join(dir, "tpahub.db")
	_, statErr := os.Stat(path)
	fresh := os.IsNotExist(statErr)
	if statErr != nil && !fresh {
		return nil, fmt.Errorf("inspect database: %w", statErr)
	}
	if e := os.MkdirAll(dir, 0755); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	if !fresh && !explicitMigration {
		var markerCount, schemaVersion int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tpahub_schema'`).Scan(&markerCount); err != nil {
			db.Close()
			return nil, fmt.Errorf("inspect database schema marker: %w", err)
		}
		if markerCount == 0 {
			db.Close()
			return nil, fmt.Errorf("database migration required: schema version is not recorded")
		}
		if err := db.QueryRow(`SELECT version FROM tpahub_schema WHERE singleton=1 AND (SELECT COUNT(*) FROM tpahub_schema)=1`).Scan(&schemaVersion); err != nil {
			db.Close()
			return nil, fmt.Errorf("read database schema version: %w", err)
		}
		if schemaVersion != currentSchemaVersion {
			db.Close()
			return nil, fmt.Errorf("unsupported database schema version %d (current %d)", schemaVersion, currentSchemaVersion)
		}
	}
	var migrationTx *sql.Tx
	var migrationExec migrationExecutor = db
	if explicitMigration {
		if _, e = db.Exec(`PRAGMA foreign_keys=ON`); e != nil {
			db.Close()
			return nil, fmt.Errorf("enable database foreign keys for migration: %w", e)
		}
		migrationTx, e = db.Begin()
		if e != nil {
			db.Close()
			return nil, fmt.Errorf("begin database migration: %w", e)
		}
		migrationExec = migrationTx
		defer migrationTx.Rollback()
	}
	if fresh || explicitMigration {
		_, e = migrationExec.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS schema_migrations(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS tpahub_schema(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version > 0)); CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS account_metadata(account_id TEXT PRIMARY KEY REFERENCES accounts(id),account_class TEXT NOT NULL DEFAULT 'unclassified'); CREATE TABLE IF NOT EXISTS operator_identity(id INTEGER PRIMARY KEY CHECK(id=1),user_handle BLOB NOT NULL UNIQUE); CREATE TABLE IF NOT EXISTS operator_credentials(credential_id BLOB PRIMARY KEY,credential_data BLOB NOT NULL,label TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,last_used_at TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS operator_recovery(id INTEGER PRIMARY KEY CHECK(id=1),token_hash BLOB NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,expires_at_unix_nanos INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT); CREATE TABLE IF NOT EXISTS repositories(id TEXT PRIMARY KEY,account_id TEXT, name TEXT NOT NULL,visibility TEXT NOT NULL DEFAULT 'public',created_at TEXT NOT NULL,UNIQUE(account_id,name),FOREIGN KEY(account_id) REFERENCES accounts(id)); CREATE TABLE IF NOT EXISTS publish_jobs(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),state TEXT NOT NULL,incoming_path TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT,cleanup_state TEXT NOT NULL DEFAULT 'complete',cleanup_error TEXT NOT NULL DEFAULT '',cleanup_attempts INTEGER NOT NULL DEFAULT 0,cleanup_next_retry TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS reader_credentials(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),name TEXT NOT NULL,token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,revoked_at TEXT,UNIQUE(repository_id,name)); CREATE INDEX IF NOT EXISTS publish_jobs_queue ON publish_jobs(state,created_at); CREATE TABLE IF NOT EXISTS repository_egress_usage(repository_id TEXT NOT NULL REFERENCES repositories(id),period TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,period)); CREATE INDEX IF NOT EXISTS repository_egress_period ON repository_egress_usage(period); CREATE TABLE IF NOT EXISTS account_storage_high_water(account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,high_water_bytes INTEGER NOT NULL CHECK(high_water_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(account_id,period)); CREATE TABLE IF NOT EXISTS mock_billing_events(id TEXT NOT NULL,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(product_id,id),UNIQUE(account_id,product_id,period,event_type)); CREATE TABLE IF NOT EXISTS billing_provider_accounts(account_id TEXT NOT NULL REFERENCES accounts(id),provider TEXT NOT NULL,environment TEXT NOT NULL,provider_customer_id TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(account_id,provider,environment)); CREATE TABLE IF NOT EXISTS billing_provider_submissions(account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,product_id,period,provider,environment)); CREATE TABLE IF NOT EXISTS account_entitlements(account_id TEXT PRIMARY KEY REFERENCES accounts(id),plan_code TEXT NOT NULL,started_at TEXT NOT NULL,valid_from TEXT NOT NULL DEFAULT '',valid_until TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,source TEXT NOT NULL DEFAULT 'legacy',purchase_id TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS product_grants(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL DEFAULT '',period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),source TEXT NOT NULL,purchase_id TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(customer_id,product_id,capability_id)); CREATE TABLE IF NOT EXISTS purchase_grant_snapshots(purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),capability_id TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),PRIMARY KEY(purchase_id,capability_id)); CREATE TABLE IF NOT EXISTS purchase_grant_snapshot_sets(purchase_id TEXT PRIMARY KEY REFERENCES billing_purchases(id),expected_count INTEGER NOT NULL CHECK(expected_count>0),capability_digest TEXT NOT NULL); CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_immutable_update BEFORE UPDATE ON purchase_grant_snapshots BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are immutable'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_immutable_delete BEFORE DELETE ON purchase_grant_snapshots BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are immutable'); END; CREATE TABLE IF NOT EXISTS billing_purchases(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,plan_code TEXT NOT NULL,offer_version TEXT NOT NULL DEFAULT 'legacy-unversioned',rate_card_id TEXT NOT NULL,rate_card_mode TEXT NOT NULL,tax_classification TEXT NOT NULL DEFAULT 'saas-personal',currency TEXT NOT NULL,amount_minor INTEGER NOT NULL CHECK(amount_minor >= 0),billing_environment TEXT NOT NULL,provider TEXT NOT NULL,status TEXT NOT NULL,entitlement_period_start TEXT NOT NULL,entitlement_period_end TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',checkout_url TEXT NOT NULL DEFAULT '',checkout_request_version TEXT NOT NULL DEFAULT 'v1',provider_state TEXT NOT NULL DEFAULT 'open',reconciliation_reason TEXT NOT NULL DEFAULT '',snapshot_finalized INTEGER NOT NULL DEFAULT 0 CHECK(snapshot_finalized IN (0,1)),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',failed_at TEXT NOT NULL DEFAULT '',expired_at TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS billing_provider_events(provider TEXT NOT NULL,environment TEXT NOT NULL,provider_event_id TEXT NOT NULL,event_type TEXT NOT NULL,received_at TEXT NOT NULL,processed_at TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',purchase_id TEXT NOT NULL DEFAULT '',provider_reference TEXT NOT NULL DEFAULT '',PRIMARY KEY(provider,environment,provider_event_id)); CREATE TABLE IF NOT EXISTS repository_egress_daily(repository_id TEXT NOT NULL REFERENCES repositories(id),usage_date TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,usage_date)); CREATE TABLE IF NOT EXISTS purchase_grant_instances(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(purchase_id,capability_id)); CREATE INDEX IF NOT EXISTS purchase_grant_instances_lookup ON purchase_grant_instances(customer_id,product_id,capability_id); CREATE INDEX IF NOT EXISTS repository_egress_daily_date ON repository_egress_daily(usage_date);`)
		if e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize database: %w", e)
		}
		if explicitMigration {
			var markerSQL string
			if e = migrationExec.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='tpahub_schema'`).Scan(&markerSQL); e != nil {
				db.Close()
				return nil, fmt.Errorf("inspect schema authority: %w", e)
			}
			if !strings.Contains(strings.ReplaceAll(markerSQL, " ", ""), "singletonINTEGERPRIMARYKEY") {
				if _, e = migrationExec.Exec(`CREATE TABLE tpahub_schema_new(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version>0)); INSERT INTO tpahub_schema_new(singleton,version) SELECT 1,version FROM tpahub_schema; DROP TABLE tpahub_schema; ALTER TABLE tpahub_schema_new RENAME TO tpahub_schema`); e != nil {
					db.Close()
					return nil, fmt.Errorf("upgrade schema authority: %w", e)
				}
			}
		}
	} else {
		_, e = migrationExec.Exec(`PRAGMA foreign_keys=ON`)
		if e != nil {
			db.Close()
			return nil, fmt.Errorf("enable database foreign keys: %w", e)
		}
	}
	var schemaVersion int
	versionErr := migrationExec.QueryRow(`SELECT version FROM tpahub_schema WHERE singleton=1 AND (SELECT COUNT(*) FROM tpahub_schema)=1`).Scan(&schemaVersion)
	var currentMigrationMarkers int
	if explicitMigration && versionErr == nil && schemaVersion == currentSchemaVersion {
		if err := migrationExec.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name IN ('purchase-intentstate-elimination-v1','commerce-product-grants-v1','historical-purchase-grant-instances-v1','legacy-six-month-nano-v1')`).Scan(&currentMigrationMarkers); err != nil {
			db.Close()
			return nil, fmt.Errorf("inspect current migration markers: %w", err)
		}
	}
	if explicitMigration && versionErr == nil && schemaVersion == currentSchemaVersion && currentMigrationMarkers == 4 {
		if err := migrationTx.Commit(); err != nil {
			db.Close()
			return nil, fmt.Errorf("commit current schema migration check: %w", err)
		}
		if err := validateCurrentSchema(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("validate current schema: %w", err)
		}
		return s, nil
	}
	if !fresh && !explicitMigration {
		if versionErr == sql.ErrNoRows {
			db.Close()
			return nil, fmt.Errorf("database migration required: schema version is not recorded")
		}
		if versionErr != nil {
			db.Close()
			return nil, fmt.Errorf("read database schema version: %w", versionErr)
		}
		if schemaVersion != currentSchemaVersion {
			db.Close()
			return nil, fmt.Errorf("unsupported database schema version %d (current %d)", schemaVersion, currentSchemaVersion)
		}
		if err := validateCurrentSchema(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("validate current schema: %w", err)
		}
	}
	if fresh || explicitMigration {
		if e = migrateOperatorRecoveryTimes(migrationExec); e != nil {
			db.Close()
			return nil, fmt.Errorf("migrate operator recovery times: %w", e)
		}
		if _, e = migrationExec.Exec(`INSERT OR IGNORE INTO account_metadata(account_id,account_class) SELECT id,'unclassified' FROM accounts`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize account metadata: %w", e)
		}
		if _, e = migrationExec.Exec(`CREATE TABLE IF NOT EXISTS repository_publications(repository_id TEXT PRIMARY KEY REFERENCES repositories(id),active_generation TEXT NOT NULL,updated_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS egress_accounting_uncertain(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),created_at TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'uncertain',version INTEGER NOT NULL DEFAULT 1,period_start TEXT NOT NULL DEFAULT '',period_end TEXT NOT NULL DEFAULT '',reserved_bytes INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS generation_cleanup_tasks(repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,attempts INTEGER NOT NULL DEFAULT 0,next_retry TEXT NOT NULL,last_error TEXT NOT NULL,updated_at TEXT NOT NULL)`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize publication state: %w", e)
		}
		if _, e = migrationExec.Exec(`CREATE TABLE IF NOT EXISTS admin_audit_log(id TEXT PRIMARY KEY,action TEXT NOT NULL,marker_id TEXT NOT NULL,account_id TEXT NOT NULL,repository_id TEXT NOT NULL,previous_state TEXT NOT NULL,previous_version INTEGER NOT NULL,result TEXT NOT NULL,operator TEXT NOT NULL,created_at TEXT NOT NULL,details TEXT NOT NULL DEFAULT '')`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize administrative audit state: %w", e)
		}
		if explicitMigration {
			if e = ensureColumn(migrationExec, "billing_purchases", "offer_version", "offer_version TEXT NOT NULL DEFAULT 'legacy-unversioned'"); e != nil {
				db.Close()
				return nil, fmt.Errorf("migrate offer version: %w", e)
			}
			for _, column := range []struct{ table, name, definition string }{
				{"account_entitlements", "source", "source TEXT NOT NULL DEFAULT 'legacy'"},
				{"account_entitlements", "purchase_id", "purchase_id TEXT NOT NULL DEFAULT ''"},
				{"billing_purchases", "tax_classification", "tax_classification TEXT NOT NULL DEFAULT 'saas-personal'"},
				{"billing_purchases", "checkout_request_version", "checkout_request_version TEXT NOT NULL DEFAULT 'v1'"},
				{"billing_purchases", "snapshot_finalized", "snapshot_finalized INTEGER NOT NULL DEFAULT 0"},
				{"billing_purchases", "checkout_url", "checkout_url TEXT NOT NULL DEFAULT ''"},
				{"billing_purchases", "product_id", "product_id TEXT NOT NULL DEFAULT 'tpa-hub'"},

				{"billing_purchases", "provider_state", "provider_state TEXT NOT NULL DEFAULT 'open'"},
				{"billing_purchases", "reconciliation_reason", "reconciliation_reason TEXT NOT NULL DEFAULT ''"},
				{"billing_provider_events", "purchase_id", "purchase_id TEXT NOT NULL DEFAULT ''"},
				{"billing_provider_events", "provider_reference", "provider_reference TEXT NOT NULL DEFAULT ''"},
				{"account_entitlements", "valid_from", "valid_from TEXT NOT NULL DEFAULT ''"},
				{"account_entitlements", "valid_until", "valid_until TEXT NOT NULL DEFAULT ''"},
				{"billing_provider_submissions", "product_id", "product_id TEXT NOT NULL DEFAULT 'tpa-hub'"},
				{"mock_billing_events", "product_id", "product_id TEXT NOT NULL DEFAULT 'tpa-hub'"},
				{"egress_accounting_uncertain", "state", "state TEXT NOT NULL DEFAULT 'uncertain'"},
				{"egress_accounting_uncertain", "version", "version INTEGER NOT NULL DEFAULT 1"},
				{"egress_accounting_uncertain", "period_start", "period_start TEXT NOT NULL DEFAULT ''"},
				{"egress_accounting_uncertain", "period_end", "period_end TEXT NOT NULL DEFAULT ''"},
				{"egress_accounting_uncertain", "reserved_bytes", "reserved_bytes INTEGER NOT NULL DEFAULT 0"},
				{"publish_jobs", "cleanup_state", "cleanup_state TEXT NOT NULL DEFAULT 'complete'"},
				{"publish_jobs", "cleanup_error", "cleanup_error TEXT NOT NULL DEFAULT ''"},
				{"publish_jobs", "cleanup_attempts", "cleanup_attempts INTEGER NOT NULL DEFAULT 0"},
				{"publish_jobs", "cleanup_next_retry", "cleanup_next_retry TEXT NOT NULL DEFAULT ''"},
			} {
				if e = ensureColumn(migrationExec, column.table, column.name, column.definition); e != nil {
					db.Close()
					return nil, fmt.Errorf("migrate %s.%s: %w", column.table, column.name, e)
				}
			}
			if e = migrateIntentState(migrationExec); e != nil {
				db.Close()
				return nil, e
			}
			if e = migrationCheckpoint("after-schema-migration"); e != nil {
				db.Close()
				return nil, e
			}
			if e = migrateProductScopedKeys(migrationExec); e != nil {
				db.Close()
				return nil, e
			}
			if e = enforceUniquePurchaseProviderReferences(migrationExec); e != nil {
				db.Close()
				return nil, e
			}
			if _, e = migrationExec.Exec(`DROP INDEX IF EXISTS billing_purchases_pending; CREATE UNIQUE INDEX billing_purchases_pending ON billing_purchases(account_id,product_id,provider,billing_environment) WHERE status='pending'; UPDATE account_entitlements SET valid_from=started_at WHERE valid_from='';
UPDATE account_entitlements SET valid_from=COALESCE((SELECT entitlement_period_start FROM billing_purchases WHERE id=account_entitlements.purchase_id AND status='paid'),valid_from),valid_until=COALESCE((SELECT entitlement_period_end FROM billing_purchases WHERE id=account_entitlements.purchase_id AND status='paid'),'') WHERE source='purchase';`); e != nil {
				db.Close()
				return nil, fmt.Errorf("migrate entitlement validity: %w", e)
			}
		}
		if _, e = migrationExec.Exec(`DROP TRIGGER IF EXISTS purchase_grant_snapshots_no_late_insert; CREATE TRIGGER purchase_grant_snapshots_no_late_insert BEFORE INSERT ON purchase_grant_snapshots WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=NEW.purchase_id AND (snapshot_finalized=1 OR status='reconciliation_required')) BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are finalized'); END; DROP TRIGGER IF EXISTS billing_purchase_terms_immutable; CREATE TRIGGER billing_purchase_terms_immutable AFTER UPDATE ON billing_purchases WHEN (OLD.snapshot_finalized=1 OR OLD.status='reconciliation_required') AND (NEW.snapshot_finalized<>OLD.snapshot_finalized OR NEW.plan_code<>OLD.plan_code OR NEW.offer_version<>OLD.offer_version OR NEW.product_id<>OLD.product_id OR NEW.amount_minor<>OLD.amount_minor OR NEW.currency<>OLD.currency OR NEW.rate_card_id<>OLD.rate_card_id OR NEW.rate_card_mode<>OLD.rate_card_mode OR NEW.tax_classification<>OLD.tax_classification OR NEW.entitlement_period_start<>OLD.entitlement_period_start OR NEW.entitlement_period_end<>OLD.entitlement_period_end OR NEW.provider<>OLD.provider OR NEW.provider_reference<>OLD.provider_reference OR NEW.billing_environment<>OLD.billing_environment) BEGIN SELECT RAISE(ABORT,'billing purchase terms are immutable'); END; CREATE TABLE IF NOT EXISTS purchase_grant_snapshot_sets(purchase_id TEXT PRIMARY KEY REFERENCES billing_purchases(id),expected_count INTEGER NOT NULL CHECK(expected_count>0),capability_digest TEXT NOT NULL); DROP TRIGGER IF EXISTS purchase_grant_snapshot_sets_no_late_insert; CREATE TRIGGER purchase_grant_snapshot_sets_no_late_insert BEFORE INSERT ON purchase_grant_snapshot_sets WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=NEW.purchase_id AND (snapshot_finalized=1 OR status='reconciliation_required')) BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are finalized'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshot_sets_immutable_update BEFORE UPDATE ON purchase_grant_snapshot_sets BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are immutable'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshot_sets_immutable_delete BEFORE DELETE ON purchase_grant_snapshot_sets BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are immutable'); END; CREATE TABLE IF NOT EXISTS purchase_grant_instances(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(purchase_id,capability_id)); CREATE INDEX IF NOT EXISTS purchase_grant_instances_lookup ON purchase_grant_instances(customer_id,product_id,capability_id); CREATE TRIGGER IF NOT EXISTS migration_grants_commercial_terms_immutable BEFORE UPDATE ON product_grants WHEN OLD.source='migration' AND (NEW.source<>OLD.source OR NEW.offer_id<>OLD.offer_id OR NEW.valid_from<>OLD.valid_from OR NEW.valid_until<>OLD.valid_until OR NEW.period_cadence<>OLD.period_cadence OR NEW.storage_limit_bytes<>OLD.storage_limit_bytes OR NEW.egress_limit_bytes<>OLD.egress_limit_bytes OR NEW.operation_metric<>OLD.operation_metric OR NEW.operation_limit<>OLD.operation_limit OR NEW.purchase_id<>OLD.purchase_id) BEGIN SELECT RAISE(ABORT,'migration grant terms are immutable'); END`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize purchase grant instances: %w", e)
		}
		if _, e = migrationExec.Exec(`DROP TRIGGER IF EXISTS billing_purchase_owner_immutable; CREATE TRIGGER billing_purchase_owner_immutable BEFORE UPDATE ON billing_purchases WHEN (OLD.snapshot_finalized=1 OR OLD.status='reconciliation_required') AND NEW.account_id<>OLD.account_id BEGIN SELECT RAISE(ABORT,'billing purchase ownership is immutable'); END; DROP TRIGGER IF EXISTS billing_purchase_reconciliation_reason_immutable; CREATE TRIGGER billing_purchase_reconciliation_reason_immutable BEFORE UPDATE ON billing_purchases WHEN OLD.status='reconciliation_required' AND NEW.reconciliation_reason<>OLD.reconciliation_reason BEGIN SELECT RAISE(ABORT,'billing purchase reconciliation reason is immutable'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_immutable_update BEFORE UPDATE ON purchase_grant_snapshots WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=OLD.purchase_id AND snapshot_finalized=1) BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are finalized'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_snapshots_immutable_delete BEFORE DELETE ON purchase_grant_snapshots WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=OLD.purchase_id AND snapshot_finalized=1) BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are finalized'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_instances_immutable_update BEFORE UPDATE ON purchase_grant_instances BEGIN SELECT RAISE(ABORT,'purchase grant instances are immutable'); END; CREATE TRIGGER IF NOT EXISTS purchase_grant_instances_immutable_delete BEFORE DELETE ON purchase_grant_instances BEGIN SELECT RAISE(ABORT,'purchase grant instances are immutable'); END`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize purchase grant integrity: %w", e)
		}
		if _, e = migrationExec.Exec(`DROP TRIGGER IF EXISTS purchase_grant_instances_no_unauthorized_insert; CREATE TRIGGER purchase_grant_instances_no_unauthorized_insert BEFORE INSERT ON purchase_grant_instances BEGIN SELECT CASE WHEN EXISTS(SELECT 1 FROM billing_purchases p WHERE p.id=NEW.purchase_id AND p.status='paid') THEN RAISE(ABORT,'purchase grant instances are immutable after activation') END; END;`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize purchase grant insert integrity: %w", e)
		}
		if _, e = migrationExec.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS billing_purchases_provider_reference ON billing_purchases(provider, billing_environment, provider_reference) WHERE provider_reference<>''`); e != nil {
			db.Close()
			return nil, fmt.Errorf("initialize provider reference index: %w", e)
		}
		if explicitMigration {
			if e = migrateLegacyTPAGrants(migrationExec); e != nil {
				db.Close()
				return nil, e
			}
			if e = migrateHistoricalPurchaseGrantInstances(migrationExec); e != nil {
				db.Close()
				return nil, e
			}
			if e = migrateLegacyAccessToSixMonthNano(migrationExec); e != nil {
				db.Close()
				return nil, e
			}
			if e = rebuildCompatibilityProjection(migrationExec); e != nil {
				db.Close()
				return nil, fmt.Errorf("rebuild compatibility projection: %w", e)
			}
		}
		var hasOwner, hasVisibility bool
		rows, pe := migrationExec.Query(`PRAGMA table_info(repositories)`)
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
			if _, e = migrationExec.Exec(`ALTER TABLE repositories ADD COLUMN account_id TEXT`); e != nil {
				db.Close()
				return nil, e
			}
		}
		if !hasVisibility {
			if _, e = migrationExec.Exec(`ALTER TABLE repositories ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'`); e != nil {
				db.Close()
				return nil, e
			}
		}
	}
	if fresh || explicitMigration {
		if _, e = migrationExec.Exec(`DELETE FROM tpahub_schema; INSERT INTO tpahub_schema(singleton,version) VALUES(1,?); INSERT OR IGNORE INTO schema_migrations(name,applied_at) VALUES
('purchase-intentstate-elimination-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now')),
('commerce-product-grants-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now')),
('historical-purchase-grant-instances-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now')),
('legacy-six-month-nano-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, currentSchemaVersion); e != nil {
			db.Close()
			return nil, fmt.Errorf("record current schema version: %w", e)
		}
	}
	if explicitMigration {
		if e = migrationTx.Commit(); e != nil {
			db.Close()
			return nil, fmt.Errorf("commit database migration: %w", e)
		}
	}
	if reconcileRunning {
		_, e = db.Exec(`UPDATE publish_jobs SET state='failed',error='interrupted by service restart',finished_at=?,cleanup_state='pending' WHERE state='running'`, time.Now().UTC().Format(time.RFC3339Nano))
		if e == nil {
			_, e = db.Exec(`UPDATE egress_accounting_uncertain SET state='uncertain',version=version+1 WHERE state='streaming'`)
		}
	}
	if e != nil {
		db.Close()
	}
	return s, e
}
func migrateLegacyTPAGrants(exec migrationExecutor) error {
	var err error
	rows, err := exec.Query(`SELECT DISTINCT plan_code FROM account_entitlements WHERE source='legacy'`)
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
	legacyEntitlements, err := exec.Query(`SELECT account_id,valid_from,valid_until FROM account_entitlements WHERE source='legacy'`)
	if err != nil {
		return fmt.Errorf("inspect legacy entitlement validity: %w", err)
	}
	for legacyEntitlements.Next() {
		var accountID, validFrom, validUntil string
		if err = legacyEntitlements.Scan(&accountID, &validFrom, &validUntil); err != nil {
			legacyEntitlements.Close()
			return fmt.Errorf("read legacy entitlement validity: %w", err)
		}
		if accountID == "" || !validHistoricalWindow(validFrom, validUntil) {
			legacyEntitlements.Close()
			return fmt.Errorf("invalid legacy entitlement validity: account=%s", accountID)
		}
	}
	if err = legacyEntitlements.Err(); err != nil {
		legacyEntitlements.Close()
		return fmt.Errorf("inspect legacy entitlement validity: %w", err)
	}
	legacyEntitlements.Close()
	legacyGrants, err := exec.Query(`SELECT customer_id,product_id,offer_id,enabled,valid_from,valid_until FROM product_grants WHERE source='legacy' AND product_id=?`, string(tpahubproduct.ProductID))
	if err != nil {
		return fmt.Errorf("inspect legacy grant validity: %w", err)
	}
	for legacyGrants.Next() {
		var customerID, productID, offerID, validFrom, validUntil string
		var enabled int
		if err = legacyGrants.Scan(&customerID, &productID, &offerID, &enabled, &validFrom, &validUntil); err != nil {
			legacyGrants.Close()
			return fmt.Errorf("read legacy grant validity: %w", err)
		}
		if customerID == "" || productID != string(tpahubproduct.ProductID) || !knownHistoricalOffer(offerID) || !validHistoricalWindow(validFrom, validUntil) || enabled < 0 || enabled > 1 {
			legacyGrants.Close()
			return fmt.Errorf("invalid legacy TPA grant: customer=%s offer=%s", customerID, offerID)
		}
	}
	if err = legacyGrants.Err(); err != nil {
		legacyGrants.Close()
		return fmt.Errorf("inspect legacy grant validity: %w", err)
	}
	legacyGrants.Close()
	sources, err := exec.Query(`SELECT DISTINCT source FROM account_entitlements`)
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
	purchaseRows, err := exec.Query(`SELECT e.account_id,e.purchase_id,COALESCE(p.account_id,''),COALESCE(p.product_id,''),e.plan_code,COALESCE(p.plan_code,''),COALESCE(p.status,''),COALESCE(p.entitlement_period_start,''),COALESCE(p.entitlement_period_end,''),e.valid_from,e.valid_until FROM account_entitlements e LEFT JOIN billing_purchases p ON p.id=e.purchase_id WHERE e.source='purchase'`)
	if err != nil {
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	for purchaseRows.Next() {
		var accountID, purchaseID, purchaseAccount, productID, entitlementPlan, purchasePlan, status, purchaseFrom, purchaseUntil, validFrom, validUntil string
		if err = purchaseRows.Scan(&accountID, &purchaseID, &purchaseAccount, &productID, &entitlementPlan, &purchasePlan, &status, &purchaseFrom, &purchaseUntil, &validFrom, &validUntil); err != nil {
			purchaseRows.Close()
			return fmt.Errorf("read purchase-backed grant: %w", err)
		}
		from, fromErr := time.Parse(time.RFC3339Nano, validFrom)
		until, untilErr := time.Parse(time.RFC3339Nano, validUntil)
		if accountID == "" || purchaseID == "" || purchaseAccount != accountID || productID != string(tpahubproduct.ProductID) || entitlementPlan != purchasePlan || status != "paid" || purchaseFrom != validFrom || purchaseUntil != validUntil || validFrom == "" || validUntil == "" || fromErr != nil || untilErr != nil || !until.After(from) {
			purchaseRows.Close()
			return fmt.Errorf("invalid purchase-backed entitlement: account=%s purchase=%s status=%s", accountID, purchaseID, status)
		}
	}
	if err = purchaseRows.Err(); err != nil {
		purchaseRows.Close()
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	purchaseRows.Close()
	purchaseRows, err = exec.Query(`SELECT g.customer_id,g.product_id,g.purchase_id,COALESCE(p.account_id,''),COALESCE(p.product_id,''),COALESCE(p.plan_code,''),COALESCE(p.status,''),COALESCE(p.entitlement_period_start,''),COALESCE(p.entitlement_period_end,''),g.offer_id,g.valid_from,g.valid_until FROM product_grants g LEFT JOIN billing_purchases p ON p.id=g.purchase_id WHERE g.source='purchase'`)
	if err != nil {
		return fmt.Errorf("inspect purchase-backed grants: %w", err)
	}
	for purchaseRows.Next() {
		var accountID, grantProductID, purchaseID, purchaseAccount, purchaseProductID, purchasePlan, status, purchaseFrom, purchaseUntil, offerID, validFrom, validUntil string
		if err = purchaseRows.Scan(&accountID, &grantProductID, &purchaseID, &purchaseAccount, &purchaseProductID, &purchasePlan, &status, &purchaseFrom, &purchaseUntil, &offerID, &validFrom, &validUntil); err != nil {
			purchaseRows.Close()
			return fmt.Errorf("read purchase-backed grant: %w", err)
		}
		from, fromErr := time.Parse(time.RFC3339Nano, validFrom)
		until, untilErr := time.Parse(time.RFC3339Nano, validUntil)
		if accountID == "" || grantProductID != string(tpahubproduct.ProductID) || purchaseID == "" || purchaseAccount != accountID || purchaseProductID != grantProductID || purchasePlan != offerID || status != "paid" || purchaseFrom != validFrom || purchaseUntil != validUntil || validFrom == "" || validUntil == "" || fromErr != nil || untilErr != nil || !until.After(from) {
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
	if err = exec.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='commerce-product-grants-v1'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	if err = migrationCheckpoint("before-materialization"); err != nil {
		return err
	}
	if _, err = exec.Exec(`INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.create',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,0,source,purchase_id,created_at,updated_at FROM account_entitlements WHERE source='legacy';
	INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.publish',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,CASE plan_code WHEN 'nano' THEN 5000000 WHEN 'standard' THEN 500000000 ELSE 9223372036854775807 END,0,source,purchase_id,created_at,updated_at FROM account_entitlements WHERE source='legacy';
	INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.download',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,CASE plan_code WHEN 'nano' THEN 12000000000 WHEN 'standard' THEN 20000000000 ELSE 9223372036854775807 END,source,purchase_id,created_at,updated_at FROM account_entitlements WHERE source='legacy';
	INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT account_id,'tpa-hub','repository.read.private',plan_code,1,valid_from,valid_until,CASE WHEN plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,0,source,purchase_id,created_at,updated_at FROM account_entitlements WHERE source='legacy';
INSERT OR IGNORE INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit) SELECT p.id,g.capability_id,g.period_cadence,g.storage_limit_bytes,g.egress_limit_bytes,g.operation_metric,g.operation_limit FROM billing_purchases p JOIN product_grants g ON g.purchase_id=p.id WHERE p.status='paid' AND g.source='purchase';`); err != nil {
		return fmt.Errorf("migrate legacy TPA grants: %w", err)
	}
	if _, err = exec.Exec(`INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT e.account_id,'tpa-hub','repository.create',e.plan_code,1,e.valid_from,e.valid_until,CASE WHEN e.plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,0,'purchase',e.purchase_id,e.created_at,e.updated_at FROM account_entitlements e JOIN billing_purchases p ON p.id=e.purchase_id AND p.account_id=e.account_id AND p.product_id='tpa-hub' AND p.status='paid' WHERE e.source='purchase';
	INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT e.account_id,'tpa-hub','repository.publish',e.plan_code,1,e.valid_from,e.valid_until,CASE WHEN e.plan_code='nano' THEN 'annual' ELSE 'monthly' END,CASE e.plan_code WHEN 'nano' THEN 5000000 WHEN 'standard' THEN 500000000 ELSE 9223372036854775807 END,0,'purchase',e.purchase_id,e.created_at,e.updated_at FROM account_entitlements e JOIN billing_purchases p ON p.id=e.purchase_id AND p.account_id=e.account_id AND p.product_id='tpa-hub' AND p.status='paid' WHERE e.source='purchase';
	INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT e.account_id,'tpa-hub','repository.download',e.plan_code,1,e.valid_from,e.valid_until,CASE WHEN e.plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,CASE e.plan_code WHEN 'nano' THEN 12000000000 WHEN 'standard' THEN 20000000000 ELSE 9223372036854775807 END,'purchase',e.purchase_id,e.created_at,e.updated_at FROM account_entitlements e JOIN billing_purchases p ON p.id=e.purchase_id AND p.account_id=e.account_id AND p.product_id='tpa-hub' AND p.status='paid' WHERE e.source='purchase';
	INSERT OR IGNORE INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,source,purchase_id,created_at,updated_at) SELECT e.account_id,'tpa-hub','repository.read.private',e.plan_code,1,e.valid_from,e.valid_until,CASE WHEN e.plan_code='nano' THEN 'annual' ELSE 'monthly' END,0,0,'purchase',e.purchase_id,e.created_at,e.updated_at FROM account_entitlements e JOIN billing_purchases p ON p.id=e.purchase_id AND p.account_id=e.account_id AND p.product_id='tpa-hub' AND p.status='paid' WHERE e.source='purchase'`); err != nil {
		return fmt.Errorf("migrate verified purchase grants: %w", err)
	}
	if err = migrationCheckpoint("after-materialization-before-marker"); err != nil {
		return err
	}
	if _, err = exec.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES('commerce-product-grants-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		return fmt.Errorf("record legacy TPA migration: %w", err)
	}
	if err = migrationCheckpoint("before-commit"); err != nil {
		return err
	}
	return nil
}

func migrateHistoricalPurchaseGrantInstances(exec migrationExecutor) error {
	var err error
	var exists int
	if err = exec.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='historical-purchase-grant-instances-v1'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	if err = migrationCheckpoint("paid-instances-after-validation"); err != nil {
		return err
	}
	if _, err = exec.Exec(`DROP TRIGGER IF EXISTS purchase_grant_instances_no_unauthorized_insert`); err != nil {
		return fmt.Errorf("prepare historical paid grant migration: %w", err)
	}
	if _, err = exec.Exec(`INSERT OR IGNORE INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,created_at,updated_at)
		SELECT g.customer_id,g.product_id,g.capability_id,g.purchase_id,g.offer_id,g.enabled,g.valid_from,g.valid_until,g.period_cadence,g.storage_limit_bytes,g.egress_limit_bytes,g.operation_metric,g.operation_limit,g.created_at,g.updated_at
		FROM product_grants g JOIN billing_purchases p ON p.id=g.purchase_id AND p.account_id=g.customer_id AND p.product_id=g.product_id AND p.status='paid' WHERE g.source='purchase'`); err != nil {
		return fmt.Errorf("materialize historical paid grants: %w", err)
	}
	if err = migrationCheckpoint("paid-instances-after-copy"); err != nil {
		return err
	}
	var mismatches int
	if err = exec.QueryRow(`SELECT COUNT(*) FROM product_grants g LEFT JOIN purchase_grant_instances i ON i.purchase_id=g.purchase_id AND i.capability_id=g.capability_id WHERE g.source='purchase' AND (i.purchase_id IS NULL OR i.customer_id<>g.customer_id OR i.product_id<>g.product_id OR i.offer_id<>g.offer_id OR i.enabled<>g.enabled OR i.valid_from<>g.valid_from OR i.valid_until<>g.valid_until OR i.period_cadence<>g.period_cadence OR i.storage_limit_bytes<>g.storage_limit_bytes OR i.egress_limit_bytes<>g.egress_limit_bytes OR i.operation_metric<>g.operation_metric OR i.operation_limit<>g.operation_limit OR i.created_at<>g.created_at OR i.updated_at<>g.updated_at)`).Scan(&mismatches); err != nil {
		return fmt.Errorf("verify historical paid grants: %w", err)
	}
	if mismatches != 0 {
		return fmt.Errorf("historical paid grant instance conflicts with source")
	}
	if _, err = exec.Exec(`DELETE FROM product_grants WHERE source='purchase'`); err != nil {
		return fmt.Errorf("remove historical paid compatibility grants: %w", err)
	}
	if err = migrationCheckpoint("paid-instances-after-delete"); err != nil {
		return err
	}
	if _, err = exec.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES('historical-purchase-grant-instances-v1',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		return err
	}
	if _, err = exec.Exec(`CREATE TRIGGER purchase_grant_instances_no_unauthorized_insert BEFORE INSERT ON purchase_grant_instances BEGIN SELECT CASE WHEN EXISTS(SELECT 1 FROM billing_purchases p WHERE p.id=NEW.purchase_id AND p.status='paid') THEN RAISE(ABORT,'purchase grant instances are immutable after activation') END; END;`); err != nil {
		return fmt.Errorf("restore purchase grant insert integrity: %w", err)
	}
	if err = migrationCheckpoint("paid-instances-before-commit"); err != nil {
		return err
	}
	return nil
}

func migrateLegacyAccessToSixMonthNano(exec migrationExecutor) error {
	var err error
	var exists int
	if err = exec.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='legacy-six-month-nano-v1'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	anchor := migrationNow().UTC()
	from, until := anchor.Format(time.RFC3339Nano), anchor.AddDate(0, 6, 0).Format(time.RFC3339Nano)
	rows, err := exec.Query(`SELECT account_id,valid_from,valid_until FROM account_entitlements WHERE source='legacy' UNION SELECT customer_id,valid_from,valid_until FROM product_grants WHERE source='legacy' AND product_id=? AND enabled=1`, string(tpahubproduct.ProductID))
	if err != nil {
		return err
	}
	var accounts []string
	seenAccounts := make(map[string]struct{})
	for rows.Next() {
		var id, validFrom, validUntil string
		if err = rows.Scan(&id, &validFrom, &validUntil); err != nil {
			rows.Close()
			return err
		}
		start, startErr := parseEntitlementTime(validFrom)
		if startErr != nil || start.After(anchor) {
			continue
		}
		if validUntil != "" {
			end, endErr := parseEntitlementTime(validUntil)
			if endErr != nil || !anchor.Before(end) {
				continue
			}
		}
		if _, seen := seenAccounts[id]; !seen {
			seenAccounts[id] = struct{}{}
			accounts = append(accounts, id)
		}
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
		if _, err = exec.Exec(`DELETE FROM product_grants WHERE customer_id=? AND product_id=? AND source='legacy'`, accountID, string(offer.ProductID)); err != nil {
			return err
		}
		if _, err = exec.Exec(`UPDATE account_entitlements SET plan_code='nano',started_at=?,valid_from=?,valid_until=?,source='migration',purchase_id='',updated_at=? WHERE account_id=? AND source='legacy'`, from, from, until, from, accountID); err != nil {
			return err
		}
		for _, template := range offer.Grants {
			if _, err = exec.Exec(`INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?,?,?,?,'migration','',?,?) ON CONFLICT(customer_id,product_id,capability_id) DO UPDATE SET offer_id=excluded.offer_id,enabled=1,valid_from=excluded.valid_from,valid_until=excluded.valid_until,period_cadence=excluded.period_cadence,storage_limit_bytes=excluded.storage_limit_bytes,egress_limit_bytes=excluded.egress_limit_bytes,operation_metric=excluded.operation_metric,operation_limit=excluded.operation_limit,source='migration',purchase_id='',updated_at=excluded.updated_at`, accountID, offer.ProductID, template.CapabilityID, offer.ID, from, until, template.Period, template.Limits.StorageBytes, template.Limits.EgressBytes, template.Limits.OperationMetric, template.Limits.OperationCount, from, from); err != nil {
				return err
			}
		}
		if err = migrationCheckpoint("bounded-after-account"); err != nil {
			return err
		}
	}
	if _, err = exec.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES('legacy-six-month-nano-v1',?)`, from); err != nil {
		return err
	}
	if err = migrationCheckpoint("bounded-before-commit"); err != nil {
		return err
	}
	return nil
}

func rebuildCompatibilityProjection(exec migrationExecutor) error {
	rows, err := exec.Query(`SELECT p.account_id,p.plan_code,p.entitlement_period_start,p.entitlement_period_end,p.paid_at,p.id FROM billing_purchases p WHERE p.product_id=? AND p.status='paid' AND p.paid_at=(SELECT MAX(p2.paid_at) FROM billing_purchases p2 WHERE p2.account_id=p.account_id AND p2.product_id=? AND p2.status='paid')`, string(tpahubproduct.ProductID), string(tpahubproduct.ProductID))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, plan, from, until, paidAt, purchaseID string
		if err = rows.Scan(&accountID, &plan, &from, &until, &paidAt, &purchaseID); err != nil {
			return err
		}
		if _, err = exec.Exec(`INSERT INTO account_entitlements(account_id,plan_code,started_at,valid_from,valid_until,created_at,updated_at,source,purchase_id) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET plan_code=excluded.plan_code,started_at=excluded.started_at,valid_from=excluded.valid_from,valid_until=excluded.valid_until,updated_at=excluded.updated_at,source='purchase',purchase_id=excluded.purchase_id`, accountID, plan, from, from, until, paidAt, paidAt, "purchase", purchaseID); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return nil
}

func migrateProductScopedKeys(exec migrationExecutor) error {
	var err error
	var submissionsSQL, eventsSQL string
	if err = exec.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='billing_provider_submissions'`).Scan(&submissionsSQL); err != nil {
		return err
	}
	if !strings.Contains(strings.ReplaceAll(submissionsSQL, " ", ""), "PRIMARYKEY(account_id,product_id,period,provider,environment)") {
		if _, err = exec.Exec(`CREATE TABLE billing_provider_submissions_new(account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,product_id,period,provider,environment)); INSERT INTO billing_provider_submissions_new SELECT account_id,product_id,period,provider,environment,finalization_id,status,provider_reference,attempted_at,completed_at,last_error FROM billing_provider_submissions; DROP TABLE billing_provider_submissions; ALTER TABLE billing_provider_submissions_new RENAME TO billing_provider_submissions;`); err != nil {
			return fmt.Errorf("migrate provider submissions: %w", err)
		}
	}
	if err = exec.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='mock_billing_events'`).Scan(&eventsSQL); err != nil {
		return err
	}
	normalizedEventsSQL := strings.ReplaceAll(eventsSQL, " ", "")
	if !strings.Contains(normalizedEventsSQL, "PRIMARYKEY(product_id,id)") || !strings.Contains(normalizedEventsSQL, "UNIQUE(account_id,product_id,period,event_type)") {
		if _, err = exec.Exec(`CREATE TABLE mock_billing_events_new(id TEXT NOT NULL,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(product_id,id),UNIQUE(account_id,product_id,period,event_type)); INSERT INTO mock_billing_events_new SELECT id,account_id,product_id,period,event_type,payload,created_at FROM mock_billing_events; DROP TABLE mock_billing_events; ALTER TABLE mock_billing_events_new RENAME TO mock_billing_events;`); err != nil {
			return fmt.Errorf("migrate mock billing events: %w", err)
		}
	}
	return nil
}

func enforceUniquePurchaseProviderReferences(exec migrationExecutor) error {
	var err error
	var provider, environment, reference string
	var count int
	err = exec.QueryRow(`SELECT provider,billing_environment,provider_reference,COUNT(*) FROM billing_purchases WHERE provider_reference IS NOT NULL AND provider_reference<>'' GROUP BY provider,billing_environment,provider_reference HAVING COUNT(*)>1 ORDER BY provider,billing_environment,provider_reference LIMIT 1`).Scan(&provider, &environment, &reference, &count)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("inspect billing purchase provider references: %w", err)
	}
	if err == nil {
		return fmt.Errorf("duplicate billing purchase provider reference: provider=%q environment=%q reference=%q count=%d", provider, environment, reference, count)
	}
	if _, err = exec.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS billing_purchases_provider_reference ON billing_purchases(provider,billing_environment,provider_reference) WHERE provider_reference IS NOT NULL AND provider_reference<>''`); err != nil {
		return fmt.Errorf("enforce billing purchase provider reference uniqueness: %w", err)
	}
	return nil
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

type AccountEntitlement struct{ AccountID, PlanCode, StartedAt, ValidFrom, ValidUntil, CreatedAt, UpdatedAt, Source, PurchaseID string }

func parseEntitlementTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
	}
	return parsed, err
}

func validHistoricalWindow(fromText, untilText string) bool {
	if fromText == "" {
		return false
	}
	from, err := parseEntitlementTime(fromText)
	if err != nil {
		return false
	}
	if untilText == "" {
		return true
	}
	until, err := parseEntitlementTime(untilText)
	return err == nil && until.After(from)
}

func knownHistoricalOffer(offer string) bool {
	switch offer {
	case "legacy", "nano", "standard", "pro":
		return true
	default:
		return false
	}
}

func knownGrantSource(source string) bool {
	switch source {
	case "legacy", "migration", "local", "purchase", "test":
		return true
	default:
		return false
	}
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

// SetLegacyTPAEntitlement persists the legacy TPA compatibility projection.
// Current product-aware callers must use explicit product-scoped APIs instead.
func (s *Store) SetLegacyTPAEntitlement(c context.Context, x AccountEntitlement) error {
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
	// Deliberately do not synchronize product_grants from this row. This
	// compatibility API records historical data only; it is not a reverse
	// projection and cannot alter current entitlement.
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

// GetGrant is the canonical current entitlement resolution path (I-003):
// authoritative product grants and paid grant instances are validated and
// aggregated here; account_entitlements is never consulted.
func (s *Store) GetGrant(c context.Context, customer commerce.CustomerID, product commerce.ProductID, capability commerce.CapabilityID, at time.Time) (commerce.Grant, error) {
	const columns = `customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at`
	var contributors []commerce.Grant
	seen, enabledSeen := false, false
	base, err := scanGrant(s.db.QueryRowContext(c, `SELECT `+columns+` FROM product_grants WHERE customer_id=? AND product_id=? AND capability_id=?`, customer, product, capability))
	if err == nil {
		if !knownGrantSource(base.Source) {
			// Unknown provenance is retained for forensics but is never an
			// authorization contributor.
			err = sql.ErrNoRows
		} else {
			// Permanent historical legacy rows are audit-only after the bounded
			// migration. They never participate in runtime authorization.
			if base.Source == "legacy" && base.ProductID == tpahubproduct.ProductID {
				base.Enabled = false
			}
			seen = true
			if base.Enabled {
				enabledSeen = true
				if base.ActiveAt(at) {
					contributors = append(contributors, base)
				}
			}
		}
	} else if err != sql.ErrNoRows {
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
	_, err := s.db.ExecContext(c, `INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,valid_until,period_cadence,storage_limit_bytes,egress_limit_bytes,operation_metric,operation_limit,source,purchase_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(customer_id,product_id,capability_id) DO UPDATE SET offer_id=excluded.offer_id,enabled=excluded.enabled,valid_from=excluded.valid_from,valid_until=excluded.valid_until,period_cadence=excluded.period_cadence,storage_limit_bytes=excluded.storage_limit_bytes,egress_limit_bytes=excluded.egress_limit_bytes,operation_metric=excluded.operation_metric,operation_limit=excluded.operation_limit,source=excluded.source,purchase_id=excluded.purchase_id,updated_at=excluded.updated_at`, grant.CustomerID, grant.ProductID, grant.CapabilityID, grant.OfferID, grant.Enabled, grant.ValidFrom.UTC().Format(time.RFC3339Nano), validUntil, grant.Period, grant.Limits.StorageBytes, grant.Limits.EgressBytes, grant.Limits.OperationMetric, grant.Limits.OperationCount, grant.Source, grant.PurchaseID, grant.CreatedAt.UTC().Format(time.RFC3339Nano), grant.UpdatedAt.UTC().Format(time.RFC3339Nano))
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
		if purchase.OfferVersion == "legacy-unversioned" && commerce.ProductID(purchase.ProductID) == tpahubproduct.ProductID && !tpahubproduct.ValidCapability(template.CapabilityID) {
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
		p.OfferVersion = "legacy-unversioned"
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
	// Purchase admission is product/capability scoped. The account-wide
	// account_entitlements compatibility projection is migration history, not
	// current commercial authority.
	grantRows, grantErr := tx.QueryContext(c, `SELECT enabled,valid_from,valid_until FROM product_grants WHERE customer_id=? AND product_id=? AND source='purchase' UNION ALL SELECT enabled,valid_from,valid_until FROM purchase_grant_instances WHERE customer_id=? AND product_id=?`, p.AccountID, p.ProductID, p.AccountID, p.ProductID)
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
		// account_entitlements is migration-only compatibility history. Payment
		// activation writes authoritative product grants and immutable purchase
		// evidence; it does not maintain a reverse runtime projection.
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
	// Retain the compatibility row for legacy database conversion and
	// historical diagnostics. It is never consulted for current policy.
	for _, grant := range grants {
		if grant.ProductID != tpahubproduct.ProductID {
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
