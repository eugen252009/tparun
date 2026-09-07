package main

import "testing"

func TestBootstrapRecommendationIsFiniteAndAdvisory(t *testing.T) {
	cases := []struct{ usage, current, want int64 }{
		{0, 0, 1_000_000_000},
		{517_000_000, 1_000_000_000, 1_000_000_000},
		{920_000_000, 1_000_000_000, 2_000_000_000},
	}
	for _, tc := range cases {
		if got := recommendedCapacity(tc.usage, tc.current); got != tc.want {
			t.Fatalf("recommendation(%d,%d)=%d want %d", tc.usage, tc.current, got, tc.want)
		}
	}
	if got := recommendedCapacity(1_000_000_000_000_000, 1_000_000_000_000_000); got < 1_000_000_000_000_000 {
		t.Fatalf("large recommendation=%d", got)
	}
	if got := recommendedCapacity(int64(^uint64(0)>>1), 0); got <= 0 {
		t.Fatalf("overflow recommendation=%d", got)
	}
}
