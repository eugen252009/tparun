package main

import "testing"

func TestCLIUsesOfficialServerByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := loadCLI().Server; got != defaultServer {
		t.Fatalf("default server=%q, want %q", got, defaultServer)
	}
}
