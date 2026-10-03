package tpa

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRunnerCommand(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleBoundedUsesPrivateCopyAndPreservesSafePartialDelete(t *testing.T) {
	root := t.TempDir()
	commands := filepath.Join(root, "commands")
	if err := os.Mkdir(commands, 0755); err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(root, "repo")
	input := filepath.Join(repositoryRoot, "generations", "active")
	if err := os.MkdirAll(input, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "sentinel"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	keyHome := filepath.Join(root, "managed-key")
	if err := os.Mkdir(keyHome, 0700); err != nil {
		t.Fatal(err)
	}
	mountLog := filepath.Join(root, "mounts")
	writeRunnerCommand(t, filepath.Join(commands, "unshare"), `for i in 1 2 3 4 5 6; do shift; done
exec "$@"`)
	writeRunnerCommand(t, filepath.Join(commands, "mount"), `printf '%s\n' "$*" >> "$TPA_TEST_MOUNTS"
exit 0`)
	writeRunnerCommand(t, filepath.Join(commands, "umount"), `exit 0`)
	writeRunnerCommand(t, filepath.Join(commands, "prlimit"), `while [ "$1" != "--" ]; do shift; done
shift
exec "$@"`)
	writeRunnerCommand(t, filepath.Join(commands, "setpriv"), `while [ "$1" != "--" ]; do shift; done
shift
exec "$@"`)
	tpa := filepath.Join(root, "fake-tpa")
	writeRunnerCommand(t, tpa, `command=$1
shift
repository=
for arg do
  case "$arg" in -in=*) repository=${arg#-in=} ;; esac
done
[ -n "$repository" ] || exit 20
printf 'mutated' > "$repository/result"
if [ "$command" = delete ] && [ "${TPA_TEST_PARTIAL:-0}" = 1 ]; then
  echo 'tpa: package was unlisted, but artifact deletion failed: fixture' >&2
  exit 1
fi
echo lifecycle-ok`)
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TPA_TEST_MOUNTS", mountLog)
	runner := Runner{Bin: tpa, ProtectedPaths: []string{keyHome}}
	request := LifecycleRequest{Operation: LifecycleUnlist, Input: input, Output: filepath.Join(repositoryRoot, "generations", "candidate-1"), Package: "pkg", Version: "1", Architecture: "amd64", SigningHome: keyHome, SigningFingerprint: "DEADBEEF"}
	result, err := runner.LifecycleBounded(context.Background(), request, 1<<20, 4096)
	if err != nil {
		t.Fatalf("LifecycleBounded: %v", err)
	}
	if !strings.Contains(result.Output, "lifecycle-ok") || result.CleanupFailed {
		t.Fatalf("unexpected result: %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(input, "sentinel")); err != nil || string(data) != "original" {
		t.Fatalf("active input changed: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(request.Output, "result")); err != nil || string(data) != "mutated" {
		t.Fatalf("candidate result=%q err=%v", data, err)
	}
	mounts, err := os.ReadFile(mountLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mounts), keyHome) {
		t.Fatalf("signing home was masked: %s", mounts)
	}
	request.Operation = LifecycleDelete
	request.Output = filepath.Join(repositoryRoot, "generations", "candidate-2")
	t.Setenv("TPA_TEST_PARTIAL", "1")
	result, err = runner.LifecycleBounded(context.Background(), request, 1<<20, 4096)
	if err != nil || !result.CleanupFailed || !strings.Contains(result.Diagnostic, "artifact deletion failed") {
		t.Fatalf("partial delete result=%+v err=%v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(request.Output, "result")); err != nil || string(data) != "mutated" {
		t.Fatalf("partial candidate was not preserved: %q err=%v", data, err)
	}
}
