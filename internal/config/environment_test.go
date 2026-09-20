package config

import "testing"

func TestCanonicalEnvReadsTPARUNName(t *testing.T) {
	t.Setenv("TPARUN_SERVER", "https://new.example")
	if got := CanonicalEnv("TPARUN_SERVER"); got != "https://new.example" {
		t.Fatalf("canonical environment value=%q", got)
	}
}
