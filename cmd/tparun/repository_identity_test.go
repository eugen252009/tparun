package main

import (
	"context"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestRepositoryAndUsernameGrammar(t *testing.T) {
	for _, name := range []string{"mema", "mema-bun", "dev-tools", "repo2", "2d-tools", "a"} {
		if !validRepositoryName(name) {
			t.Errorf("valid repository name rejected: %q", name)
		}
	}
	for _, name := range []string{"Mema", "my_repo", "my.repo", "my repo", "mämä", "🔥repo", "-repo", "repo-", "../repo", "repo/foo", "repo%2Ffoo"} {
		if validRepositoryName(name) {
			t.Errorf("invalid repository name accepted: %q", name)
		}
	}
	for _, email := range []string{"eugen@example.com", "eu.lupricht@gmail.com", "weird+tag@example.com"} {
		if !validUsername(accountUsername(email)) {
			t.Errorf("derived username is unsafe for %q: %q", email, accountUsername(email))
		}
	}
}

func TestCanonicalRepositoryResolutionUsesImmutableID(t *testing.T) {
	s, h := testServer(t)
	const id = "0123456789abcdef0123456789abcdef"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: "dev-tools", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	got, err := resolveCanonicalRepository(cliConfig{Server: h.URL}, "a", "dev-tools")
	if err != nil || got != id {
		t.Fatalf("resolved=%q err=%v", got, err)
	}
}

func TestCanonicalRepositoryURLParsing(t *testing.T) {
	ref, err := ParseRepoURL("https://tpa.run/r/eugen/dev-tools")
	if err != nil || ref.Username != "eugen" || ref.Name != "dev-tools" || ref.ID != "" {
		t.Fatalf("canonical parse=%+v err=%v", ref, err)
	}
	if _, err := ParseRepoURL("https://tpa.run/r/Eugen/dev-tools"); err == nil {
		t.Fatal("unsafe username accepted")
	}
	if _, err := ParseRepoURL("https://tpa.run/r/eugen/my_repo"); err == nil {
		t.Fatal("unsafe repository name accepted")
	}
	legacy, err := ParseRepoURL("https://tpa.run/0123456789abcdef0123456789abcdef")
	if err != nil || legacy.ID == "" || legacy.Username != "" || legacy.Name != "" {
		t.Fatalf("internal UUID parse=%+v err=%v", legacy, err)
	}
}
