package main

import "testing"

func TestCLIUsesOfficialServerByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := loadCLI().Server; got != defaultServer {
		t.Fatalf("default server=%q, want %q", got, defaultServer)
	}
}

func TestCLIEnvironmentServerOverridesConfigDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TPAHUB_SERVER", "http://127.0.0.1:8080/")
	if got := loadCLI().Server; got != "http://127.0.0.1:8080" {
		t.Fatalf("environment server=%q", got)
	}
}
