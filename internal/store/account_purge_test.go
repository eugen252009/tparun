package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func testPurgeGrant(accountID string, now time.Time) commerce.Grant {
	return commerce.Grant{CustomerID: commerce.CustomerID(accountID), ProductID: tpahubproduct.ProductID, CapabilityID: tpahubproduct.CapabilityRepositoryCreate, OfferID: "test", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Source: "local", CreatedAt: now, UpdatedAt: now}
}

func TestPurgeAccountIsCanonicalAndIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.CreateAccount(ctx, Account{ID: "purge", Email: "purge@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), LifecycleKind: AccountLifecycleEphemeral, MembershipExpiresAt: now.Add(-time.Hour).Format(time.RFC3339Nano), PurgeAfter: now.Add(-time.Minute).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, Repository{ID: "purge-repo", AccountID: "purge", Name: "repo", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, Session{ID: "purge-session", AccountID: "purge", TokenPrefix: "purge"}, HashToken("purge-token")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGrant(ctx, testPurgeGrant("purge", now)); err != nil {
		t.Fatal(err)
	}
	ids, err := s.PurgeAccount(ctx, "purge", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "purge-repo" {
		t.Fatalf("repositories=%v", ids)
	}
	if _, err := s.AccountByID(ctx, "purge"); err == nil {
		t.Fatal("account survived purge")
	}
	if _, err := s.Repository(ctx, "purge-repo"); err == nil {
		t.Fatal("repository survived purge")
	}
	if _, err := s.SessionPrincipalByToken(ctx, HashToken("purge-token")); err == nil {
		t.Fatal("session survived purge")
	}
	if _, err := s.PurgeAccount(ctx, "purge", now); err != nil {
		t.Fatalf("repeat purge: %v", err)
	}
}

func TestPurgeAccountRevalidatesRenewalAndProtectsInternal(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.CreateAccount(ctx, Account{ID: "renew-before-purge", Email: "renew-before-purge@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleCustomer, MembershipExpiresAt: now.Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano), PurgeAfter: now.Add(-time.Minute).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembershipExpiry(ctx, "renew-before-purge", now.Add(24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeAccount(ctx, "renew-before-purge", now); err == nil {
		t.Fatal("renewed account was purged")
	}
	if err := s.CreateAccount(ctx, Account{ID: "internal-purge", Email: "internal-purge@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: "now", LifecycleKind: AccountLifecycleInternal, PurgeAfter: now.Add(-time.Hour).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeAccount(ctx, "internal-purge", now); err == nil {
		t.Fatal("internal account was purged")
	}
}
