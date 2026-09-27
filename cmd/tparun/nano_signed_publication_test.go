package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

// Build a real, extractable uncompressed Debian package. An additional trailing
// ar member supplies exact byte-boundary padding (permitted after data.tar).
// APT metadata is generated ONLY by the real publisher, never fabricated here.
func sizedNanoDeb(t *testing.T, size int, flavor byte, version string) []byte {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"DEBIAN", "usr/share/nano-fixture"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	control := fmt.Sprintf("Package: nano-fixture\nVersion: %s\nArchitecture: all\nMaintainer: Audit <audit@example.invalid>\nDescription: Nano qualification variant %d\n", version, flavor)
	if err := os.WriteFile(filepath.Join(root, "DEBIAN", "control"), []byte(control), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/share/nano-fixture/payload"), bytes.Repeat([]byte{flavor}, size-65536), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.deb")
	if out, err := exec.Command("dpkg-deb", "-Znone", "--build", root, path).CombinedOutput(); err != nil {
		t.Fatalf("build Debian package: %v %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	padding := size - len(data) - 60
	if padding < 0 {
		t.Fatal("fixture exceeds requested boundary")
	}
	header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n", "nano-padding/", 0, 0, 0, "100644", padding)
	data = append(data, []byte(header)...)
	data = append(data, bytes.Repeat([]byte{flavor}, padding)...)
	// The optional alignment byte after the final, ignored ar member is omitted
	// for an odd boundary. dpkg must still accept and extract the package.
	if len(data) != size {
		t.Fatalf("fixture size=%d want=%d", len(data), size)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	if out, err := exec.Command("dpkg-deb", "--extract", path, extracted).CombinedOutput(); err != nil {
		t.Fatalf("exact-sized Debian package is not extractable: %v %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(extracted, "usr/share/nano-fixture/payload"))
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{flavor}, size-65536)) {
		t.Fatal("extracted package payload differs")
	}
	return data
}

func qualifyPublishedRepositoryWithAPT(t *testing.T, baseURL, repository string, signing config.Config) {
	t.Helper()
	aptGet, err := exec.LookPath("apt-get")
	if err != nil {
		t.Skip("apt-get is unavailable; consumer-side readback not run")
	}
	aptCache, err := exec.LookPath("apt-cache")
	if err != nil {
		t.Skip("apt-cache is unavailable; consumer-side readback not run")
	}
	keyring, err := exec.Command("gpg", "--batch", "--homedir", signing.GPGHome, "--export", signing.GPGFingerprint).Output()
	if err != nil {
		t.Fatalf("export test public key: %v", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "test-keyring.gpg")
	if err := os.WriteFile(keyPath, keyring, 0644); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "sources.list")
	source := fmt.Sprintf("deb [signed-by=%s] %s/r/%s stable main\n", keyPath, baseURL, repository)
	if err := os.WriteFile(sourcePath, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	lists := filepath.Join(root, "lists")
	archives := filepath.Join(root, "archives")
	for _, path := range []string{lists, filepath.Join(lists, "partial"), archives, filepath.Join(archives, "partial")} {
		if err := os.MkdirAll(path, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-o", "Dir::Etc::sourcelist=" + sourcePath, "-o", "Dir::Etc::sourceparts=-", "-o", "Dir::State::lists=" + lists, "-o", "Dir::Cache=" + filepath.Join(root, "cache"), "-o", "Dir::Cache::archives=" + archives, "-o", "Debug::NoLocking=1", "-o", "APT::Get::List-Cleanup=0", "update"}
	if output, err := exec.Command(aptGet, args...).CombinedOutput(); err != nil {
		t.Fatalf("APT update rejected server-signed repository: %v\n%s", err, output)
	}
	showArgs := []string{"-o", "Dir::Etc::sourcelist=" + sourcePath, "-o", "Dir::Etc::sourceparts=-", "-o", "Dir::State::lists=" + lists, "show", "nano-fixture"}
	output, err := exec.Command(aptCache, showArgs...).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Package: nano-fixture") {
		t.Fatalf("APT did not read the published package index: %v\n%s", err, output)
	}
}

func TestNanoRealSignedPublicationReplacementAndRetention(t *testing.T) {
	bin := buildCurrentTPA(t)
	signing := testCandidateSigningConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, h := configuredTestServer(t, func(s *server) {
		s.ctx = ctx
		s.cfg.GPGHome, s.cfg.GPGFingerprint = signing.GPGHome, signing.GPGFingerprint
		s.cfg.MaxUpload = 16 << 20
		s.cfg.MaxStagingBytes = 4 << 30
		s.cfg.MaxAccountStagingBytes = 1 << 30
		s.cfg.MaxCandidateInodes = 65536
		s.cfg.MinDiskFreeBytes = 100 << 20
		s.cfg.MinFreeInodes = 1024
		s.cfg.PublishTimeout = time.Minute
		s.cfg.MaxGenerations = 2
		s.verifyCandidate = nil // real signature, index, payload and identity validation
		s.verifyUnsignedCandidate = nil
		s.signCandidate = nil
		s.cfg.TPABin = bin
		s.runner = publicationTPARunner(s.cfg)
	})
	// No helper grant override: use the actual persisted Nano grant resolver.
	repo := createLocalPolicyRepo(t, s, "tph_s_test", "public")
	grant, err := s.db.GetGrant(ctx, "a", TPARunProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil || grant.Limits.StorageBytes != 5_000_000 {
		t.Fatalf("Nano grant=%+v %v", grant, err)
	}
	workerDone := make(chan struct{})
	started := false
	t.Cleanup(func() {
		cancel()
		if started {
			select {
			case <-workerDone:
			case <-time.After(10 * time.Second):
				t.Error("worker did not stop")
			}
		}
	})
	var active string
	for _, tc := range []struct {
		name                     string
		size                     int
		flavor                   byte
		allowed                  bool
		version                  string
		finalBytes, packageCount int64
	}{
		{"initial-full", 5_000_000, 1, true, "1.0", 5_000_000, 1},
		{"different-bytes-same-identity-full", 5_000_000, 2, true, "1.0", 5_000_000, 1},
		{"reduction-while-full", 4_000_000, 3, true, "1.0", 4_000_000, 1},
		{"growth-to-4800000", 4_800_000, 4, true, "1.0", 4_800_000, 1},
		{"growth-to-exact-limit", 5_000_000, 5, true, "1.0", 5_000_000, 1},
		{"one-byte-over", 5_000_001, 6, false, "1.0", 5_000_000, 1},
		{"make-room-for-another-version", 4_000_000, 7, true, "1.0", 4_000_000, 1},
		{"different-version-retained", 1_000_000, 8, true, "2.0", 5_000_000, 2},
		{"replace-only-matching-version", 3_000_000, 9, true, "1.0", 4_000_000, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := sizedNanoDeb(t, tc.size, tc.flavor, tc.version)
			r, err := http.NewRequest("POST", h.URL+"/v1/repositories/"+repo+"/publishes?filename=fixture.deb", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer tph_s_test")
			r.Header.Set("Content-Type", "application/octet-stream")
			response, err := h.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 202 {
				t.Fatalf("temporary admission=%d %s %v", response.StatusCode, body, err)
			}
			var accepted struct{ ID, State string }
			if err := json.Unmarshal(body, &accepted); err != nil || accepted.ID == "" || accepted.State != "queued" {
				t.Fatalf("not queued: %s %v", body, err)
			}
			if !started {
				queued, err := s.db.Job(ctx, accepted.ID)
				if err != nil || queued.State != "queued" {
					t.Fatalf("durable queue=%+v %v", queued, err)
				}
				info, err := os.Stat(filepath.Join(queued.IncomingPath, "fixture.deb"))
				if err != nil || info.Size() != int64(tc.size) {
					t.Fatal("queued payload not persisted")
				}
				started = true
				go func() { defer close(workerDone); s.worker() }()
			}
			deadline := time.Now().Add(30 * time.Second)
			var job store.Job
			for {
				job, err = s.db.Job(ctx, accepted.ID)
				if err != nil {
					t.Fatal(err)
				}
				if job.State == "succeeded" || job.State == "failed" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("job timed out: %+v", job)
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Wait for the worker's cleanup, not just its durable success record.
			s.stagingMu.Lock()
			defer s.stagingMu.Unlock()
			if tc.allowed {
				if job.State != "succeeded" || job.VerificationState != "match" {
					t.Fatalf("real signed publication=%+v", job)
				}
				active = job.ID
			} else if job.State != "failed" || !strings.Contains(job.Error, "storage-capacity-exceeded") {
				t.Fatalf("one-byte over was not rejected by payload quota: %+v", job)
			}
			gotActive, err := s.db.ActiveGeneration(ctx, repo)
			if err != nil || gotActive != active {
				t.Fatalf("authority=%s want=%s %v", gotActive, active, err)
			}
			root := filepath.Join(s.cfg.DataDir, "repos", repo, "generations", active)
			usage, err := s.accountUsage(ctx, "a")
			want := tc.finalBytes
			if err != nil || usage.StoredBytes != want || usage.PackageCount != tc.packageCount {
				t.Fatalf("final active payload=%+v want=%d %v", usage, want, err)
			}
			total, err := activeRepositoryFileBytes(root)
			if err != nil || total <= want || total-want > activeRepositoryHeadroomBytes {
				t.Fatalf("real metadata footprint total=%d payload=%d %v", total, want, err)
			}
			t.Logf("payload=%d metadata=%d active_total=%d", want, total-want, total)
			inRelease, err := os.ReadFile(filepath.Join(root, "dists", "stable", "InRelease"))
			if err != nil || !bytes.Contains(inRelease, []byte("BEGIN PGP SIGNED MESSAGE")) {
				t.Fatal("missing signed APT release")
			}
			artifacts, err := s.db.PublicationArtifacts(ctx, active)
			if err != nil || len(artifacts) != 1 || artifacts[0].VerificationState != "match" {
				t.Fatalf("uploaded/published identity not verified: %+v %v", artifacts, err)
			}
			if tc.allowed {
				published, err := os.ReadFile(filepath.Join(root, artifacts[0].PublishedFilename))
				if err != nil || !bytes.Equal(published, payload) {
					t.Fatal("replacement did not publish the uploaded bytes")
				}
			}
			for _, dir := range []string{"incoming", "work"} {
				entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, dir))
				if err != nil || len(entries) != 0 {
					t.Fatalf("%s not cleaned: %v %v", dir, entries, err)
				}
			}
			if len(s.reservations) != 0 {
				t.Fatal("temporary reservation leaked")
			}
			generations, err := os.ReadDir(filepath.Dir(root))
			if err != nil || len(generations) > 2 {
				t.Fatalf("retention=%v %v", generations, err)
			}
			if active != "" && tc.name != "initial-full" && len(generations) != 2 {
				t.Fatal("history not retained independently of quota")
			}
		})
	}
	qualifyPublishedRepositoryWithAPT(t, h.URL, repo, signing)
	db, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var schema int
	if err := db.QueryRow("SELECT version FROM tpahub_schema WHERE singleton=1").Scan(&schema); err != nil || schema != 12 {
		t.Fatalf("schema=%d err=%v", schema, err)
	}
}
