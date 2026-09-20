package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStagingSnapshotsBytesAndUnstage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TPARUN_STAGING_DIR", filepath.Join(t.TempDir(), "staging"))
	packagePath := filepath.Join(root, "tool_1.0_amd64.deb")
	buildSemanticTestDebForArchitecture(t, packagePath, "amd64")
	if err := stageCLI([]string{packagePath}); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadStagingManifest()
	if err != nil || len(manifest.Artifacts) != 1 {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	original, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(manifest.Artifacts[0].StagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, staged) {
		t.Fatal("staged bytes differ from source")
	}
	if err := os.WriteFile(packagePath, []byte("different bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyStagedArtifact(manifest.Artifacts[0]); err != nil {
		t.Fatal(err)
	}
	if err := unstageCLI([]string{packagePath}); err != nil {
		t.Fatal(err)
	}
	manifest, err = loadStagingManifest()
	if err != nil || len(manifest.Artifacts) != 0 {
		t.Fatalf("unstage manifest=%+v err=%v", manifest, err)
	}
	if _, err := os.Stat(packagePath); err != nil {
		t.Fatal(err)
	}
}

func TestStageIsTransactionalForInvalidInput(t *testing.T) {
	t.Setenv("TPARUN_STAGING_DIR", filepath.Join(t.TempDir(), "staging"))
	valid := filepath.Join(t.TempDir(), "valid.deb")
	buildSemanticTestDebForArchitecture(t, valid, "amd64")
	invalid := filepath.Join(t.TempDir(), "invalid.deb")
	if err := os.WriteFile(invalid, []byte("not a deb"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stageCLI([]string{valid, invalid}); err == nil {
		t.Fatal("invalid package was staged")
	}
	manifest, err := loadStagingManifest()
	if err != nil || len(manifest.Artifacts) != 0 {
		t.Fatalf("transaction left state: %+v err=%v", manifest, err)
	}
}
