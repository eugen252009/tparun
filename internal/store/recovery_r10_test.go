package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func recoveryPurchaseFixture(t *testing.T, dir, id string) (*Store, BillingPurchase, time.Time, time.Time) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = s.CreateAccount(ctx, Account{ID: "recovery-" + id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: from.Format(time.RFC3339Nano)}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	p := BillingPurchase{ID: id, AccountID: "recovery-" + id, ProductID: string(commerce.ProductTPAHub), PlanCode: "offer-x", RateCardID: "rate-x", RateCardMode: "test", TaxClassification: "saas-personal", Currency: "EUR", AmountMinor: 500, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: from.Format(time.RFC3339Nano), EntitlementPeriodEnd: until.Format(time.RFC3339Nano), CreatedAt: from.Format(time.RFC3339Nano), UpdatedAt: from.Format(time.RFC3339Nano)}
	if _, err = s.CreatePendingPurchase(ctx, p, p.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 12345}}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, p, from, until
}

func recoverAndPay(t *testing.T, s *Store, p BillingPurchase, eventID string, occurredAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.RecoverPurchaseCheckout(ctx, p.ID, p.Provider, p.BillingEnvironment, "ref-"+p.ID, time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPaymentTransition(ctx, commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: eventID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-" + p.ID, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: occurredAt}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency}); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveredTerms(t *testing.T, s *Store, p BillingPurchase, from, until time.Time) {
	t.Helper()
	stored, err := s.Purchase(context.Background(), p.ID)
	if err != nil || stored.EntitlementPeriodStart != from.Format(time.RFC3339Nano) || stored.EntitlementPeriodEnd != until.Format(time.RFC3339Nano) {
		t.Fatalf("commercial terms drifted: purchase=%+v err=%v", stored, err)
	}
	var instanceFrom, instanceUntil string
	if err = s.db.QueryRow(`SELECT valid_from,valid_until FROM purchase_grant_instances WHERE purchase_id=? AND capability_id='repository.publish'`, p.ID).Scan(&instanceFrom, &instanceUntil); err != nil {
		t.Fatal(err)
	}
	if instanceFrom != stored.EntitlementPeriodStart || instanceUntil != stored.EntitlementPeriodEnd {
		t.Fatalf("instance validity=%s..%s purchase=%s..%s", instanceFrom, instanceUntil, stored.EntitlementPeriodStart, stored.EntitlementPeriodEnd)
	}
}

func TestRecoveredCommercialTermsIgnoreProviderAndReceiveTimes(t *testing.T) {
	for _, occurredAt := range []time.Time{
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(occurredAt.Format("2006-01-02"), func(t *testing.T) {
			s, p, from, until := recoveryPurchaseFixture(t, t.TempDir(), "purchase-"+occurredAt.Format("20060102"))
			defer s.Close()
			recoverAndPay(t, s, p, "event-"+occurredAt.Format("20060102"), occurredAt)
			assertRecoveredTerms(t, s, p, from, until)
			if occurredAt.After(until) {
				if _, err := s.GetGrant(context.Background(), commerce.CustomerID(p.AccountID), commerce.ProductTPAHub, "repository.publish", occurredAt); err != commerce.ErrGrantInactive {
					t.Fatalf("expired immutable terms granted access: %v", err)
				}
			}
		})
	}
}

func TestRecoveredPaymentReplayAcrossRestartCannotDriftValidity(t *testing.T) {
	dir := t.TempDir()
	s, p, from, until := recoveryPurchaseFixture(t, dir, "restart-replay")
	if err := s.RecoverPurchaseCheckout(context.Background(), p.ID, p.Provider, p.BillingEnvironment, "ref-"+p.ID, "2040-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	late := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	transition := commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "replayed-event", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-" + p.ID, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: late}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency}
	if err = s.ApplyPaymentTransition(context.Background(), transition); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ApplyPaymentTransition(context.Background(), transition); err != nil {
		t.Fatal(err)
	}
	transition.Event.ProviderEventID = "same-payment-later-delivery"
	transition.Event.OccurredAt = late.AddDate(1, 0, 0)
	if err = s.ApplyPaymentTransition(context.Background(), transition); err != nil {
		t.Fatal(err)
	}
	assertRecoveredTerms(t, s, p, from, until)
	if count, countErr := s.PurchaseGrantInstanceCount(context.Background(), p.AccountID); countErr != nil || count != 1 {
		t.Fatalf("instances=%d err=%v", count, countErr)
	}
}

func TestRecoveryUsesPersistedSnapshotAfterLiveOfferMutation(t *testing.T) {
	s, p, from, until := recoveryPurchaseFixture(t, t.TempDir(), "offer-mutation")
	defer s.Close()
	now := from.Add(time.Hour)
	if err := s.PutGrant(context.Background(), commerce.Grant{CustomerID: commerce.CustomerID(p.AccountID), ProductID: commerce.ProductTPAHub, CapabilityID: "repository.publish", OfferID: "offer-y", Enabled: true, ValidFrom: from, ValidUntil: &until, Period: commerce.PeriodMonthly, Limits: commerce.Limits{StorageBytes: 999999}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	recoverAndPay(t, s, p, "offer-mutation-event", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	assertRecoveredTerms(t, s, p, from, until)
	var offer, cadence string
	var storage int64
	if err := s.db.QueryRow(`SELECT offer_id,period_cadence,storage_limit_bytes FROM purchase_grant_instances WHERE purchase_id=?`, p.ID).Scan(&offer, &cadence, &storage); err != nil {
		t.Fatal(err)
	}
	if offer != "offer-x" || cadence != string(commerce.PeriodAnnual) || storage != 12345 {
		t.Fatalf("recovered live offer instead of snapshot: offer=%s cadence=%s storage=%d", offer, cadence, storage)
	}
}

func TestMissingRecoveryAuthorityRetainsProviderEvidenceAndFailsClosed(t *testing.T) {
	s, p, _, _ := recoveryPurchaseFixture(t, t.TempDir(), "missing-authority")
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.Exec(`UPDATE billing_purchases SET entitlement_period_start='',entitlement_period_end='' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPurchaseCheckout(ctx, p.ID, p.Provider, p.BillingEnvironment, "ref-missing-authority", "2040-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	transition := commerce.PaymentTransition{Event: commerce.PaymentEvent{ProviderEventID: "paid-unreconciled", OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-missing-authority", AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, Provider: p.Provider, Environment: p.BillingEnvironment, Kind: "success", AmountMinor: p.AmountMinor, Currency: p.Currency}
	if err := s.ApplyPaymentTransition(ctx, transition); err != nil {
		t.Fatal(err)
	}
	transition.Event.ProviderEventID = "late-failure-unreconciled"
	transition.Event.Type = commerce.PaymentFailed
	transition.Kind = "failed"
	if err := s.ApplyPaymentTransition(ctx, transition); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Purchase(ctx, p.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderReference != "ref-missing-authority" || stored.ProviderState != "provider_success_unreconciled" {
		t.Fatalf("unreconciled purchase=%+v err=%v", stored, err)
	}
	var eventStatus string
	if err = s.db.QueryRow(`SELECT status FROM billing_provider_events WHERE provider_event_id='paid-unreconciled'`).Scan(&eventStatus); err != nil || eventStatus != "reconciliation_required" {
		t.Fatalf("provider evidence status=%s err=%v", eventStatus, err)
	}
	if count, countErr := s.PurchaseGrantInstanceCount(ctx, p.AccountID); countErr != nil || count != 0 {
		t.Fatalf("unreconciled instances=%d err=%v", count, countErr)
	}
	if _, err = s.db.Exec(`UPDATE billing_purchases SET entitlement_period_start='2030-01-01T00:00:00Z' WHERE id=?`, p.ID); err == nil {
		t.Fatal("unreconciled commercial authority was mutable")
	}
	fresh := p
	fresh.ID = "blocked-by-reconciliation"
	fresh.EntitlementPeriodStart = "2030-01-01T00:00:00Z"
	fresh.EntitlementPeriodEnd = "2031-01-01T00:00:00Z"
	fresh.CreatedAt = fresh.EntitlementPeriodStart
	fresh.UpdatedAt = fresh.CreatedAt
	if _, err = s.CreatePendingPurchase(ctx, fresh, fresh.CreatedAt, commerce.GrantTemplate{CapabilityID: "repository.publish"}); err == nil || !strings.Contains(err.Error(), "purchase-reconciliation-required") {
		t.Fatalf("new checkout was not blocked by unresolved payment: %v", err)
	}
}
