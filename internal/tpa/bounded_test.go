package tpa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func boundedWriterScript(t *testing.T, bytes int) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "writer")
	body := `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do if [ "$1" = "--atomic-publish" ]; then shift; out="$1"; fi; shift; done
mkdir -p "$out"
dd if=/dev/zero of="$out/blob" bs=1 count=` + fmt.Sprint(bytes) + ` status=none
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestPublishBoundedRejectsOverrunAndAllowsExactBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		wantErr bool
	}{{"exact", 1 << 20, false}, {"overrun", (1 << 20) + 1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			err := (Runner{Bin: boundedWriterScript(t, tc.size)}).PublishBounded(context.Background(), PublishRequest{Input: t.TempDir(), Output: out}, 1<<20)
			if tc.wantErr {
				if err == nil {
					t.Fatal("overrun accepted")
				}
				if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
					t.Fatalf("overrun output remains: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(out, "blob"))
			if err != nil || info.Size() != 1<<20 {
				t.Fatalf("exact output=%v err=%v", info, err)
			}
		})
	}
}

func TestPublishBoundedWithInodesUsesExplicitCount(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runner := Runner{Bin: boundedWriterScript(t, 128)}
	if err := runner.PublishBoundedWithInodes(context.Background(), PublishRequest{Input: t.TempDir(), Output: out}, 1<<20, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "blob")); err != nil {
		t.Fatal(err)
	}
	if err := runner.PublishBoundedWithInodes(context.Background(), PublishRequest{Input: t.TempDir(), Output: filepath.Join(t.TempDir(), "invalid")}, 1<<20, 0); err == nil {
		t.Fatal("zero inode count accepted")
	}
}
