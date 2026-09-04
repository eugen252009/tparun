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
	if err := s.BeginEgressDelivery(ctx, "active-delivery", "egress-repo", "2026-09-04T10:00:00Z"); err != nil {
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
			if err := s.BeginEgressDelivery(ctx, "race-delivery", "egress-repo", "2026-09-04T10:00:00Z"); err != nil {
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
	if err := s.BeginEgressDelivery(ctx, "concurrent-delivery", "egress-repo", "2026-09-04T10:00:00Z"); err != nil {
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

func TestEgressRecoveryValidatesMarkerPeriod(t *testing.T) {
	s := egressR6Fixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, usageDate string
		wantErr         bool
	}{
		{name: "before", usageDate: "2026-08-31", wantErr: true},
		{name: "start", usageDate: "2026-09-01"},
		{name: "end", usageDate: "2026-09-30"},
		{name: "after", usageDate: "2026-10-01", wantErr: true},
		{name: "malformed", usageDate: "2026-09-31", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "period-delivery-" + tc.name
			if err := s.BeginEgressDelivery(ctx, id, "egress-repo", "2026-09-04T10:00:00Z"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReserveEgressDelivery(ctx, id, "egress-repo", "2026-09-01", "2026-10-01", 10, 100, "now"); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkEgressDeliveryUncertain(ctx, id, "egress-repo", "settlement failed"); err != nil {
				t.Fatal(err)
			}
			err := s.RecoverEgressDeliveryWithAudit(ctx, "egress-account", id, "egress-repo", 2, tc.usageDate, 10, tc.name, "operator-r7")
			if tc.wantErr && err == nil {
				t.Fatal("out-of-period recovery succeeded")
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEgressRecoveryListsAndRecoversUnownedRepository(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateRepository(ctx, Repository{ID: "unowned-repo", Name: "legacy", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateAccount(ctx, Account{ID: "owned-account", Email: "owned@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "owned-repo", AccountID: "owned-account", Name: "owned", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginEgressDelivery(ctx, "unowned-delivery", "unowned-repo", "2026-09-04T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkEgressDeliveryUncertain(ctx, "unowned-delivery", "unowned-repo", "settlement failed"); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginEgressDelivery(ctx, "owned-delivery", "owned-repo", "2026-09-04T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkEgressDeliveryUncertain(ctx, "owned-delivery", "owned-repo", "settlement failed"); err != nil {
		t.Fatal(err)
	}
	deliveries, err := s.EgressDeliveries(ctx, "")
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("unowned deliveries=%+v err=%v", deliveries, err)
	}
	if err = s.RecoverEgressDeliveryWithAudit(ctx, "", "unowned-delivery", "unowned-repo", 2, "2026-09-04", 4, "now", "operator-r6"); err != nil {
		t.Fatal(err)
	}
	deliveries, err = s.EgressDeliveries(ctx, "")
	if err != nil || len(deliveries) != 1 || deliveries[0].ID != "owned-delivery" {
		t.Fatalf("owned marker changed during unowned recovery: %+v err=%v", deliveries, err)
	}
	if err = s.RecoverEgressDeliveryWithAudit(ctx, "owned-account", "owned-delivery", "owned-repo", 2, "2026-09-04", 4, "later", "operator-r6"); err != nil {
		t.Fatal(err)
	}
}

func TestEgressReservationPersistsPeriodWithoutAllowance(t *testing.T) {
	s := egressR6Fixture(t)
	ctx := context.Background()
	if err := s.BeginEgressDelivery(ctx, "unlimited-delivery", "egress-repo", "2026-09-04T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if limited, err := s.ReserveEgressDelivery(ctx, "unlimited-delivery", "egress-repo", "2026-09-01", "2026-10-01", 100, 0, "now"); err != nil || limited {
		t.Fatalf("unlimited reservation limited=%v err=%v", limited, err)
	}
	var start, end string
	if err := s.db.QueryRow(`SELECT period_start,period_end FROM egress_accounting_uncertain WHERE id=?`, "unlimited-delivery").Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if start != "2026-09-01" || end != "2026-10-01" {
		t.Fatalf("period was not persisted: %s %s", start, end)
	}
}
