package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

// RepositoryPolicy is the TPA Hub authority for repository ownership and
// publication authorization. Store exposes durable repository facts and
// commits an already-authorized publication; it does not interpret TPA
// entitlements or ownership.
type RepositoryPolicy struct{ server *server }

var ErrRepositoryOwnerMismatch = errors.New("repository owner mismatch")

func (s *server) repositoryPolicy() RepositoryPolicy { return RepositoryPolicy{server: s} }

func (p RepositoryPolicy) Resolve(ctx context.Context, id string) (store.Repository, error) {
	r, err := p.server.db.Repository(ctx, id)
	if err != nil {
		return store.Repository{}, fmt.Errorf("repository not found: %w", err)
	}
	if strings.TrimSpace(r.AccountID) == "" {
		return store.Repository{}, fmt.Errorf("repository owner is missing (legacy/quarantined)")
	}
	if _, err = p.server.db.AccountByID(ctx, r.AccountID); err != nil {
		return store.Repository{}, fmt.Errorf("repository owner is invalid: %w", err)
	}
	return r, nil
}

func (p RepositoryPolicy) AuthorizeMutation(ctx context.Context, accountID, repositoryID string) (store.Repository, error) {
	r, err := p.Resolve(ctx, repositoryID)
	if err != nil {
		return store.Repository{}, err
	}
	if strings.TrimSpace(accountID) == "" || r.AccountID != accountID {
		return store.Repository{}, ErrRepositoryOwnerMismatch
	}
	a, err := p.server.db.AccountByID(ctx, accountID)
	if err != nil {
		return store.Repository{}, fmt.Errorf("repository owner is invalid: %w", err)
	}
	if a.Status != store.AccountStatusActive {
		return store.Repository{}, fmt.Errorf("repository account is not active")
	}
	return r, nil
}

func (p RepositoryPolicy) AuthorizePublication(ctx context.Context, accountID, repositoryID string) (store.Repository, commerce.Grant, error) {
	if !p.server.accountAllowsProductWrites(ctx, accountID) {
		return store.Repository{}, commerce.Grant{}, fmt.Errorf("membership is in retention grace")
	}
	r, err := p.AuthorizeMutation(ctx, accountID, repositoryID)
	if err != nil {
		return store.Repository{}, commerce.Grant{}, err
	}
	grant, err := p.server.grantService().GetGrant(ctx, commerce.CustomerID(accountID), tpahubproduct.ProductID, CapabilityRepositoryPublish, p.server.currentTime())
	if err != nil {
		return store.Repository{}, commerce.Grant{}, fmt.Errorf("publication not authorized: %w", err)
	}
	return r, grant, nil
}

// AuthorizeCreation verifies the authenticated account before any repository
// directory or durable repository row is created.
func (p RepositoryPolicy) AuthorizeCreation(ctx context.Context, accountID string) error {
	if strings.TrimSpace(accountID) == "" {
		return fmt.Errorf("repository owner is required")
	}
	a, err := p.server.db.AccountByID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("repository owner is invalid: %w", err)
	}
	if a.Status != store.AccountStatusActive {
		return fmt.Errorf("repository account is not active")
	}
	if !p.server.accountAllowsProductWrites(ctx, accountID) {
		return fmt.Errorf("membership is in retention grace")
	}
	if _, err = p.server.grantService().GetGrant(ctx, commerce.CustomerID(accountID), tpahubproduct.ProductID, CapabilityRepositoryCreate, p.server.currentTime()); err != nil {
		return fmt.Errorf("repository creation not authorized: %w", err)
	}
	return nil
}
