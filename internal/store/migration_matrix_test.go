package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// historicalFixture creates the tables and rows that existed before the
// commerce-product-grants migration. It deliberately does not call Open.
func historicalFixture(t *testing.T, mutate func(*sql.DB) error) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`PRAGMA foreign_keys=OFF;
CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL,password_hash TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE account_entitlements(account_id TEXT PRIMARY KEY,plan_code TEXT NOT NULL,started_at TEXT NOT NULL,valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,source TEXT NOT NULL,purchase_id TEXT);
CREATE TABLE billing_purchases(id TEXT PRIMARY KEY,account_id TEXT NOT NULL,product_id TEXT NOT NULL,plan_code TEXT NOT NULL,rate_card_id TEXT NOT NULL,rate_card_mode TEXT NOT NULL,tax_classification TEXT NOT NULL,currency TEXT NOT NULL,amount_minor INTEGER NOT NULL,billing_environment TEXT NOT NULL,provider TEXT NOT NULL,status TEXT NOT NULL,entitlement_period_start TEXT NOT NULL,entitlement_period_end TEXT NOT NULL,provider_reference TEXT NOT NULL,checkout_url TEXT NOT NULL,checkout_request_version TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,paid_at TEXT NOT NULL,failed_at TEXT NOT NULL,expired_at TEXT NOT NULL);
CREATE TABLE product_grants(customer_id TEXT NOT NULL,product_id TEXT NOT NULL,capability_id TEXT NOT NULL,offer_id TEXT NOT NULL,enabled INTEGER NOT NULL,valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,period_cadence TEXT NOT NULL,storage_limit_bytes INTEGER NOT NULL,egress_limit_bytes INTEGER NOT NULL,operation_metric TEXT NOT NULL,operation_limit INTEGER NOT NULL,source TEXT NOT NULL,purchase_id TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(customer_id,product_id,capability_id));
INSERT INTO accounts VALUES('customer-a','a@example.invalid','x','active','2025-01-01T00:00:00Z'),('customer-b','b@example.invalid','x','active','2025-01-01T00:00:00Z');
INSERT INTO billing_purchases VALUES('purchase-a','customer-a','tpa-hub','nano','rate','test','saas-personal','EUR',1,'test','stripe','paid','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','provider-a','','v1','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z','','');
INSERT INTO account_entitlements VALUES('customer-a','nano','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z','purchase','purchase-a');
INSERT INTO product_grants VALUES('customer-a','tpa-hub','repository.publish','nano',1,'2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','annual',5,6,'ops',7,'purchase','purchase-a','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		if err = mutate(db); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func migrationState(t *testing.T, dir string) (instances, grants, projections, markers int) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, out := range map[string]*int{
		"SELECT COUNT(*) FROM purchase_grant_instances":                                  &instances,
		"SELECT COUNT(*) FROM product_grants":                                            &grants,
		"SELECT COUNT(*) FROM account_entitlements":                                      &projections,
		"SELECT COUNT(*) FROM schema_migrations WHERE name='commerce-product-grants-v1'": &markers,
	} {
		if err := db.QueryRow(q).Scan(out); err != nil {
			// A fully atomic failed migration restores genuinely legacy databases,
			// where these later-baseline tables do not yet exist.
			if strings.Contains(err.Error(), "no such table") {
				*out = 0
				continue
			}
			t.Fatal(err)
		}
	}
	return
}

func TestMalformedHistoricalMigrationMatrixFailsClosedAndRepeats(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sql.DB) error
	}{
		{"unknown plan", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE account_entitlements SET plan_code='mystery',source='legacy',purchase_id=''`)
			return e
		}},
		{"unknown source", func(db *sql.DB) error { _, e := db.Exec(`UPDATE account_entitlements SET source='alien'`); return e }},
		{"missing purchase id", func(db *sql.DB) error { _, e := db.Exec(`UPDATE account_entitlements SET purchase_id=NULL`); return e }},
		{"empty purchase id", func(db *sql.DB) error { _, e := db.Exec(`UPDATE account_entitlements SET purchase_id=''`); return e }},
		{"orphan purchase", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE account_entitlements SET purchase_id='missing'`)
			return e
		}},
		{"unpaid purchase", func(db *sql.DB) error { _, e := db.Exec(`UPDATE billing_purchases SET status='pending'`); return e }},
		{"failed purchase", func(db *sql.DB) error { _, e := db.Exec(`UPDATE billing_purchases SET status='failed'`); return e }},
		{"expired purchase", func(db *sql.DB) error { _, e := db.Exec(`UPDATE billing_purchases SET status='expired'`); return e }},
		{"wrong customer", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE billing_purchases SET account_id='customer-b'`)
			return e
		}},
		{"wrong product", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE billing_purchases SET product_id='other-product'`)
			return e
		}},
		{"purchase entitlement wrong plan", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE account_entitlements SET plan_code='pro'`)
			return e
		}},
		{"wrong grant product", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET product_id='other-product'`)
			return e
		}},
		{"wrong grant offer", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET offer_id='other-offer'`)
			return e
		}},
		{"grant starts before purchase", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET valid_from='2025-12-31T23:59:59Z'`)
			return e
		}},
		{"grant ends after purchase", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET valid_until='2027-01-01T00:00:01Z'`)
			return e
		}},
		{"missing valid_from", func(db *sql.DB) error { _, e := db.Exec(`UPDATE product_grants SET valid_from=''`); return e }},
		{"missing valid_until", func(db *sql.DB) error { _, e := db.Exec(`UPDATE product_grants SET valid_until=''`); return e }},
		{"malformed valid_from", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET valid_from='yesterday'`)
			return e
		}},
		{"malformed valid_until", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET valid_until='tomorrow'`)
			return e
		}},
		{"equal validity", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET valid_until=valid_from`)
			return e
		}},
		{"reverse validity", func(db *sql.DB) error {
			_, e := db.Exec(`UPDATE product_grants SET valid_until='2025-01-01T00:00:00Z'`)
			return e
		}},
		{"open ended purchase", func(db *sql.DB) error { _, e := db.Exec(`UPDATE product_grants SET valid_until=''`); return e }},
		{"empty customer", func(db *sql.DB) error { _, e := db.Exec(`UPDATE account_entitlements SET account_id=''`); return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := historicalFixture(t, tc.mutate)
			for attempt := 0; attempt < 2; attempt++ {
				if s, err := openMigrated(dir); err == nil {
					s.Close()
					i, g, p, m := migrationState(t, dir)
					t.Fatalf("invalid historical source accepted: instances=%d grants=%d projections=%d markers=%d", i, g, p, m)
				}
				i, g, p, m := migrationState(t, dir)
				if i != 0 || g != 1 || p != 1 || m != 0 {
					t.Fatalf("attempt %d partial state instances=%d grants=%d projections=%d markers=%d", attempt, i, g, p, m)
				}
			}
		})
	}
}

func TestMalformedHistoricalMigrationCanBeRepairedAfterFailure(t *testing.T) {
	dir := historicalFixture(t, func(db *sql.DB) error {
		_, e := db.Exec(`UPDATE account_entitlements SET plan_code='unknown',source='legacy',purchase_id=''`)
		return e
	})
	if _, err := openMigrated(dir); err == nil {
		t.Fatal("invalid source accepted")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE account_entitlements SET plan_code='nano'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openMigrated(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err = s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM purchase_grant_instances WHERE customer_id='customer-a'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("repaired migration instances=%d err=%v", n, err)
	}
}

func TestMigrationInterruptionReopenMatrix(t *testing.T) {
	for _, stage := range []string{"after-schema-migration", "after-source-validation", "before-materialization", "after-materialization-before-marker", "before-commit"} {
		t.Run(stage, func(t *testing.T) {
			dir := historicalFixture(t, nil)
			migrationFault = func(found string) error {
				if found == stage {
					return errors.New("injected migration interruption")
				}
				return nil
			}
			if s, err := openMigrated(dir); err == nil {
				s.Close()
				t.Fatal("interrupted migration committed")
			}
			migrationFault = nil
			i, g, p, marker := migrationState(t, dir)
			if i != 0 || g != 1 || p != 1 || marker != 0 {
				t.Fatalf("partial state instances=%d grants=%d projections=%d marker=%d", i, g, p, marker)
			}
			s, err := openMigrated(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			i, g, p, marker = migrationState(t, dir)
			if i != 1 || g != 0 || p != 1 || marker != 1 {
				t.Fatalf("retry state instances=%d grants=%d projections=%d marker=%d", i, g, p, marker)
			}
			s, err = openMigrated(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			i2, g2, p2, marker2 := migrationState(t, dir)
			if i2 != i || g2 != g || p2 != p || marker2 != marker {
				t.Fatalf("non-idempotent reopen: %d/%d/%d/%d -> %d/%d/%d/%d", i, g, p, marker, i2, g2, p2, marker2)
			}
		})
	}
	migrationFault = nil
}

func TestHistoricalPaidGrantMaterializationInterruptionReopensCleanly(t *testing.T) {
	for _, stage := range []string{"paid-instances-after-validation", "paid-instances-after-copy", "paid-instances-after-delete", "paid-instances-before-commit"} {
		t.Run(stage, func(t *testing.T) {
			dir := historicalFixture(t, nil)
			migrationFault = func(found string) error {
				if found == stage {
					return errors.New("injected paid instance interruption")
				}
				return nil
			}
			if s, err := openMigrated(dir); err == nil {
				s.Close()
				t.Fatal("interrupted paid materialization committed")
			}
			migrationFault = nil
			i, g, p, marker := migrationState(t, dir)
			if i != 0 || g != 1 || p != 1 || marker != 0 {
				t.Fatalf("rolled back paid materialization state instances=%d grants=%d projections=%d marker=%d", i, g, p, marker)
			}
			s, err := openMigrated(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			i, g, p, marker = migrationState(t, dir)
			if i != 1 || g != 0 || p != 1 || marker != 1 {
				t.Fatalf("reopened paid materialization state instances=%d grants=%d projections=%d marker=%d", i, g, p, marker)
			}
		})
	}
	migrationFault = nil
}

func TestHistoricalPurchaseRelationshipsDoNotAttachNearestRow(t *testing.T) {
	dir := historicalFixture(t, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE account_entitlements SET account_id='customer-a',purchase_id='purchase-a'; UPDATE billing_purchases SET account_id='customer-b'`)
		return err
	})
	if _, err := openMigrated(dir); err == nil {
		t.Fatal("cross-account purchase accepted")
	}
}
