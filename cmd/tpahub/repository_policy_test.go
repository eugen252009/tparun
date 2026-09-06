package main

import (
	"context"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestRepositoryPolicySeparatesOwnershipFromEntitlement(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateAccount(ctx, store.Account{ID: "b", Email: "b@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "owned", AccountID: "a", Name: "owned", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.repositoryPolicy().AuthorizePublication(ctx, "b", "owned"); err == nil {
		t.Fatal("foreign account was authorized despite having no ownership")
	}
	if _, _, err := s.repositoryPolicy().AuthorizePublication(ctx, "a", "owned"); err != nil {
		t.Fatalf("owner with effective Nano entitlement was denied: %v", err)
	}
}

func TestRepositoryPolicyQuarantinesUnownedRepository(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "legacy", Name: "legacy", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.repositoryPolicy().AuthorizeMutation(ctx, "a", "legacy"); err == nil {
		t.Fatal("unowned repository was mutable")
	}
	if err := s.repositoryPolicy().AuthorizeCreation(ctx, ""); err == nil {
		t.Fatal("repository creation accepted an empty owner")
	}
}
