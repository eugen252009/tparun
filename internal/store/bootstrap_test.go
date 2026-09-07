package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

func TestGlobalBootstrapRepositorySingletonAndReassignment(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateAccount(ctx, Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAccountClass(ctx, id, AccountClassInternal); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRepository(ctx, Repository{ID: "repo-" + id, AccountID: id, Name: id, Visibility: "public", CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnableGlobalBootstrap(ctx, "repo-a", "operator", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableGlobalBootstrap(ctx, "repo-b", "operator", "2026-01-01T00:00:01Z"); err == nil {
		t.Fatal("second bootstrap assignment succeeded")
	}
	b, ok, err := s.GlobalBootstrapRepository(ctx)
	if err != nil || !ok || b.RepositoryID != "repo-a" {
		t.Fatalf("bootstrap=%+v ok=%v err=%v", b, ok, err)
	}
	if _, err := s.db.Exec(`DELETE FROM repositories WHERE id='repo-a'`); err == nil {
		t.Fatal("selected bootstrap repository deletion succeeded")
	}
	if err := s.DisableGlobalBootstrap(ctx, "operator", "2026-01-01T00:00:02Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableGlobalBootstrap(ctx, "repo-b", "operator", "2026-01-01T00:00:03Z"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM global_bootstrap_repository`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestGlobalBootstrapRequiresActiveInternalServiceAccount(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateAccount(ctx, Account{ID: "ordinary", Email: "ordinary@example.invalid", PasswordHash: "x", Status: AccountStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, Repository{ID: "ordinary-repo", AccountID: "ordinary", Name: "r", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableGlobalBootstrap(ctx, "ordinary-repo", "operator", "now"); err == nil {
		t.Fatal("ordinary account accepted")
	}
	if err := s.SetAccountClass(ctx, "ordinary", AccountClassInternal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE accounts SET status='suspended' WHERE id='ordinary'`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableGlobalBootstrap(ctx, "ordinary-repo", "operator", "now"); err == nil {
		t.Fatal("suspended account accepted")
	}
}

func TestGlobalBootstrapCreationAtomicOnMigrationFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE tpahub_schema SET version=2; DROP TABLE global_bootstrap_repository`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	migrationFault = func(stage string) error {
		if stage == "before-bootstrap-registry" {
			return context.Canceled
		}
		return nil
	}
	defer func() { migrationFault = nil }()
	if err = Migrate(dir); err == nil {
		t.Fatal("injected bootstrap migration failure accepted")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version=%d", version)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='global_bootstrap_repository'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("bootstrap table survived failed migration")
	}
}

func TestGlobalBootstrapConcurrentAssignment(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateAccount(ctx, Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: AccountStatusActive}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAccountClass(ctx, id, AccountClassInternal); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRepository(ctx, Repository{ID: "repo-" + id, AccountID: id, Name: id, Visibility: "public", CreatedAt: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"repo-a", "repo-b"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); results <- s.EnableGlobalBootstrap(ctx, id, "operator", id) }(id)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM global_bootstrap_repository`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
