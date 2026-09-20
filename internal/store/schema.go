package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// schemaSQL is the sole definition of the first supported persistent schema.
// There is deliberately no migration from disposable pre-release schemas.
//
//go:embed schema.sql
var schemaSQL string

func normalizedDDL(s string) string {
	// Preserve token boundaries: removing whitespace must not confuse a column
	// named idTEXT with the declaration id TEXT. SQL literals remain exact.
	r := []rune(s)
	var tokens []string
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		if i+1 < len(r) && r[i] == '-' && r[i+1] == '-' {
			for i < len(r) && r[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(r) && r[i] == '/' && r[i+1] == '*' {
			i += 2
			for i+1 < len(r) && !(r[i] == '*' && r[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		if r[i] == '\'' || r[i] == '"' || r[i] == '`' || r[i] == '[' {
			quote := r[i]
			end := quote
			if quote == '[' {
				end = ']'
			}
			start := i
			i++
			var value strings.Builder
			for i < len(r) {
				if r[i] == end {
					if i+1 < len(r) && r[i+1] == end && quote != '[' {
						value.WriteRune(end)
						i += 2
						continue
					}
					i++
					break
				}
				value.WriteRune(r[i])
				i++
			}
			if quote == '\'' {
				tokens = append(tokens, string(r[start:i]))
			} else {
				tokens = append(tokens, strings.ToLower(value.String()))
			}
			continue
		}
		start := i
		i++
		if unicode.IsLetter(r[start]) || unicode.IsDigit(r[start]) || r[start] == '_' {
			for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_' || r[i] == '$') {
				i++
			}
		}
		tokens = append(tokens, strings.ToLower(string(r[start:i])))
	}
	return strings.Join(tokens, "\x00")
}

func schemaDefinitions(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	var tables []string
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			return nil, err
		}
		result[kind+":"+name] = normalizedDDL(ddl)
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Introspection independently checks actual parsed structure, including
	// implicit indexes. Normalized SQL alone is not a substitute for this.
	// Batch introspection across tables, rather than preparing three queries
	// per table on every open. No instance result is cached or check omitted.
	for kind, query := range map[string]string{
		"columns":      `SELECT m.name,p.cid,p.name,upper(p.type),p."notnull",p.dflt_value,p.pk,p.hidden FROM sqlite_master m JOIN pragma_table_xinfo(m.name) p WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name,p.cid`,
		"foreign-keys": `SELECT m.name,p."table",p."from",p."to",p.on_update,p.on_delete,p.match FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) p WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name,p.id,p.seq`,
		"indexes":      `SELECT m.name,p.name,p."unique",p.origin,p.partial FROM sqlite_master m JOIN pragma_index_list(m.name) p WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name,p.name`,
	} {
		values, err := schemaQueryShape(db, query)
		if err != nil {
			return nil, err
		}
		for _, table := range tables {
			value, ok := values[table]
			if !ok {
				value = "null"
			}
			result[kind+":"+table] = value
		}
	}
	return result, nil
}

func schemaQueryShape(db *sql.DB, query string) (map[string]string, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := map[string][][]any{}
	for rows.Next() {
		var table string
		row := make([]any, len(columns)-1)
		dest := make([]any, len(columns))
		dest[0] = &table
		for i := range row {
			dest[i+1] = &row[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		values[table] = append(values[table], row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for table, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		result[table] = string(data)
	}
	return result, nil
}

// Cache only the immutable built-in reference, never an instance's shape.
// The embedded DDL remains the sole authority; this is process-local derivation.
var canonicalSchemaDefinitions = sync.OnceValues(func() (map[string]string, error) {
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer reference.Close()
	reference.SetMaxOpenConns(1)
	if _, err = reference.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("invalid built-in schema: %w", err)
	}
	return schemaDefinitions(reference)
})

// Validate every declared table, column, CHECK, FK, index and trigger against
// the canonical executable DDL. Every instance is freshly inspected read-only.
func validateCurrentSchema(db *sql.DB) error {
	return validateSchema(db, schemaSQL, "Schema 9")
}

func validateSchema(db *sql.DB, ddl, label string) error {
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	if _, err = reference.Exec(ddl); err != nil {
		return fmt.Errorf("invalid %s reference: %w", label, err)
	}
	want, err := schemaDefinitions(reference)
	if err != nil {
		return err
	}
	got, err := schemaDefinitions(db)
	if err != nil {
		return err
	}
	var names []string
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if got[name] != want[name] {
			return fmt.Errorf("missing or invalid %s structure %s; restore a valid backup", label, name)
		}
	}
	names = names[:0]
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := want[name]; !ok {
			return fmt.Errorf("unexpected %s structure %s", label, name)
		}
	}
	var singleton int
	if err := db.QueryRow(`SELECT singleton FROM tpahub_schema WHERE typeof(singleton)='integer'`).Scan(&singleton); err != nil || singleton != 1 {
		return fmt.Errorf("invalid schema authority singleton")
	}
	return nil
}

func migrateSchemaTo9(db *sql.DB) error {
	oldJobs := "CREATE TABLE publish_jobs(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed')),incoming_path TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT,cleanup_state TEXT NOT NULL DEFAULT 'complete' CHECK(cleanup_state IN ('complete','pending')),cleanup_error TEXT NOT NULL DEFAULT '',cleanup_attempts INTEGER NOT NULL DEFAULT 0,cleanup_next_retry TEXT NOT NULL DEFAULT '');"
	legacyDDL := schemaSQL
	start := strings.Index(legacyDDL, "CREATE TABLE publish_jobs(")
	end := -1
	if start >= 0 {
		end = strings.Index(legacyDDL[start:], ";")
	}
	if start >= 0 && end >= 0 {
		legacyDDL = legacyDDL[:start] + oldJobs + legacyDDL[start+end+1:]
	}
	legacyDDL = strings.Replace(legacyDDL, "\n\nCREATE TABLE publication_artifacts(job_id TEXT NOT NULL REFERENCES publish_jobs(id) ON DELETE CASCADE,filename TEXT NOT NULL,package TEXT NOT NULL,version TEXT NOT NULL,architecture TEXT NOT NULL,uploaded_size INTEGER NOT NULL CHECK(uploaded_size>=0),uploaded_sha256 TEXT NOT NULL CHECK(length(uploaded_sha256)=64),published_filename TEXT NOT NULL DEFAULT '',published_size INTEGER NOT NULL DEFAULT 0 CHECK(published_size>=0),published_sha256 TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),PRIMARY KEY(job_id,filename));", "", 1)
	legacyDDL = strings.Replace(legacyDDL, ",generation_id TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),verified_at TEXT NOT NULL DEFAULT '',verification_error TEXT NOT NULL DEFAULT ''", "", 1)
	if err := validateSchema(db, legacyDDL, "Schema 8"); err != nil {
		return fmt.Errorf("Schema 8 validation failed: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin Schema 8 to 9 migration: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`ALTER TABLE publish_jobs ADD COLUMN generation_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE publish_jobs ADD COLUMN verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch'))`,
		`ALTER TABLE publish_jobs ADD COLUMN verified_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE publish_jobs ADD COLUMN verification_error TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE publication_artifacts(job_id TEXT NOT NULL REFERENCES publish_jobs(id) ON DELETE CASCADE,filename TEXT NOT NULL,package TEXT NOT NULL,version TEXT NOT NULL,architecture TEXT NOT NULL,uploaded_size INTEGER NOT NULL CHECK(uploaded_size>=0),uploaded_sha256 TEXT NOT NULL CHECK(length(uploaded_sha256)=64),published_filename TEXT NOT NULL DEFAULT '',published_size INTEGER NOT NULL DEFAULT 0 CHECK(published_size>=0),published_sha256 TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),PRIMARY KEY(job_id,filename))`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("add publication identity field: %w", err)
		}
	}
	if err = migrationCheckpoint("before-schema-9-version"); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE tpahub_schema SET version=9 WHERE singleton=1`); err != nil {
		return fmt.Errorf("set Schema 9 version: %w", err)
	}
	return tx.Commit()
}

func migrateSchemaTo8(db *sql.DB) error {
	oldAccount := "CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('active','suspended')),created_at TEXT NOT NULL);"
	legacyDDL := strings.Replace(schemaSQL, "CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('active','suspended')),created_at TEXT NOT NULL,membership_expires_at TEXT DEFAULT NULL,purge_after TEXT DEFAULT NULL,lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal')));", oldAccount, 1)
	legacyDDL = strings.Replace(legacyDDL, ",generation_id TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),verified_at TEXT NOT NULL DEFAULT '',verification_error TEXT NOT NULL DEFAULT ''", "", 1)
	legacyDDL = strings.Replace(legacyDDL, "\n\nCREATE TABLE publication_artifacts(job_id TEXT NOT NULL REFERENCES publish_jobs(id) ON DELETE CASCADE,filename TEXT NOT NULL,package TEXT NOT NULL,version TEXT NOT NULL,architecture TEXT NOT NULL,uploaded_size INTEGER NOT NULL CHECK(uploaded_size>=0),uploaded_sha256 TEXT NOT NULL CHECK(length(uploaded_sha256)=64),published_filename TEXT NOT NULL DEFAULT '',published_size INTEGER NOT NULL DEFAULT 0 CHECK(published_size>=0),published_sha256 TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),PRIMARY KEY(job_id,filename));", "", 1)
	if err := validateSchema(db, legacyDDL, "Schema 7"); err != nil {
		return fmt.Errorf("Schema 7 validation failed: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin Schema 7 to 8 migration: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`ALTER TABLE accounts ADD COLUMN membership_expires_at TEXT DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN purge_after TEXT DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal'))`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("add account lifecycle field: %w", err)
		}
	}
	if _, err = tx.Exec(`UPDATE accounts SET lifecycle_kind='internal' WHERE id IN (SELECT account_id FROM global_bootstrap_repository)`); err != nil {
		return fmt.Errorf("classify bootstrap account: %w", err)
	}
	if err = migrationCheckpoint("before-schema-8-version"); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE tpahub_schema SET version=8 WHERE singleton=1`); err != nil {
		return fmt.Errorf("set Schema 8 version: %w", err)
	}
	return tx.Commit()
}

func migrateSchemaTo7(db *sql.DB, version int) error {
	finalCred := "CREATE TABLE account_ssh_credentials(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,credential_type TEXT NOT NULL CHECK(credential_type='ssh'),public_key BLOB NOT NULL,fingerprint TEXT NOT NULL UNIQUE,label TEXT NOT NULL DEFAULT '',flags INTEGER NOT NULL DEFAULT 0 CHECK(flags>=0),created_at TEXT NOT NULL,revoked_at TEXT NOT NULL DEFAULT '');\n\n"
	finalAudit := "CREATE TABLE account_ssh_credential_audit(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),credential_id TEXT NOT NULL,old_flags INTEGER NOT NULL CHECK(old_flags>=0),new_flags INTEGER NOT NULL CHECK(new_flags>=0),action TEXT NOT NULL,created_at TEXT NOT NULL);\n\n"
	finalSessions := "CREATE TABLE sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT,credential_id TEXT NOT NULL DEFAULT '');"
	oldCred := "CREATE TABLE account_ssh_credentials(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,credential_type TEXT NOT NULL CHECK(credential_type='ssh'),public_key BLOB NOT NULL,fingerprint TEXT NOT NULL UNIQUE,label TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,revoked_at TEXT NOT NULL DEFAULT '');\n\n"
	oldSessions := "CREATE TABLE sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT);"
	legacyDDL := strings.Replace(schemaSQL, finalCred+finalAudit, oldCred, 1)
	legacyDDL = strings.Replace(legacyDDL, finalSessions, oldSessions, 1)
	legacyDDL = strings.Replace(legacyDDL, ",membership_expires_at TEXT DEFAULT NULL,purge_after TEXT DEFAULT NULL,lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal'))", "", 1)
	if version == 5 {
		legacyDDL = strings.Replace(legacyDDL, oldCred, "", 1)
	}
	if err := validateSchema(db, legacyDDL, fmt.Sprintf("Schema %d", version)); err != nil {
		return fmt.Errorf("Schema %d validation failed: %w", version, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin Schema %d to 7 migration: %w", version, err)
	}
	defer tx.Rollback()
	if version == 5 {
		if _, err = tx.Exec(finalCred); err != nil {
			return fmt.Errorf("create SSH credential table: %w", err)
		}
	} else {
		if _, err = tx.Exec(`ALTER TABLE account_ssh_credentials RENAME TO account_ssh_credentials_legacy`); err != nil {
			return fmt.Errorf("stage SSH credential table: %w", err)
		}
		if _, err = tx.Exec(finalCred); err != nil {
			return fmt.Errorf("create canonical SSH credential table: %w", err)
		}
		if _, err = tx.Exec(`INSERT INTO account_ssh_credentials(id,account_id,credential_type,public_key,fingerprint,label,flags,created_at,revoked_at) SELECT id,account_id,credential_type,public_key,fingerprint,label,0,created_at,revoked_at FROM account_ssh_credentials_legacy`); err != nil {
			return fmt.Errorf("preserve SSH credentials: %w", err)
		}
		if _, err = tx.Exec(`DROP TABLE account_ssh_credentials_legacy`); err != nil {
			return fmt.Errorf("remove staged SSH credential table: %w", err)
		}
	}
	if _, err = tx.Exec(`CREATE TABLE account_ssh_credential_audit(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),credential_id TEXT NOT NULL,old_flags INTEGER NOT NULL CHECK(old_flags>=0),new_flags INTEGER NOT NULL CHECK(new_flags>=0),action TEXT NOT NULL,created_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create SSH credential audit table: %w", err)
	}
	if _, err = tx.Exec(`ALTER TABLE sessions ADD COLUMN credential_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add session credential identity: %w", err)
	}
	if err = migrationCheckpoint("before-schema-7-version"); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE tpahub_schema SET version=7 WHERE singleton=1`); err != nil {
		return fmt.Errorf("set Schema 7 version: %w", err)
	}
	return tx.Commit()
}

func open(dir string, reconcileRunning, explicitMigration bool) (result *Store, err error) {
	path := filepath.Join(dir, "tpahub.db")
	info, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	fresh := os.IsNotExist(statErr) || (statErr == nil && info.Size() == 0)
	if fresh {
		if explicitMigration {
			return nil, fmt.Errorf("migration requires an initialized supported database")
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	readOnly := !fresh && !explicitMigration
	dsn := path
	if readOnly {
		absolute, e := filepath.Abs(path)
		if e != nil {
			return nil, e
		}
		uri := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
		dsn = uri.String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if err != nil && db != nil {
			db.Close()
		}
	}()
	if !fresh {
		var objects int
		if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
			return nil, fmt.Errorf("invalid database: %w", err)
		}
		fresh = objects == 0 && !explicitMigration
	}
	if !fresh {
		var version int
		if err = db.QueryRow(`SELECT version FROM tpahub_schema WHERE typeof(version)='integer' AND version>0 AND (SELECT COUNT(*) FROM tpahub_schema)=1`).Scan(&version); err != nil {
			return nil, fmt.Errorf("invalid or missing schema metadata; restore a backup or explicitly reinitialize disposable state: %w", err)
		}
		if version < 5 {
			return nil, fmt.Errorf("unsupported pre-release database schema %d: Schemas 1–4 are development-only; stop Hub, back up anything wanted, and explicitly reinitialize state", version)
		}
		if version > currentSchemaVersion {
			return nil, fmt.Errorf("database schema %d is newer than this binary (schema %d); use a compatible binary or restore its matching backup", version, currentSchemaVersion)
		}
		if version != currentSchemaVersion {
			if !explicitMigration || ((version != 5 && version != 6) && version != 7 && version != 8) {
				return nil, fmt.Errorf("schema %d requires an explicit supported forward migration to %d", version, currentSchemaVersion)
			}
			if version == 8 {
				if err = migrateSchemaTo9(db); err != nil {
					return nil, err
				}
			} else if version == 7 {
				if err = migrateSchemaTo8(db); err != nil {
					return nil, err
				}
				if err = migrateSchemaTo9(db); err != nil {
					return nil, err
				}
			} else if err = migrateSchemaTo7(db, version); err != nil {
				return nil, err
			}
		}
		if err = validateCurrentSchema(db); err != nil {
			return nil, err
		}
	}
	// A current migrate call keeps the read-only connection: even SQLite
	// recovery cannot turn this validation operation into an accidental write.
	if explicitMigration {
		return &Store{db}, nil
	}
	if readOnly {
		if err = db.Close(); err != nil {
			return nil, err
		}
		db, err = sql.Open("sqlite", path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
	}
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return nil, err
	}
	if fresh {
		tx, e := db.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, err = tx.Exec(schemaSQL); err != nil {
			return nil, fmt.Errorf("initialize Schema 7: %w", err)
		}
		if _, err = tx.Exec(`INSERT INTO tpahub_schema(singleton,version) VALUES(1,?)`, currentSchemaVersion); err != nil {
			return nil, err
		}
		if err = migrationCheckpoint("before-schema-commit"); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	if reconcileRunning {
		_, err = db.Exec(`UPDATE publish_jobs SET state='failed',error='interrupted by service restart',finished_at=?,cleanup_state='pending' WHERE state='running'`, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		_, err = db.Exec(`UPDATE egress_accounting_uncertain SET state='uncertain',version=version+1 WHERE state='streaming'`)
		if err != nil {
			return nil, err
		}
	}
	return &Store{db}, nil
}
