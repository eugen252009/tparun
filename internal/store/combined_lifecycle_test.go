package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func seedEligibleHistoricalAccount(t *testing.T, dir, plan string) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	grants := tpahubproduct.DefaultGrants(now)
	for i := range grants {
		grants[i].Source = "legacy"
		grants[i].OfferID = plan
		if grants[i].CapabilityID == tpahubproduct.CapabilityRepositoryPublish {
			grants[i].Limits.StorageBytes = 999_000_000
		}
	}
	if err = s.CreateAccount(context.Background(), Account{ID: "historical", Email: "historical@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}, grants...); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM schema_migrations WHERE name='legacy-six-month-nano-v1'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestHistoricalAccountsReceiveOneBoundedNanoMigrationGrant(t *testing.T) {
	for _, plan := range []string{"standard", "pro"} {
		t.Run(plan, func(t *testing.T) {
			dir := t.TempDir()
			seedEligibleHistoricalAccount(t, dir, plan)
			anchor := time.Date(2028, 4, 15, 12, 0, 0, 0, time.UTC)
			migrationNow = func() time.Time { return anchor }
			defer func() { migrationNow = time.Now }()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			var fromText, untilText string
			if err = s.db.QueryRow(`SELECT valid_from,valid_until FROM account_entitlements WHERE account_id='historical' AND source='migration' AND plan_code='nano'`).Scan(&fromText, &untilText); err != nil {
				t.Fatal(err)
			}
			from, _ := time.Parse(time.RFC3339Nano, fromText)
			until, _ := time.Parse(time.RFC3339Nano, untilText)
			if !from.Equal(anchor) || !until.Equal(anchor.AddDate(0, 6, 0)) {
				t.Fatalf("migration validity %s..%s", from, until)
			}
			var n int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id='historical' AND source='migration' AND offer_id='nano' AND purchase_id=''`).Scan(&n); err != nil || n != len(tpahubproduct.NanoOffer().Grants) {
				t.Fatalf("migration grants=%d err=%v", n, err)
			}
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id='historical' AND source='legacy'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("legacy grants remain=%d err=%v", n, err)
			}
			g, err := s.GetGrant(ctx, "historical", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, from.Add(time.Hour))
			if _, mutationErr := s.db.Exec(`UPDATE product_grants SET valid_until=valid_from WHERE customer_id='historical' AND source='migration'`); mutationErr == nil {
				t.Fatal("migration validity was mutable")
			}
			if err != nil || g.Source != "migration" || g.Limits.StorageBytes != tpahubproduct.NanoStorageBytes {
				t.Fatalf("effective migration=%+v err=%v", g, err)
			}
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var from2, until2 string
			_ = s.db.QueryRow(`SELECT valid_from,valid_until FROM account_entitlements WHERE account_id='historical'`).Scan(&from2, &until2)
			if from2 != fromText || until2 != untilText {
				t.Fatalf("migration clock extended %s/%s -> %s/%s", fromText, untilText, from2, until2)
			}
			if err = s.SetAccountStatus(ctx, "historical", "suspended"); err != nil {
				t.Fatal(err)
			}
			if err = s.SetAccountStatus(ctx, "historical", AccountStatusActive); err != nil {
				t.Fatal(err)
			}
			var afterReactivation string
			if err = s.db.QueryRow(`SELECT valid_until FROM product_grants WHERE customer_id='historical' AND source='migration' LIMIT 1`).Scan(&afterReactivation); err != nil || afterReactivation != untilText {
				t.Fatalf("reactivation changed migration expiry %q -> %q: %v", untilText, afterReactivation, err)
			}
			if _, err = s.GetGrant(ctx, "historical", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, until); !errors.Is(err, commerce.ErrGrantInactive) {
				t.Fatalf("expired migration err=%v", err)
			}
		})
	}
}

func TestPurchaseBeforeBoundedMigrationRemainsIndependent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2033, 4, 5, 6, 7, 8, 0, time.UTC)
	legacy := tpahubproduct.DefaultGrants(now.Add(-time.Hour))
	for i := range legacy {
		legacy[i].Source = "legacy"
		legacy[i].OfferID = "standard"
	}
	if err = s.CreateAccount(ctx, Account{ID: "purchase-first", Email: "purchase-first@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}, legacy...); err != nil {
		t.Fatal(err)
	}
	purchaseEnd := now.AddDate(1, 0, 0)
	purchase := BillingPurchase{ID: "purchase-before-migration", AccountID: "purchase-first", ProductID: "tpa-hub", PlanCode: "paid", RateCardID: "paid", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: purchaseEnd.Format(time.RFC3339Nano), ProviderReference: "purchase-first-reference", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 9_000_000}}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, purchase.ID, purchase.ProviderReference, "https://checkout.invalid/purchase-first", purchase.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "purchase-first-event", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM schema_migrations WHERE name='legacy-six-month-nano-v1'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	migrationNow = func() time.Time { return now }
	defer func() { migrationNow = time.Now }()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	grant, err := s.GetGrant(ctx, "purchase-first", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, now.Add(time.Hour))
	if err != nil || grant.Effective == nil || len(grant.Effective.Contributors) != 2 || grant.Limits.StorageBytes != 9_000_000 {
		t.Fatalf("purchase-before-migration effective grant=%+v err=%v", grant, err)
	}
	if _, err = s.GetGrant(ctx, "purchase-first", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, now.AddDate(0, 6, 0)); err != nil {
		t.Fatalf("purchase did not survive migration expiry: %v", err)
	}
}

func TestMigrationAndPurchasesUseOnlyActiveEnabledContributors(t *testing.T) {
	dir := t.TempDir()
	seedEligibleHistoricalAccount(t, dir, "pro")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var migrationFrom, migrationUntil string
	_ = s.db.QueryRow(`SELECT valid_from,valid_until FROM account_entitlements WHERE account_id='historical'`).Scan(&migrationFrom, &migrationUntil)
	from, _ := time.Parse(time.RFC3339Nano, migrationFrom)
	until, _ := time.Parse(time.RFC3339Nano, migrationUntil)
	purchaseUntil := until.AddDate(0, 6, 0)
	p := BillingPurchase{ID: "paid", AccountID: "historical", ProductID: "tpa-hub", PlanCode: "paid", RateCardID: "paid", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: purchaseUntil.Format(time.RFC3339Nano), ProviderReference: "ref", CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 7_000_000}}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, p.ID, p.ProviderReference, "https://checkout.invalid/paid", p.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "evt", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref", AmountMinor: 1, Currency: "EUR", OccurredAt: from}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrant(ctx, "historical", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, from.Add(time.Hour))
	if err != nil || g.Effective == nil || len(g.Effective.Contributors) != 2 || g.Limits.StorageBytes != 7_000_000 || g.Source != "effective" || g.OfferID != "" || g.PurchaseID != "" || g.Period != "" || !g.ValidFrom.IsZero() || g.ValidUntil != nil {
		t.Fatalf("combined=%+v err=%v", g, err)
	}
	if _, err = s.db.Exec(`UPDATE product_grants SET enabled=0 WHERE customer_id='historical' AND source='migration'`); err != nil {
		t.Fatal(err)
	}
	g, err = s.GetGrant(ctx, "historical", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, from.Add(time.Hour))
	if err != nil || g.Limits.StorageBytes != 7_000_000 || g.Source != "purchase" {
		t.Fatalf("disabled migration contributed: %+v %v", g, err)
	}
	if _, err = s.GetGrant(ctx, "historical", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, until.Add(time.Second)); err != nil {
		t.Fatalf("purchase did not survive migration expiry: %v", err)
	}
}

func TestFutureMigrationDoesNotContributeUntilActive(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 0, 1)
	end := future.AddDate(0, 6, 0)
	if err = s.CreateAccount(ctx, Account{ID: "future", Email: "future@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "future", ProductID: commerce.ProductTPAHub, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "nano", Enabled: true, ValidFrom: future, ValidUntil: &end, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 99}, Source: "migration", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetGrant(ctx, "future", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, now); !errors.Is(err, commerce.ErrGrantInactive) {
		t.Fatalf("future grant active: %v", err)
	}
	g, err := s.GetGrant(ctx, "future", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, future)
	if err != nil || g.Limits.StorageBytes != 99 {
		t.Fatalf("future grant not activated: %+v %v", g, err)
	}
}

func TestBoundedMigrationEligibilityIsTPAScopedAndActive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, id := range []string{"other-product", "disabled-tpa", "expired-tpa", "eligible-tpa"} {
		if err = s.CreateAccount(context.Background(), Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: anchor.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	expired := anchor.Add(-time.Second)
	grants := []commerce.Grant{
		{CustomerID: "other-product", ProductID: "other", CapabilityID: "other.use", OfferID: "legacy", Enabled: true, ValidFrom: anchor.Add(-time.Hour), Period: commerce.PeriodAnnual, Source: "legacy", CreatedAt: anchor, UpdatedAt: anchor},
		{CustomerID: "disabled-tpa", ProductID: commerce.ProductTPAHub, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "legacy", Enabled: false, ValidFrom: anchor.Add(-time.Hour), Period: commerce.PeriodAnnual, Source: "legacy", CreatedAt: anchor, UpdatedAt: anchor},
		{CustomerID: "expired-tpa", ProductID: commerce.ProductTPAHub, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "legacy", Enabled: true, ValidFrom: anchor.Add(-time.Hour), ValidUntil: &expired, Period: commerce.PeriodAnnual, Source: "legacy", CreatedAt: anchor, UpdatedAt: anchor},
		{CustomerID: "eligible-tpa", ProductID: commerce.ProductTPAHub, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "legacy", Enabled: true, ValidFrom: anchor.Add(-time.Hour), Period: commerce.PeriodAnnual, Source: "legacy", CreatedAt: anchor, UpdatedAt: anchor},
	}
	for _, grant := range grants {
		if err = s.PutGrant(context.Background(), grant); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`DELETE FROM schema_migrations WHERE name='legacy-six-month-nano-v1'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	migrationNow = func() time.Time { return anchor }
	defer func() { migrationNow = time.Now }()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"other-product", "disabled-tpa", "expired-tpa"} {
		var count int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id=? AND source='migration'`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("ineligible account %s received %d migration grants: %v", id, count, err)
		}
	}
	var eligible int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id='eligible-tpa' AND source='migration'`).Scan(&eligible); err != nil || eligible != len(tpahubproduct.NanoOffer().Grants) {
		t.Fatalf("eligible migration grants=%d err=%v", eligible, err)
	}
}

func TestBoundedMigrationInterruptionReopensIdempotently(t *testing.T) {
	for _, stage := range []string{"bounded-after-eligibility", "bounded-after-account", "bounded-before-commit"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			seedEligibleHistoricalAccount(t, dir, "standard")
			anchor := time.Date(2032, 3, 4, 5, 6, 7, 0, time.UTC)
			migrationNow = func() time.Time { return anchor }
			defer func() { migrationNow = time.Now; migrationFault = nil }()
			migrationFault = func(got string) error {
				if got == stage {
					return errors.New("bounded migration interruption")
				}
				return nil
			}
			if s, openErr := Open(dir); openErr == nil {
				s.Close()
				t.Fatal("interrupted bounded migration unexpectedly committed")
			}
			migrationFault = nil
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var marker, grants int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='legacy-six-month-nano-v1'`).Scan(&marker)
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id='historical' AND source='migration'`).Scan(&grants)
			if marker != 1 || grants != len(tpahubproduct.NanoOffer().Grants) {
				t.Fatalf("marker=%d grants=%d", marker, grants)
			}
		})
	}
}

func TestMigrationContributionRequiresEnabledAndActive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		fromOffset time.Duration
		until      func(time.Time) time.Time
		want       int64
	}{
		{name: "disabled", enabled: false, fromOffset: -time.Hour, until: func(now time.Time) time.Time { return now.AddDate(0, 6, 0) }, want: 1_000_000},
		{name: "future", enabled: true, fromOffset: time.Hour, until: func(now time.Time) time.Time { return now.AddDate(0, 6, 0) }, want: 1_000_000},
		{name: "expired", enabled: true, fromOffset: -time.Hour, until: func(now time.Time) time.Time { return now }, want: 1_000_000},
		{name: "active", enabled: true, fromOffset: -time.Hour, until: func(now time.Time) time.Time { return now.AddDate(0, 6, 0) }, want: tpahubproduct.NanoStorageBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			if err = s.CreateAccount(ctx, Account{ID: "matrix", Email: tc.name + "@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
				t.Fatal(err)
			}
			migrationUntil := tc.until(now)
			if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "matrix", ProductID: commerce.ProductTPAHub, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "nano", Enabled: tc.enabled, ValidFrom: now.Add(tc.fromOffset), ValidUntil: &migrationUntil, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: tpahubproduct.NanoStorageBytes}, Source: "migration", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			purchaseUntil := now.AddDate(1, 0, 0)
			purchase := BillingPurchase{ID: "matrix-purchase", AccountID: "matrix", ProductID: "tpa-hub", PlanCode: "paid", RateCardID: "paid", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Add(-time.Hour).Format(time.RFC3339Nano), EntitlementPeriodEnd: purchaseUntil.Format(time.RFC3339Nano), ProviderReference: "matrix-reference", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
			if _, err = s.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 1_000_000}}); err != nil {
				t.Fatal(err)
			}
			if err = s.UpdatePurchaseCheckout(ctx, purchase.ID, purchase.ProviderReference, "https://checkout.invalid/matrix", purchase.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "matrix-event", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); err != nil {
				t.Fatal(err)
			}
			grant, err := s.GetGrant(ctx, "matrix", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, now)
			if err != nil || grant.Limits.StorageBytes != tc.want {
				t.Fatalf("grant=%+v err=%v want=%d", grant, err, tc.want)
			}
			if tc.name == "active" && grant.Effective == nil {
				t.Fatal("active migration and purchase were not independently represented")
			}
			if tc.name != "active" && grant.Source != "purchase" {
				t.Fatalf("inactive migration contributed provenance: %+v", grant)
			}
		})
	}
}

func TestPurchaseExpiryDoesNotResetOrRemoveMigration(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	migrationUntil := now.AddDate(0, 6, 0)
	if err = s.CreateAccount(ctx, Account{ID: "independent", Email: "independent@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err = s.PutGrant(ctx, commerce.Grant{CustomerID: "independent", ProductID: commerce.ProductTPAHub, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "nano", Enabled: true, ValidFrom: now, ValidUntil: &migrationUntil, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: tpahubproduct.NanoStorageBytes}, Source: "migration", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	purchaseUntil := now.AddDate(0, 1, 0)
	purchase := BillingPurchase{ID: "short-purchase", AccountID: "independent", ProductID: "tpa-hub", PlanCode: "paid", RateCardID: "paid", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 1, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: purchaseUntil.Format(time.RFC3339Nano), ProviderReference: "short-reference", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, commerce.GrantTemplate{CapabilityID: tpahubproduct.CapabilityRepositoryPublish, Period: commerce.PeriodMonthly, Limits: commerce.Limits{StorageBytes: 7_000_000}}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePurchaseCheckout(ctx, purchase.ID, purchase.ProviderReference, "https://checkout.invalid/short", purchase.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "short-event", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: purchase.ProviderReference, AmountMinor: 1, Currency: "EUR", OccurredAt: now}, Provider: "stripe", Environment: "test", Kind: "success", AmountMinor: 1, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	grant, err := s.GetGrant(ctx, "independent", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, purchaseUntil)
	if err != nil || grant.Source != "migration" || grant.Limits.StorageBytes != tpahubproduct.NanoStorageBytes {
		t.Fatalf("migration after purchase expiry=%+v err=%v", grant, err)
	}
	if _, err = s.GetGrant(ctx, "independent", commerce.ProductTPAHub, tpahubproduct.CapabilityRepositoryPublish, migrationUntil); !errors.Is(err, commerce.ErrGrantInactive) {
		t.Fatalf("migration did not expire normally: %v", err)
	}
}

func TestNewHostedAccountDoesNotReceiveMigrationGrant(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(context.Background(), Account{ID: "new-hosted", Email: "new-hosted@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM product_grants WHERE customer_id='new-hosted' AND source='migration'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new hosted migration grants=%d err=%v", count, err)
	}
}
