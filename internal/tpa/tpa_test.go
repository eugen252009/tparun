package tpa

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundedDiagnosticCapsRetainedOutput(t *testing.T) {
	var diagnostic boundedDiagnostic
	payload := bytes.Repeat([]byte("x"), maxDiagnosticBytes+1)
	if n, err := diagnostic.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if diagnostic.Len() != maxDiagnosticBytes || !diagnostic.truncated {
		t.Fatalf("diagnostic length=%d truncated=%t", diagnostic.Len(), diagnostic.truncated)
	}
	if !strings.HasSuffix(diagnostic.String(), "[diagnostic output truncated]") {
		t.Fatalf("missing truncation marker: %q", diagnostic.String()[maxDiagnosticBytes:])
	}
}

func TestCheckAndPublishProcessBoundary(t *testing.T) {
	d := t.TempDir()
	bin := filepath.Join(d, "fake-tpa")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = schema ] && exit 0\n[ \"$1\" = pack ] || exit 9\nwhile [ \"$#\" -gt 0 ]; do\n  [ \"$1\" = --atomic-publish ] && { shift; out=$1; break; }\n  shift\ndone\nmkdir -p \"$out\"\nprintf candidate > \"$out/marker\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := Runner{Bin: bin}
	if err := r.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(d, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(d, "output")
	if err := r.PublishBounded(context.Background(), PublishRequest{Input: input, Output: output}, 1<<20); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(output, "marker")); err != nil || string(data) != "candidate" {
		t.Fatalf("bounded candidate=%q err=%v", data, err)
	}
}
func TestBoundedPublisherHasNoSigningEnvironmentOrKeyHomeAccess(t *testing.T) {
	root := t.TempDir()
	keyHome := filepath.Join(root, "signing-home")
	keyDir := filepath.Join(keyHome, "private-keys-v1.d")
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(keyDir, "secret.key")
	if err := os.WriteFile(secret, []byte("test-only-signing-key-material"), 0600); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "args")
	envPath := filepath.Join(root, "env")
	t.Setenv("GNUPGHOME", keyHome)
	t.Setenv("GPG_AGENT_INFO", "/tmp/test-gpg-agent:1:1")
	t.Setenv("GPG_TTY", "/dev/pts/test")
	t.Setenv("TPARUN_GPG_FINGERPRINT", "test-only-fingerprint")
	t.Setenv("HOME", root)
	t.Setenv("FAKE_TPA_ARGS", argsPath)
	t.Setenv("FAKE_TPA_ENV", envPath)
	script := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$@" > "$FAKE_TPA_ARGS"
env > "$FAKE_TPA_ENV"
[ -z "${GNUPGHOME:-}" ]
[ -z "${GPG_AGENT_INFO:-}" ]
[ -z "${GPG_TTY:-}" ]
[ -z "${TPARUN_GPG_FINGERPRINT:-}" ]
[ "$HOME" = /nonexistent ]
[ ! -e %q ]
if umount %q 2>/dev/null; then exit 91; fi
[ ! -e %q ]
[ "$1" = pack ]
shift
[ "$1" = -in ]
shift 2
[ "$1" = --atomic-publish ]
shift
out=$1
mkdir -p "$out"
printf candidate > "$out/marker"
`, secret, keyHome, secret)
	bin := filepath.Join(root, "fake-tpa")
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidate")
	runner := Runner{Bin: bin, ProtectedPaths: []string{keyHome}}
	if err := runner.PublishBoundedWithInodes(context.Background(), PublishRequest{Input: input, Output: output}, 1<<20, 64); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "--gpg") || strings.Contains(string(args), "test-only-fingerprint") {
		t.Fatalf("signing option reached publisher: %q", args)
	}
	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GNUPGHOME=", "GPG_AGENT_INFO=", "GPG_TTY=", "TPARUN_GPG_FINGERPRINT="} {
		if strings.Contains(string(environment), name) {
			t.Fatalf("signing environment %s reached publisher", name)
		}
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "test-only-signing-key-material" {
		t.Fatalf("protected key home changed: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(output, "marker")); err != nil || string(data) != "candidate" {
		t.Fatalf("candidate output missing: %q %v", data, err)
	}
}

func TestMissingExecutable(t *testing.T) {
	err := (Runner{Bin: "/does/not/exist"}).Check(context.Background())
	if err == nil {
		t.Fatal("expected validation failure")
	}
}
