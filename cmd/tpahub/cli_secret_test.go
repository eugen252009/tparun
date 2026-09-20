package main

import (
	"strings"
	"testing"
)

func TestRedactSecretsRemovesSessionAndReaderTokens(t *testing.T) {
	input := `session=tph_s_secret-value bearer=Authorization: Bearer tph_s_other-value reader=tph_r_reader-value`
	got := redactSecrets(input)
	for _, secret := range []string{"tph_s_secret-value", "tph_s_other-value", "tph_r_reader-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q leaked in %q", secret, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %q", got)
	}
}

func TestNonInteractiveSSHLoginRequiresConfiguredEmail(t *testing.T) {
	t.Setenv("TPAHUB_EMAIL", "")
	_, err := sshLoginCLI(cliConfig{Server: "https://tpa.run"}, "/does/not/exist", true)
	if err == nil || !strings.Contains(err.Error(), "TPAHUB_EMAIL") {
		t.Fatalf("expected explicit non-interactive email error, got %v", err)
	}
}
