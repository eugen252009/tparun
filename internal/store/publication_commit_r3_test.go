package store

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestCommitPublicationAtomicallyRechecksAuthorizationAndJobState(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	grant := commerce.Grant{CustomerID: "account", ProductID: tpahubproduct.ProductID, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "local", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 100}, Source: "local", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateAccount(ctx, Account{ID: "account", Email: "account@example.invalid", PasswordHash: "x", Status: AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}, grant); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRepository(ctx, Repository{ID: "repo", AccountID: "account", Name: "repo", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateJob(ctx, Job{ID: "job", RepositoryID: "repo", State: "running", IncomingPath: "incoming", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	grant.Enabled = false
	if err = s.PutGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	at := now.Format(time.RFC3339Nano)
	// Disabling the historical grant does not remove the current Nano floor.
	if err = s.CommitPublication(ctx, "repo", "job", string(tpahubproduct.ProductID), string(tpahubproduct.CapabilityRepositoryPublish), at, at); err != nil {
		t.Fatalf("Nano floor did not authorize publication: %v", err)
	}
	grant.Enabled = true
	if err = s.PutGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateJob(ctx, Job{ID: "terminal-job", RepositoryID: "repo", State: "running", IncomingPath: "incoming", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, "terminal-job", "failed", "cancelled before commit"); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitPublication(ctx, "repo", "terminal-job", string(tpahubproduct.ProductID), string(tpahubproduct.CapabilityRepositoryPublish), at, at); err == nil {
		t.Fatal("publication committed after job left running state")
	}
	if err = s.CreateJob(ctx, Job{ID: "success-job", RepositoryID: "repo", State: "running", IncomingPath: "incoming", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitPublication(ctx, "repo", "success-job", string(tpahubproduct.ProductID), string(tpahubproduct.CapabilityRepositoryPublish), at, at); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveGeneration(ctx, "repo")
	job, jobErr := s.Job(ctx, "success-job")
	if err != nil || jobErr != nil || active != "success-job" || job.State != "succeeded" {
		t.Fatalf("active=%q job=%+v errors=%v/%v", active, job, err, jobErr)
	}
}
