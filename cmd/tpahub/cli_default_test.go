package main

import "testing"

func TestCLIUsesOfficialServerByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := loadCLI().Server; got != defaultServer {
		t.Fatalf("default server=%q, want %q", got, defaultServer)
	}
}

func TestRepositoryCompatibilityCommandsNormalizeToFlatCommands(t *testing.T) {
	for _, input := range [][]string{
		{"repo", "add", "https://tpa.run/0123456789abcdef0123456789abcdef"},
		{"repo", "remove", "https://tpa.run/0123456789abcdef0123456789abcdef"},
		{"repo", "show", "https://tpa.run/0123456789abcdef0123456789abcdef"},
		{"repo", "list"},
		{"repo", "create", "demo"},
	} {
		got := normalizeCLICommand(input)
		if got[0] == "repo" {
			t.Fatalf("%v did not normalize: %v", input, got)
		}
	}
	if got := normalizeCLICommand([]string{"repo", "token", "list"}); got[0] != "repo" {
		t.Fatalf("token namespace unexpectedly normalized: %v", got)
	}
}

func TestCLIEnvironmentServerOverridesConfigDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TPAHUB_SERVER", "http://127.0.0.1:8080/")
	if got := loadCLI().Server; got != "http://127.0.0.1:8080" {
		t.Fatalf("environment server=%q", got)
	}
}
