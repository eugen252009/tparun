package store

import (
	"fmt"
	"strings"
)

type schemaObject struct{ name, sql string }

// migrateStructuralConstraints adds only product-neutral, closed-domain
// constraints. It rebuilds tables from their existing definitions so every
// non-constraint column and row is copied byte-for-byte.
func migrateStructuralConstraints(exec migrationExecutor) error {
	inventory := []struct{ table, column, allowed string }{
		{"accounts", "status", "'active','suspended'"},
		{"repositories", "visibility", "'public','private'"},
		{"billing_purchases", "status", "'pending','superseded','paid','failed','expired','reconciliation_required'"},
		{"billing_provider_events", "status", "'processing','processed','reconciliation_required'"},
		{"product_grants", "period_cadence", "'annual','monthly','continuous'"},
		{"purchase_grant_snapshots", "period_cadence", "'annual','monthly','continuous'"},
		{"purchase_grant_instances", "period_cadence", "'annual','monthly','continuous'"},
		{"publish_jobs", "state", "'queued','running','succeeded','failed'"},
		{"publish_jobs", "cleanup_state", "'complete','pending'"},
		{"egress_accounting_uncertain", "state", "'streaming','uncertain'"},
	}
	for _, x := range inventory {
		var n int
		if err := exec.QueryRow("SELECT COUNT(*) FROM \"" + x.table + "\" WHERE \"" + x.column + "\" IS NULL OR \"" + x.column + "\" NOT IN (" + x.allowed + ")").Scan(&n); err != nil {
			return fmt.Errorf("inventory %s.%s: %w", x.table, x.column, err)
		}
		if n != 0 {
			return fmt.Errorf("invalid existing values in %s.%s: %d", x.table, x.column, n)
		}
	}
	var n int
	if err := exec.QueryRow("SELECT COUNT(*) FROM egress_accounting_uncertain WHERE reserved_bytes IS NULL OR reserved_bytes < 0").Scan(&n); err != nil {
		return fmt.Errorf("inventory egress reserved bytes: %w", err)
	}
	if n != 0 {
		return fmt.Errorf("invalid existing egress reserved bytes: %d", n)
	}
	// Fresh databases already contain these definitions; a v2-shaped fixture
	// may also carry them while its authority marker is being tested.
	allPresent := true
	for table, replacement := range map[string]string{
		"accounts":                    "status TEXT NOT NULL CHECK(status IN ('active','suspended'))",
		"repositories":                "visibility TEXT NOT NULL DEFAULT 'public' CHECK(visibility IN ('public','private'))",
		"billing_purchases":           "status TEXT NOT NULL CHECK(status IN ('pending','superseded','paid','failed','expired','reconciliation_required'))",
		"billing_provider_events":     "status TEXT NOT NULL CHECK(status IN ('processing','processed','reconciliation_required'))",
		"product_grants":              "period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous'))",
		"purchase_grant_snapshots":    "period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous'))",
		"purchase_grant_instances":    "period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous'))",
		"publish_jobs":                "state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed'))",
		"egress_accounting_uncertain": "state TEXT NOT NULL DEFAULT 'uncertain' CHECK(state IN ('streaming','uncertain'))",
	} {
		var definition string
		if err := exec.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil {
			return err
		}
		if !strings.Contains(definition, replacement) {
			allPresent = false
		}
	}
	var egressDefinition, jobsDefinition string
	if err := exec.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='egress_accounting_uncertain'").Scan(&egressDefinition); err != nil {
		return err
	}
	if err := exec.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='publish_jobs'").Scan(&jobsDefinition); err != nil {
		return err
	}
	allPresent = allPresent && strings.Contains(jobsDefinition, "cleanup_state TEXT NOT NULL DEFAULT 'complete' CHECK(cleanup_state IN ('complete','pending'))")
	if err := migrationCheckpoint("before-structural-constraints"); err != nil {
		return err
	}
	if allPresent && strings.Contains(egressDefinition, "reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0)") {
		return nil
	}
	var triggers []schemaObject
	rows, err := exec.Query("SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL")
	if err != nil {
		return fmt.Errorf("read triggers: %w", err)
	}
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, o)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, o := range triggers {
		if _, err := exec.Exec("DROP TRIGGER IF EXISTS \"" + o.name + "\""); err != nil {
			return fmt.Errorf("drop trigger %s: %w", o.name, err)
		}
	}
	checks := map[string]string{
		"accounts":                    "status TEXT NOT NULL CHECK(status IN ('active','suspended'))",
		"repositories":                "visibility TEXT NOT NULL DEFAULT 'public' CHECK(visibility IN ('public','private'))",
		"billing_purchases":           "status TEXT NOT NULL CHECK(status IN ('pending','superseded','paid','failed','expired','reconciliation_required'))",
		"billing_provider_events":     "status TEXT NOT NULL CHECK(status IN ('processing','processed','reconciliation_required'))",
		"product_grants":              "period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous'))",
		"purchase_grant_snapshots":    "period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous'))",
		"purchase_grant_instances":    "period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous'))",
		"publish_jobs":                "state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed'))",
		"egress_accounting_uncertain": "state TEXT NOT NULL DEFAULT 'uncertain' CHECK(state IN ('streaming','uncertain'))",
	}
	// cleanup_state and reserved_bytes are separate replacements because their
	// surrounding definitions are shared with unrelated columns.
	extras := map[string][]string{
		"publish_jobs":                {"cleanup_state TEXT NOT NULL DEFAULT 'complete' CHECK(cleanup_state IN ('complete','pending'))"},
		"egress_accounting_uncertain": {"reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0)"},
	}
	order := []string{"accounts", "repositories", "billing_purchases", "billing_provider_events", "product_grants", "purchase_grant_snapshots", "purchase_grant_instances", "publish_jobs", "egress_accounting_uncertain"}
	for _, table := range order {
		if err := migrationCheckpoint("before-structural-" + table); err != nil {
			return err
		}
		if err := rebuildConstrainedTable(exec, table, checks[table], extras[table]); err != nil {
			return err
		}
	}
	// Parent renames make SQLite rewrite child FK clauses to transient names.
	// Recreate every remaining application table to normalize those clauses.
	rows, err = exec.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('schema_migrations','tpahub_schema')")
	if err != nil {
		return err
	}
	var remaining []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return err
		}
		found := false
		for _, x := range order {
			if x == table {
				found = true
				break
			}
		}
		if !found {
			remaining = append(remaining, table)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, table := range remaining {
		if err := migrationCheckpoint("before-structural-" + table); err != nil {
			return err
		}
		if err := rebuildConstrainedTable(exec, table, "", nil); err != nil {
			return err
		}
	}
	for _, o := range triggers {
		if _, err := exec.Exec(o.sql); err != nil {
			return fmt.Errorf("restore trigger %s: %w", o.name, err)
		}
	}
	return nil
}

func rebuildConstrainedTable(exec migrationExecutor, table, replacement string, extra []string) error {
	var sql string
	if err := exec.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&sql); err != nil {
		return fmt.Errorf("read %s schema: %w", table, err)
	}
	if sql == "" {
		return fmt.Errorf("missing %s schema", table)
	}
	// Renaming a referenced table causes SQLite to rewrite child FOREIGN KEY
	// clauses. Normalize those transient names before recreating each table.
	sql = strings.ReplaceAll(sql, "__v3_old", "")
	for _, x := range append([]string{replacement}, extra...) {
		if x == "" {
			continue
		}
		parts := strings.Fields(x)
		if len(parts) < 2 {
			return fmt.Errorf("invalid replacement for %s", table)
		}
		column := parts[0]
		start := strings.Index(sql, column+" ")
		if start < 0 {
			return fmt.Errorf("%s column %s not found", table, column)
		}
		end := strings.Index(sql[start:], ",")
		if end < 0 {
			end = strings.Index(sql[start:], ")")
		}
		if end < 0 {
			return fmt.Errorf("%s column %s terminator not found", table, column)
		}
		sql = sql[:start] + x + sql[start+end:]
	}
	var objects []schemaObject
	rows, err := exec.Query("SELECT name,sql FROM sqlite_master WHERE (type='index' OR type='trigger') AND tbl_name=? AND sql IS NOT NULL", table)
	if err != nil {
		return fmt.Errorf("read %s objects: %w", table, err)
	}
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, o := range objects {
		if _, err := exec.Exec("DROP " + objectType(o.sql) + " IF EXISTS \"" + o.name + "\""); err != nil {
			return fmt.Errorf("drop %s: %w", o.name, err)
		}
	}
	old := table + "__v3_old"
	if _, err := exec.Exec("ALTER TABLE \"" + table + "\" RENAME TO \"" + old + "\""); err != nil {
		return fmt.Errorf("rename %s: %w", table, err)
	}
	// sqlite_master SQL normally starts with CREATE TABLE name (possibly
	// quoted); create a temp definition first, then restore the public name.
	create := strings.Replace(sql, "CREATE TABLE "+table, "CREATE TABLE "+table+"__v3_new", 1)
	create = strings.Replace(create, "CREATE TABLE \""+table+"\"", "CREATE TABLE \""+table+"__v3_new\"", 1)
	if _, err := exec.Exec(create); err != nil {
		return fmt.Errorf("create %s: %w", table, err)
	}
	if _, err := exec.Exec("INSERT INTO \"" + table + "__v3_new\" SELECT * FROM \"" + old + "\""); err != nil {
		return fmt.Errorf("copy %s: %w", table, err)
	}
	if _, err := exec.Exec("DROP TABLE \"" + old + "\""); err != nil {
		return fmt.Errorf("drop old %s: %w", table, err)
	}
	if _, err := exec.Exec("ALTER TABLE \"" + table + "__v3_new\" RENAME TO \"" + table + "\""); err != nil {
		return fmt.Errorf("restore %s name: %w", table, err)
	}
	for _, o := range objects {
		if _, err := exec.Exec(o.sql); err != nil {
			return fmt.Errorf("restore %s: %w", o.name, err)
		}
	}
	return nil
}

func objectType(sql string) string {
	if strings.HasPrefix(strings.TrimSpace(sql), "CREATE TRIGGER") {
		return "TRIGGER"
	}
	return "INDEX"
}
