package store

import (
	"context"
	"testing"
)

func TestCompleteEgressDeliveryRollsBackUsageAndPreservesUncertainty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateRepository(ctx, Repository{ID: "repo", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginEgressDelivery(ctx, "delivery", "repo", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_egress_usage BEFORE INSERT ON repository_egress_daily BEGIN SELECT RAISE(ABORT,'injected completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEgressDelivery(ctx, "delivery", "repo", "2030-01-01", 42, "now"); err == nil {
		t.Fatal("completion failure was accepted")
	}
	var uncertain, usage int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM egress_accounting_uncertain WHERE id='delivery'`).Scan(&uncertain)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM repository_egress_daily WHERE repository_id='repo'`).Scan(&usage)
	if uncertain != 1 || usage != 0 {
		t.Fatalf("uncertain=%d usage=%d", uncertain, usage)
	}
}
