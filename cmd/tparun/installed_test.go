package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverInstalledRepositoriesParsesOwnedAndUnmanagedSources(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sources.list.d")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dir, "tparun-"+id+".list"), []byte("# managed-by: tparun\ndeb [signed-by=/etc/apt/keyrings/x.gpg] https://tpa.run/r/"+id+"/ stable main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor.list"), []byte("deb https://tpa.run/r/abcdefabcdefabcdefabcdefabcdefab/ stable main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.sources"), []byte("Types: deb\nURIs: https://tpa.lupricht.net/r/"+id+"/\nSuites: stable\nComponents: main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := discoverInstalledRepositories(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries=%d, want 3", len(entries))
	}
	var managed, unmanaged bool
	for _, entry := range entries {
		managed = managed || entry.Managed
		unmanaged = unmanaged || !entry.Managed
	}
	if !managed || !unmanaged {
		t.Fatalf("classification managed=%v unmanaged=%v", managed, unmanaged)
	}
	for _, entry := range entries {
		if entry.RepositoryID == id && strings.Contains(entry.SourceFile, "vendor") {
			t.Fatal("unmanaged source was treated as repository-owned")
		}
	}
}
