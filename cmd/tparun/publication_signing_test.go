package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/tpa"
)

func TestServerSignedCandidateMatchesTPASignedRepositorySemantics(t *testing.T) {
	bin := buildCurrentTPA(t)
	signing := testCandidateSigningConfig(t)
	work := filepath.Join(t.TempDir(), "input")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	buildSemanticTestDeb(t, filepath.Join(work, "package.deb"))
	root := t.TempDir()
	tpaSigned := filepath.Join(root, "tpa-signed")
	serverSigned := filepath.Join(root, "server-signed")
	serverConfig := config.Config{TPABin: bin, GPGHome: signing.GPGHome, GPGFingerprint: signing.GPGFingerprint, PublishTimeout: time.Minute}
	cmd := exec.Command(bin, "pack", "-in", work, "--atomic-publish", tpaSigned, "-gpg", signing.GPGFingerprint)
	cmd.Env = serverGPGEnvironment(serverConfig)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("TPA signed build failed: %v: %s", err, output)
	}
	runner := publicationTPARunner(serverConfig)
	if err := runner.PublishBoundedWithInodes(context.Background(), tpa.PublishRequest{Input: work, Output: serverSigned}, 16<<20, 4096); err != nil {
		t.Fatalf("unsigned bounded TPA build failed: %v", err)
	}
	if err := verifyUnsignedPublicationCandidate(serverSigned, work, serverConfig); err != nil {
		t.Fatalf("unsigned candidate rejected before signing: %v", err)
	}
	if err := signPublicationCandidate(context.Background(), serverSigned, serverConfig); err != nil {
		t.Fatalf("authoritative server signing failed: %v", err)
	}
	for name, path := range map[string]string{"TPA": tpaSigned, "server": serverSigned} {
		if err := verifyPublicationCandidate(path, work, serverConfig); err != nil {
			t.Fatalf("%s signed repository rejected: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(path, "dists", "stable", "Release.gpg")); !os.IsNotExist(err) {
			t.Fatalf("%s output has unexpected detached signature: %v", name, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(tpaSigned, "dists", "stable", "main", "binary-amd64", "Packages")); err != nil {
		t.Fatal(err)
	} else if want, readErr := os.ReadFile(filepath.Join(serverSigned, "dists", "stable", "main", "binary-amd64", "Packages")); readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("signed and server-signed package indexes differ: %v", readErr)
	}
	for _, candidate := range []string{tpaSigned, serverSigned} {
		packages := filepath.Join(candidate, "dists", "stable", "main", "binary-amd64", "Packages.gz")
		file, err := os.Open(packages)
		if err != nil {
			t.Fatal(err)
		}
		compressed, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(compressed)
		closeErr := compressed.Close()
		file.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read compressed package index: %v %v", readErr, closeErr)
		}
		if candidate == tpaSigned {
			if err := os.WriteFile(filepath.Join(root, "tpa-Packages"), data, 0600); err != nil {
				t.Fatal(err)
			}
		} else if expected, err := os.ReadFile(filepath.Join(root, "tpa-Packages")); err != nil || !bytes.Equal(data, expected) {
			t.Fatalf("signed and server-signed compressed indexes differ: %v", err)
		}
	}
	tpaChecksums, tpaFields, err := parseCandidateReleaseFile(filepath.Join(tpaSigned, "dists", "stable", "Release"))
	if err != nil {
		t.Fatal(err)
	}
	serverChecksums, serverFields, err := parseCandidateReleaseFile(filepath.Join(serverSigned, "dists", "stable", "Release"))
	if err != nil {
		t.Fatal(err)
	}
	delete(tpaFields, "Date")
	delete(serverFields, "Date")
	if len(tpaFields) != len(serverFields) {
		t.Fatalf("Release fields differ: TPA=%v server=%v", tpaFields, serverFields)
	}
	for field, value := range tpaFields {
		if serverFields[field] != value {
			t.Fatalf("Release field %s differs: %q != %q", field, value, serverFields[field])
		}
	}
	if len(tpaChecksums) != len(serverChecksums) {
		t.Fatalf("Release checksum paths differ: %v != %v", tpaChecksums, serverChecksums)
	}
	for path, expected := range tpaChecksums {
		actual, ok := serverChecksums[path]
		if !ok || expected.size != actual.size || (path != "main/binary-amd64/Packages.gz" && expected.sha256 != actual.sha256) {
			t.Fatalf("Release checksum entry %s differs: %+v != %+v", path, expected, actual)
		}
	}
}

func parseCandidateReleaseFile(path string) (map[string]candidateChecksum, map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return parseCandidateRelease(data)
}
