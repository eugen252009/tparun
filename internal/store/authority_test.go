package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestCompatibilityProjectionCannotChangeEffectiveGrant(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	grants := tpahubproduct.DefaultGrants(now)
	if err := s.CreateAccount(context.Background(), Account{ID: "authority", Email: "authority@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}, grants...); err != nil {
		t.Fatal(err)
	}
	wantPublish, err := s.GetGrant(context.Background(), "authority", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, now)
	if err != nil {
		t.Fatal(err)
	}
	wantDownload, err := s.GetGrant(context.Background(), "authority", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryDownload, now)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		row  *AccountEntitlement
	}{
		{name: "missing"},
		{name: "stale-low", row: &AccountEntitlement{AccountID: "authority", PlanCode: "nano", StartedAt: now.Format(time.RFC3339Nano), ValidFrom: now.Format(time.RFC3339Nano), ValidUntil: now.Add(time.Hour).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), Source: "legacy"}},
		{name: "stale-high", row: &AccountEntitlement{AccountID: "authority", PlanCode: "pro", StartedAt: now.Format(time.RFC3339Nano), ValidFrom: now.Add(-time.Hour).Format(time.RFC3339Nano), ValidUntil: now.Add(time.Hour).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), Source: "legacy"}},
		{name: "conflicting", row: &AccountEntitlement{AccountID: "authority", PlanCode: "standard", StartedAt: "invalid", ValidFrom: "invalid", ValidUntil: "invalid", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), Source: "legacy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.db.Exec(`DELETE FROM account_entitlements WHERE account_id=?`, "authority"); err != nil {
				t.Fatal(err)
			}
			if tc.row != nil {
				if err := s.SetLegacyTPAEntitlement(context.Background(), *tc.row); err != nil {
					t.Fatal(err)
				}
			}
			gotPublish, err := s.GetGrant(context.Background(), "authority", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, now)
			if err != nil {
				t.Fatal(err)
			}
			gotDownload, err := s.GetGrant(context.Background(), "authority", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryDownload, now)
			if err != nil {
				t.Fatal(err)
			}
			if gotPublish != wantPublish || gotDownload != wantDownload {
				t.Fatalf("projection altered grants: publish=%+v/%+v download=%+v/%+v", gotPublish, wantPublish, gotDownload, wantDownload)
			}
		})
	}
}
