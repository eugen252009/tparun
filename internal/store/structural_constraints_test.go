package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func structuralFixture(t *testing.T, dirs ...string) *Store {
	t.Helper()
	dir := t.TempDir()
	if len(dirs) != 0 {
		dir = dirs[0]
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "a", Email: "a@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "r", AccountID: "a", Name: "r", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "a", ProductID: "product", CapabilityID: "cap", OfferID: "offer", Enabled: true, ValidFrom: time.Now().UTC(), Period: commerce.PeriodMonthly, Source: "future-source", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	_, err = s.CreatePendingPurchase(ctx, BillingPurchase{ID: "p", AccountID: "a", ProductID: "product", PlanCode: "offer", RateCardID: "rate", RateCardMode: "test", TaxClassification: "generic", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "sandbox", Provider: "future-provider", Status: "pending", EntitlementPeriodStart: now, EntitlementPeriodEnd: until, CreatedAt: now, UpdatedAt: now}, now, commerce.GrantTemplate{CapabilityID: "cap", Period: commerce.PeriodMonthly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO billing_provider_events(provider,environment,provider_event_id,event_type,received_at,status) VALUES('future-provider','sandbox','e','future-event',?,'processing')`, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO publish_jobs(id,repository_id,state,incoming_path,error,created_at) VALUES('j','r','queued','x','',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO egress_accounting_uncertain(id,repository_id,created_at,state,reserved_bytes) VALUES('e','r',?,'streaming',0)`, now); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSchemaV3StructuralConstraints(t *testing.T) {
	s := structuralFixture(t)
	defer s.Close()
	cases := []struct{ name, table, column, value string }{
		{"accounts-status", "accounts", "status", "deleted"},
		{"repositories-visibility", "repositories", "visibility", "internal"},
		{"billing-purchases-status", "billing_purchases", "status", "refunded"},
		{"billing-provider-events-status", "billing_provider_events", "status", "unknown"},
		{"product-grants-cadence", "product_grants", "period_cadence", "fortnightly"},
		{"purchase-grant-snapshots-cadence", "purchase_grant_snapshots", "period_cadence", "fortnightly"},
		{"purchase-grant-instances-cadence", "purchase_grant_instances", "period_cadence", "fortnightly"},
		{"publish-jobs-state", "publish_jobs", "state", "unknown"},
		{"publish-jobs-cleanup-state", "publish_jobs", "cleanup_state", "unknown"},
		{"egress-uncertain-state", "egress_accounting_uncertain", "state", "unknown"},
	}
	queries := map[string]string{
		"accounts":                    "UPDATE accounts SET status=? WHERE id='a'",
		"repositories":                "UPDATE repositories SET visibility=? WHERE id='r'",
		"billing_purchases":           "UPDATE billing_purchases SET status=? WHERE id='p'",
		"billing_provider_events":     "UPDATE billing_provider_events SET status=? WHERE provider_event_id='e'",
		"product_grants":              "UPDATE product_grants SET period_cadence=? WHERE customer_id='a'",
		"purchase_grant_snapshots":    "UPDATE purchase_grant_snapshots SET period_cadence=? WHERE purchase_id='p'",
		"purchase_grant_instances":    "INSERT INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence) VALUES('a','product','cap','p','offer',1,'now','later',?)",
		"publish_jobs":                "UPDATE publish_jobs SET state=? WHERE id='j'",
		"egress_accounting_uncertain": "UPDATE egress_accounting_uncertain SET state=? WHERE id='e'",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := queries[tc.table]
			if tc.name == "publish-jobs-cleanup-state" {
				q = "UPDATE publish_jobs SET cleanup_state=? WHERE id='j'"
			}
			if _, err := s.db.Exec(q, tc.value); err == nil {
				t.Fatalf("invalid %s accepted", tc.name)
			}
		})
	}
	if _, err := s.db.Exec(`UPDATE egress_accounting_uncertain SET reserved_bytes=-1 WHERE id='e'`); err == nil {
		t.Fatal("negative reserved bytes accepted")
	}
}

func TestSchemaV2ToV3MigrationIsAtomicOnInjectedFailure(t *testing.T) {
	dir := t.TempDir()
	s := structuralFixture(t, dir)
	if _, err := s.db.Exec(`UPDATE tpahub_schema SET version=2`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrationFault = func(stage string) error {
		if stage == "before-structural-constraints" {
			return context.Canceled
		}
		return nil
	}
	defer func() { migrationFault = nil }()
	if err := Migrate(dir); err == nil {
		t.Fatal("injected migration failure accepted")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema changed after failed migration: %d", version)
	}
	var integrity string
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
}

func TestSchemaV3AllowsExtensibleProviderEnvironment(t *testing.T) {
	s := structuralFixture(t)
	defer s.Close()
	var provider, environment string
	if err := s.db.QueryRow(`SELECT provider,billing_environment FROM billing_purchases WHERE id='p'`).Scan(&provider, &environment); err != nil {
		t.Fatal(err)
	}
	if provider != "future-provider" || environment != "sandbox" {
		t.Fatalf("provider=%q environment=%q", provider, environment)
	}
}
