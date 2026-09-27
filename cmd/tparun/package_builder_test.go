package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestDebianPackageBuilderMakesTPAHardDependency(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "tparun.deb")
	packageVersion := "0.1.5"
	cmd := exec.Command("bash", filepath.Join(root, "packaging/tparun/build.sh"), outputPath)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"TPARUN_VERSION="+packageVersion,
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
	if fields["Recommends"] != "mema (>= 0.4.1)" {
		t.Errorf("generated Recommends=%q, want mema (>= 0.4.1)", fields["Recommends"])
	}
	if hasDebianDependency(fields["Depends"], "systemd") {
		t.Errorf("generated Depends unexpectedly adds systemd: %q", fields["Depends"])
	}
	if fields["Version"] != packageVersion {
		t.Errorf("package Version=%q, want %q", fields["Version"], packageVersion)
	}
	packageRoot := t.TempDir()
	if output, err := exec.Command("dpkg-deb", "-x", outputPath, packageRoot).CombinedOutput(); err != nil {
		t.Fatalf("extract package: %v\n%s", err, output)
	}
	runtimeVersion, err := exec.Command(filepath.Join(packageRoot, "usr/bin/tparun"), "version").Output()
	if err != nil {
		t.Fatalf("run packaged version command: %v", err)
	}
	if strings.TrimSpace(string(runtimeVersion)) != fields["Version"] {
		t.Fatalf("package/runtime version mismatch: package=%q runtime=%q", fields["Version"], strings.TrimSpace(string(runtimeVersion)))
	}
	if _, err := os.Stat(filepath.Join(packageRoot, "usr/lib/systemd/system/tparun.service")); err != nil {
		t.Fatalf("canonical packaged systemd unit missing: %v", err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(packageRoot, "etc/mema/recipe/manage/tparun.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Data []struct {
			Exclude []string `json:"exclude"`
		} `json:"data"`
		Identity []struct {
			ExcludeSockets []string `json:"exclude_sockets"`
		} `json:"identity"`
		Backup struct {
			Format     string `json:"format"`
			Encryption struct {
				Type      string `json:"type"`
				Recipient string `json:"recipient"`
			} `json:"encryption"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "/usr/lib/systemd/system/tparun.service" {
		t.Fatalf("manifest systemd resource does not match package path: %#v", manifest.Files)
	}
	if len(manifest.Data) != 1 || len(manifest.Data[0].Exclude) != 1 || manifest.Data[0].Exclude[0] != "./.tpahub.lock" {
		t.Fatalf("data lock exclusion is missing: %#v", manifest.Data)
	}
	wantSockets := []string{"S.gpg-agent", "S.gpg-agent.browser", "S.gpg-agent.extra", "S.gpg-agent.ssh"}
	if len(manifest.Identity) != 1 || strings.Join(manifest.Identity[0].ExcludeSockets, "\x00") != strings.Join(wantSockets, "\x00") {
		t.Fatalf("GnuPG socket exclusions=%#v, want %#v", manifest.Identity, wantSockets)
	}
	if manifest.Backup.Format != "mema-snapshot-v2" || manifest.Backup.Encryption.Type != "gpg" || manifest.Backup.Encryption.Recipient != "${BACKUP_RECIPIENT}" {
		t.Fatalf("backup manifest is not v2/GPG encrypted: %#v", manifest.Backup)
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
