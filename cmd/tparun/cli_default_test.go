package main

import "testing"

func TestCLIMissingServerIsExplicit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := loadCLI(); got.err == nil {
		t.Fatal("missing server was accepted")
	}
}

func TestCLIEnvironmentServerOverridesConfigDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TPARUN_SERVER", "http://127.0.0.1:8080/")
	if got := loadCLI().Server; got != "http://127.0.0.1:8080" {
		t.Fatalf("environment server=%q", got)
	}
}
