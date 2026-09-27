package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
	appversion "github.com/eugen252009/tpahub/internal/version"
)

func TestDebianPackageBuilderMakesTPAHardDependency(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "tparun.deb")
	cmd := exec.Command("bash", filepath.Join(root, "packaging/tparun/build.sh"), outputPath)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"TPARUN_VERSION="+appversion.Current,
		"TPARUN_BUILD_REVISION=package-metadata-test",
		"SOURCE_DATE_EPOCH=0",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Debian package: %v\n%s", err, output)
	}

	control, err := exec.Command("dpkg-deb", "-f", outputPath).Output()
	if err != nil {
		t.Fatalf("read generated package control metadata: %v", err)
	}
	fields := parseDebianControlFields(string(control))
	for _, dependency := range []string{"adduser", "ca-certificates", "gnupg", "tpa"} {
		if !hasDebianDependency(fields["Depends"], dependency) {
			t.Errorf("generated Depends %q does not include %q", fields["Depends"], dependency)
		}
	}
	if hasDebianDependency(fields["Recommends"], "tpa") {
		t.Errorf("generated Recommends still includes tpa: %q", fields["Recommends"])
	}
	if hasDebianDependency(fields["Depends"], "systemd") {
		t.Errorf("generated Depends unexpectedly adds systemd: %q", fields["Depends"])
	}
	if fields["Version"] != appversion.Current {
		t.Errorf("package Version=%q, want %q", fields["Version"], appversion.Current)
	}
	if fields["Mema-Schema"] != strconv.Itoa(store.CurrentSchemaVersion()) {
		t.Errorf("package Mema-Schema=%q, want %d", fields["Mema-Schema"], store.CurrentSchemaVersion())
	}
}

func parseDebianControlFields(control string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(control, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if ok {
			fields[name] = strings.TrimSpace(value)
		}
	}
	return fields
}

func hasDebianDependency(list, packageName string) bool {
	for _, clause := range strings.Split(list, ",") {
		fields := strings.Fields(strings.TrimSpace(clause))
		if len(fields) == 0 {
			continue
		}
		name := strings.SplitN(fields[0], ":", 2)[0]
		if name == packageName {
			return true
		}
	}
	return false
}
