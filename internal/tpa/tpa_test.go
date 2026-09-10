package tpa

import (
	"bytes"
	"context"
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
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = schema ] && exit 0\n[ \"$1\" = pack ] && exit 0\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := Runner{Bin: bin}
	if err := r.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(context.Background(), PublishRequest{Input: "in", Output: "out"}); err != nil {
		t.Fatal(err)
	}
}
func TestMissingExecutable(t *testing.T) {
	err := (Runner{Bin: "/does/not/exist"}).Check(context.Background())
	if err == nil {
		t.Fatal("expected validation failure")
	}
}
