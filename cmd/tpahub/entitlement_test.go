package main

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

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
