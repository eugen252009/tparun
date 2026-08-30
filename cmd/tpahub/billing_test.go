package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestStorageHighWaterTracksRiseFallAndNewPeriod(t *testing.T) {
	s, _ := testServer(t)
	s.now = func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) }
	id := "66666666666666666666666666666666"
	root := storageTestRepository(t, s, id)
	path := filepath.Join(root, "package.deb")
	write := func(size int) {
		if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(100)
	if _, err := s.billingSnapshot(context.Background(), "a", s.currentTime()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.db.StorageHighWater(context.Background(), "a", "2026-08"); err != nil || got != 100 {
		t.Fatalf("initial high-water=%d, %v", got, err)
	}
	write(400)
	if _, err := s.billingSnapshot(context.Background(), "a", s.currentTime()); err != nil {
		t.Fatal(err)
	}
	write(200)
	if _, err := s.billingSnapshot(context.Background(), "a", s.currentTime()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.db.StorageHighWater(context.Background(), "a", "2026-08"); got != 400 {
		t.Fatalf("August high-water=%d", got)
	}
	s.now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	if _, err := s.billingSnapshot(context.Background(), "a", s.currentTime()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.db.StorageHighWater(context.Background(), "a", "2026-09"); got != 200 {
		t.Fatalf("September high-water=%d", got)
	}
}

func TestProRatingBoundariesAndNanoFixedPlan(t *testing.T) {
	for _, test := range []struct{ value, block, want int64 }{{0, 500_000_000, 0}, {1, 500_000_000, 1}, {500_000_000, 500_000_000, 1}, {500_000_001, 500_000_000, 2}, {1_000_000_001, 500_000_000, 3}, {0, 20_000_000_000, 0}, {20_000_000_000, 20_000_000_000, 1}, {20_000_000_001, 20_000_000_000, 2}, {40_000_000_001, 20_000_000_000, 3}} {
		if got := ceilBlocks(test.value, test.block); got != test.want {
			t.Fatalf("ceilBlocks(%d,%d)=%d want %d", test.value, test.block, got, test.want)
		}
	}
	nano := rateBilling(BillingUsageSnapshot{AccountID: "a", PlanCode: "nano", StorageHighWaterBytes: 4_000_000, MonthlyEgressBytes: 800_000_000})
	if nano.StorageBlocks != 0 || nano.EgressBlocks != 0 {
		t.Fatalf("Nano rating=%+v", nano)
	}
	pro := rateBilling(BillingUsageSnapshot{AccountID: "a", PlanCode: "pro", StorageHighWaterBytes: 1_210_000_000, MonthlyEgressBytes: 53_000_000_000})
	if pro.StorageBlocks != 3 || pro.EgressBlocks != 3 {
		t.Fatalf("Pro rating=%+v", pro)
	}
}

func TestMockBillingPreviewIsDeterministicAndIdempotent(t *testing.T) {
	s, _ := testServer(t)
	snapshot := BillingUsageSnapshot{AccountID: "a", PlanCode: "pro", Period: "2026-08", StorageHighWaterBytes: 1_210_000_000, MonthlyEgressBytes: 53_000_000_000}
	provider := s.billingProvider()
	first, err := provider.Preview(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Preview(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("previews differ: %+v %+v", first, second)
	}
	if first.TotalAmountMinor != 1400 || len(first.LineItems) != 3 || first.RateCardID != "mock-rate-card-v1" || !first.IsVirtual {
		t.Fatalf("unexpected Pro monetary preview: %+v", first)
	}
	nano, err := (MockBillingProvider{Store: s.db, RateCard: mockRateCardV1}).Preview(context.Background(), BillingUsageSnapshot{AccountID: "a", PlanCode: "nano", Period: "2026-08", StorageHighWaterBytes: 4_000_000, MonthlyEgressBytes: 800_000_000})
	if err != nil || nano.TotalAmountMinor != 440 || nano.StorageBlocks != 0 {
		t.Fatalf("unexpected Nano monetary preview: %+v, %v", nano, err)
	}
	if _, err := provider.Finalize(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Finalize(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	v2 := mockRateCardV1
	v2.ID = "mock-rate-card-v2"
	v2.ProBaseAmountMinor = 9000
	v2Preview, err := (MockBillingProvider{Store: s.db, RateCard: v2}).Preview(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if v2Preview.TotalAmountMinor == first.TotalAmountMinor || v2Preview.StorageBlocks != first.StorageBlocks || v2Preview.EgressBlocks != first.EgressBlocks {
		t.Fatalf("rate card did not change only monetary result: v1=%+v v2=%+v", first, v2Preview)
	}
	stableProvider := MockBillingProvider{Store: s.db, RateCard: v2}
	stable, err := stableProvider.Finalize(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, stable) {
		t.Fatalf("finalized preview was repriced: first=%+v stable=%+v", first, stable)
	}
}

func TestBillingAccountIsolation(t *testing.T) {
	s, _ := testServer(t)
	if err := s.db.CreateAccount(context.Background(), store.Account{ID: "b", Email: "b@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryEgress(context.Background(), "missing", "2026-08", 100, "now"); err == nil {
		t.Fatal("missing repository accepted")
	}
	if got, err := s.db.AccountEgress(context.Background(), "b", "2026-08"); err != nil || got != 0 {
		t.Fatalf("isolated account egress=%d,%v", got, err)
	}
}
