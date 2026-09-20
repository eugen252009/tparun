package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestArtifactPublicationCopiesAreIndependentFiles(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "incoming")
	a := filepath.Join(root, "repo-a")
	b := filepath.Join(root, "repo-b")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("identical package bytes")
	if err := os.WriteFile(filepath.Join(src, "same.deb"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyDebs(src, a, true); err != nil {
		t.Fatal(err)
	}
	if err := copyDebs(src, b, true); err != nil {
		t.Fatal(err)
	}
	left, right := filepath.Join(a, "same.deb"), filepath.Join(b, "same.deb")
	li, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	ri, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	ls, lok := li.Sys().(*syscall.Stat_t)
	rs, rok := ri.Sys().(*syscall.Stat_t)
	if !lok || !rok {
		t.Fatal("filesystem metadata did not expose device/inode")
	}
	if ls.Dev == rs.Dev && ls.Ino == rs.Ino {
		t.Fatal("identical artifacts unexpectedly share an inode")
	}
	if ls.Nlink != 1 || rs.Nlink != 1 {
		t.Fatalf("unexpected link counts: %d,%d", ls.Nlink, rs.Nlink)
	}
	if string(payload) != string(readAuditFile(t, right)) {
		t.Fatal("copied artifact content changed")
	}
}

func TestPurgeAccountRemovesOnlyTheOwnedArtifactNamespace(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, fixture := range []struct{ account, email, repo string }{{"artifact-a", "artifact-a@example.invalid", "artifact-repo-a"}, {"artifact-b", "artifact-b@example.invalid", "artifact-repo-b"}} {
		if err := s.db.CreateAccount(ctx, store.Account{ID: fixture.account, Email: fixture.email, PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), LifecycleKind: store.AccountLifecycleEphemeral, MembershipExpiresAt: now.Add(-time.Hour).Format(time.RFC3339Nano), PurgeAfter: now.Add(-time.Minute).Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		if err := s.db.CreateRepository(ctx, store.Repository{ID: fixture.repo, AccountID: fixture.account, Name: fixture.repo, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		for _, root := range []string{filepath.Join(s.cfg.DataDir, "repos", fixture.repo), filepath.Join(s.cfg.DataDir, "artifacts", fixture.repo)} {
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "same.deb"), []byte("identical"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.PurgeAccount(ctx, "artifact-a", now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "repos", "artifact-repo-a")); !os.IsNotExist(err) {
		t.Fatalf("purged repository remains: %v", err)
	}
	bPath := filepath.Join(s.cfg.DataDir, "repos", "artifact-repo-b", "same.deb")
	if got := string(readAuditFile(t, bPath)); got != "identical" {
		t.Fatalf("remaining artifact=%q", got)
	}
	if err := s.PurgeAccount(ctx, "artifact-b", now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bPath); !os.IsNotExist(err) {
		t.Fatalf("final artifact remains: %v", err)
	}
}

func readAuditFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
