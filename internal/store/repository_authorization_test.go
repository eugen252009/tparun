package store

import (
	"context"
	"testing"
)

func TestBootstrapOwnershipTransferAndDelegation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateAccount(ctx, Account{ID: "service", Email: "service@example.invalid", PasswordHash: "x", Status: AccountStatusActive, LifecycleKind: AccountLifecycleInternal}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountClass(ctx, "service", AccountClassInternal); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAccount(ctx, Account{ID: "customer", Email: "customer@example.invalid", PasswordHash: "x", Status: AccountStatusActive, LifecycleKind: AccountLifecycleCustomer}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, Repository{ID: "bootstrap", AccountID: "service", Name: "bootstrap", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableGlobalBootstrap(ctx, "bootstrap", "operator", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.TransferRepositoryOwner(ctx, "bootstrap", "customer", "operator", "2026-01-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepositoryDelegation(ctx, "service", "bootstrap", RepositoryPublishDelegation, "operator", "2026-01-01T00:02:00Z", true); err != nil {
		t.Fatal(err)
	}
	repo, err := s.Repository(ctx, "bootstrap")
	if err != nil || repo.AccountID != "customer" || repo.Name != "bootstrap" || repo.Visibility != "public" {
		t.Fatalf("repo=%+v err=%v", repo, err)
	}
	bootstrap, ok, err := s.GlobalBootstrapRepository(ctx)
	if err != nil || !ok || bootstrap.AccountID != "service" || bootstrap.RepositoryID != "bootstrap" {
		t.Fatalf("bootstrap=%+v ok=%v err=%v", bootstrap, ok, err)
	}
	delegated, err := s.HasRepositoryDelegation(ctx, "service", "bootstrap", RepositoryPublishDelegation)
	if err != nil || !delegated {
		t.Fatalf("delegated=%v err=%v", delegated, err)
	}
	if err := s.SetRepositoryDelegation(ctx, "service", "bootstrap", RepositoryPublishDelegation, "operator", "2026-01-01T00:03:00Z", false); err != nil {
		t.Fatal(err)
	}
	delegated, err = s.HasRepositoryDelegation(ctx, "service", "bootstrap", RepositoryPublishDelegation)
	if err != nil || delegated {
		t.Fatalf("revoked delegated=%v err=%v", delegated, err)
	}
}

func TestBootstrapOwnershipTransferRejectsNonexistentOwnerAtomically(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"service", "customer"} {
		if err := s.CreateAccount(ctx, Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive, LifecycleKind: AccountLifecycleCustomer}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAccountClass(ctx, "service", AccountClassInternal); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, Repository{ID: "bootstrap", AccountID: "service", Name: "bootstrap", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableGlobalBootstrap(ctx, "bootstrap", "operator", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.TransferRepositoryOwner(ctx, "bootstrap", "missing", "operator", "later"); err == nil {
		t.Fatal("invalid owner transfer succeeded")
	}
	repo, err := s.Repository(ctx, "bootstrap")
	if err != nil || repo.AccountID != "service" {
		t.Fatalf("repo=%+v err=%v", repo, err)
	}
}
