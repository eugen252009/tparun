package store

import (
	"context"
	"sync"
	"testing"
)

func egressR6Fixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err = s.CreateAccount(ctx, Account{ID: "egress-account", Email: "egress-r6@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "egress-repo", AccountID: "egress-account", Name: "egress", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEgressRecoveryRejectsActiveMarkerAndAuditsRejection(t *testing.T) {
	s := egressR6Fixture(t)
	ctx := context.Background()
	if err := s.BeginEgressDelivery(ctx, "active-delivery", "egress-repo", "now"); err != nil {
		t.Fatal(err)
	}
	err := s.RecoverEgressDeliveryWithAudit(ctx, "egress-account", "active-delivery", "egress-repo", 1, "2026-09-04", 42, "now", "operator-r6")
	if err == nil || err.Error() != "egress delivery is still active" {
		t.Fatalf("active recovery error=%v", err)
	}
	var result, state string
	var version int64
	if err = s.db.QueryRow(`SELECT state,version FROM egress_accounting_uncertain WHERE id=?`, "active-delivery").Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	if state != "streaming" || version != 1 {
		t.Fatalf("active marker changed: state=%s version=%d", state, version)
	}
	if err = s.db.QueryRow(`SELECT result FROM admin_audit_log WHERE marker_id=?`, "active-delivery").Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "rejected" {
		t.Fatalf("audit result=%q", result)
	}
}

func TestEgressRecoveryRacePreservesUsageOrUncertainty(t *testing.T) {
	for _, recoverFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion-first", true: "recovery-first"}[recoverFirst], func(t *testing.T) {
			s := egressR6Fixture(t)
			ctx := context.Background()
			if err := s.BeginEgressDelivery(ctx, "race-delivery", "egress-repo", "now"); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkEgressDeliveryUncertain(ctx, "race-delivery", "egress-repo", "settlement failed"); err != nil {
				t.Fatal(err)
			}
			if recoverFirst {
				if err := s.RecoverEgressDeliveryWithAudit(ctx, "egress-account", "race-delivery", "egress-repo", 2, "2026-09-04", 42, "now", "operator-r6"); err != nil {
					t.Fatal(err)
				}
				if err := s.CompleteEgressDelivery(ctx, "race-delivery", "egress-repo", "2026-09-04", 42, "now"); err == nil {
					t.Fatal("completion after recovery unexpectedly succeeded")
				}
			} else {
				if err := s.CompleteEgressDelivery(ctx, "race-delivery", "egress-repo", "2026-09-04", 42, "now"); err != nil {
					t.Fatal(err)
				}
				if err := s.RecoverEgressDeliveryWithAudit(ctx, "egress-account", "race-delivery", "egress-repo", 2, "2026-09-04", 42, "now", "operator-r6"); err == nil {
					t.Fatal("recovery after completion unexpectedly succeeded")
				}
			}
			var usage, markers int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM repository_egress_daily WHERE repository_id=? AND egress_bytes>0`, "egress-repo").Scan(&usage); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM egress_accounting_uncertain WHERE id=?`, "race-delivery").Scan(&markers); err != nil {
				t.Fatal(err)
			}
			if usage == 0 && markers == 0 {
				t.Fatal("race lost both usage and uncertainty")
			}
		})
	}
}

func TestEgressRecoveryConcurrentCompletionAndRecovery(t *testing.T) {
	s := egressR6Fixture(t)
	ctx := context.Background()
	if err := s.BeginEgressDelivery(ctx, "concurrent-delivery", "egress-repo", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEgressDeliveryUncertain(ctx, "concurrent-delivery", "egress-repo", "settlement failed"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- s.CompleteEgressDelivery(ctx, "concurrent-delivery", "egress-repo", "2026-09-04", 42, "now")
	}()
	go func() {
		defer wg.Done()
		results <- s.RecoverEgressDeliveryWithAudit(ctx, "egress-account", "concurrent-delivery", "egress-repo", 2, "2026-09-04", 42, "now", "operator-r6")
	}()
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("completion/recovery successes=%d", successes)
	}
	var usage, markers int
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(egress_bytes),0) FROM repository_egress_daily WHERE repository_id=?`, "egress-repo").Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM egress_accounting_uncertain WHERE id=?`, "concurrent-delivery").Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if usage == 0 && markers == 0 {
		t.Fatal("concurrent ordering lost both usage and uncertainty")
	}
}
