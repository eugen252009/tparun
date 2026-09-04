package main

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestNanoOfferProducesCapabilityGrants(t *testing.T) {
	offer := tpaNanoOffer()
	if offer.ProductID != commerce.ProductTPAHub || offer.ID != "nano" || offer.AmountMinor != 500 || len(offer.Grants) != 4 {
		t.Fatalf("offer=%+v", offer)
	}
	grants := defaultTPAGrants("2026-01-01T00:00:00Z")
	limits := map[commerce.CapabilityID]commerce.Limits{}
	for _, grant := range grants {
		limits[grant.CapabilityID] = grant.Limits
	}
	if limits[CapabilityRepositoryPublish].StorageBytes != NanoStorageBytes || limits[CapabilityRepositoryDownload].EgressBytes != NanoEgressBytes {
		t.Fatalf("limits=%+v", limits)
	}
}

func TestAnnualEntitlementPeriodIsAnniversaryAndDayAligned(t *testing.T) {
	p, err := entitlementPeriod("2026-09-15T10:00:00Z", time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Start.Equal(time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)) || !p.End.Equal(time.Date(2027, 9, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("period=%+v", p)
	}
	atEnd, _ := entitlementPeriod("2026-09-15", p.End)
	if !atEnd.Start.Equal(p.End) {
		t.Fatalf("end was not exclusive: %+v", atEnd)
	}
}

func TestEffectivePaidEntitlementUsesExplicitHalfOpenValidity(t *testing.T) {
	from := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2027, 9, 15, 0, 0, 0, 0, time.UTC)
	entitlement := store.AccountEntitlement{Source: "purchase", ValidFrom: from.Format(time.RFC3339Nano), ValidUntil: until.Format(time.RFC3339Nano)}
	if _, err := effectiveEntitlementPeriod(entitlement, from.Add(-time.Nanosecond)); err == nil {
		t.Fatal("paid entitlement active before valid_from")
	}
	period, err := effectiveEntitlementPeriod(entitlement, from)
	if err != nil || !period.Start.Equal(from) || !period.End.Equal(until) {
		t.Fatalf("at valid_from period=%+v err=%v", period, err)
	}
	if _, err = effectiveEntitlementPeriod(entitlement, until.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("inactive just before valid_until: %v", err)
	}
	if _, err = effectiveEntitlementPeriod(entitlement, until); err == nil {
		t.Fatal("paid entitlement regenerated at valid_until")
	}
}

func TestDefaultEntitlementStartsAtValidFromAndRemainsRecurring(t *testing.T) {
	entitlement := store.AccountEntitlement{Source: "legacy", ValidFrom: "2026-01-01T00:00:00Z"}
	if _, err := effectiveEntitlementPeriod(entitlement, time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)); err == nil {
		t.Fatal("default entitlement active before valid_from")
	}
	if _, err := effectiveEntitlementPeriod(entitlement, time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("default entitlement did not remain recurring: %v", err)
	}
}

func TestAnnualEntitlementLeapDayUsesCalendarArithmetic(t *testing.T) {
	p, err := entitlementPeriod("2024-02-29", time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// Go's calendar AddDate convention normalizes Feb 29 + one year to Mar 1.
	if !p.Start.Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) || !p.End.Equal(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("leap period=%+v", p)
	}
}

func TestDailyEgressAggregatesAnnualAccountPool(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if err := s.db.AddRepositoryEgressDaily(ctx, "missing", "2026-01-01", 1, "now"); err == nil {
		t.Fatal("missing repository accepted")
	}
	// Store-level aggregation is account-wide and uses [start,end).
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "daily", AccountID: "a", Name: "daily", Visibility: "public", CreatedAt: "2026-01-01"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryEgressDaily(ctx, "daily", "2026-01-10", 8_000_000_000, "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryEgressDaily(ctx, "daily", "2026-02-10", 3_000_000_000, "now"); err != nil {
		t.Fatal(err)
	}
	got, err := s.db.AccountEgressWindow(ctx, "a", time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC))
	if err != nil || got != 11_000_000_000 {
		t.Fatalf("annual usage=%d, %v", got, err)
	}
}
