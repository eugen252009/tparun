package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
	"golang.org/x/sys/unix"
)

type semanticCandidateRunner struct {
	t      *testing.T
	cfg    config.Config
	mutate func(string)
}

type recordingPublicationRunner struct{ called bool }

func (r *recordingPublicationRunner) Check(context.Context) error { return nil }
func (r *recordingPublicationRunner) Publish(context.Context, tpa.PublishRequest) error {
	r.called = true
	return errors.New("runner unexpectedly invoked")
}
func (r *recordingPublicationRunner) PublishBounded(ctx context.Context, request tpa.PublishRequest, _ int64) error {
	return r.Publish(ctx, request)
}

func (r semanticCandidateRunner) Check(context.Context) error { return nil }
func (r semanticCandidateRunner) Publish(_ context.Context, p tpa.PublishRequest) error {
	writeSemanticCandidate(r.t, p.Output, p.Input, r.cfg)
	if r.mutate != nil {
		r.mutate(p.Output)
	}
	return nil
}
func (r semanticCandidateRunner) PublishBounded(ctx context.Context, p tpa.PublishRequest, _ int64) error {
	return r.Publish(ctx, p)
}

func workerFailureFixture(t *testing.T) (*server, string, string, store.Job) {
	t.Helper()
	s, _ := testServer(t)
	s.ctx = context.Background()
	s.runner = copyingPublicationRunner{}
	repo := "12121212121212121212121212121212"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: "r3-worker", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	old := filepath.Join(root, "generations", "old")
	if err := os.MkdirAll(old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "old.deb"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "old"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetActiveGeneration(context.Background(), repo, "old", "now"); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "r3-job")
	os.MkdirAll(incoming, 0700)
	os.WriteFile(filepath.Join(incoming, "new.deb"), []byte("new"), 0600)
	job := store.Job{ID: "r3-job", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "now"}
	if err := s.db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return s, repo, root, job
}

func buildWorkerTestDeb(t *testing.T, path, name string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, "DEBIAN"), 0755); err != nil {
		t.Fatal(err)
	}
	control := "Package: " + name + "\nVersion: 1.0.0\nArchitecture: all\nMaintainer: TPA.run test <test@example.invalid>\nDescription: worker signing fixture\n"
	if err := os.WriteFile(filepath.Join(root, "DEBIAN", "control"), []byte(control), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("dpkg-deb", "--build", root, path).CombinedOutput(); err != nil {
		t.Fatalf("build worker test deb: %v: %s", err, output)
	}
}

func buildCurrentTPA(t *testing.T) string {
	t.Helper()
	source := os.Getenv("TPA_DIR")
	if source == "" {
		working, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		source = filepath.Clean(filepath.Join(working, "..", "..", "..", "tpa"))
	}
	bin := filepath.Join(t.TempDir(), "tpa")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = source
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build current TPA: %v: %s", err, output)
	}
	return bin
}
func assertOldAuthorityAndFailed(t *testing.T, s *server, repo, root, job string) {
	t.Helper()
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil || target != filepath.Join("generations", "old") {
		t.Fatalf("pointer=%q err=%v", target, err)
	}
	active, err := s.db.ActiveGeneration(context.Background(), repo)
	if err != nil || active != "old" {
		t.Fatalf("active=%q err=%v", active, err)
	}
	j, err := s.db.Job(context.Background(), job)
	if err != nil || j.State != "failed" {
		t.Fatalf("job=%+v err=%v", j, err)
	}
	if len(s.reservations) != 0 {
		t.Fatalf("reservations=%v", s.reservations)
	}
	if _, err := os.Stat(filepath.Join(root, "generations", job)); !os.IsNotExist(err) {
		t.Fatalf("failed generation retained: %v", err)
	}
}

func TestSuccessfulRunnerMalformedCandidatesNeverActivate(t *testing.T) {
	cfg := testCandidateSigningConfig(t)
	cases := []struct {
		name   string
		mutate func(string)
	}{
		{"garbage", func(p string) {
			os.RemoveAll(p)
			os.MkdirAll(p, 0755)
			os.WriteFile(filepath.Join(p, "garbage"), []byte("x"), 0644)
		}},
		{"missing-release", func(p string) { os.Remove(filepath.Join(p, "dists", "stable", "InRelease")) }},
		{"missing-signature", func(p string) {
			os.WriteFile(filepath.Join(p, "dists", "stable", "InRelease"), []byte("unsigned"), 0644)
		}},
		{"invalid-signature", func(p string) {
			path := filepath.Join(p, "dists", "stable", "InRelease")
			data, _ := os.ReadFile(path)
			marker := []byte("-----BEGIN PGP SIGNATURE-----\n\n")
			at := bytes.Index(data, marker)
			if at >= 0 {
				data[at+len(marker)] = '!'
				os.WriteFile(path, data, 0644)
			}
		}},
		{"truncated-index", func(p string) {
			os.WriteFile(filepath.Join(p, "dists", "stable", "main", "binary-amd64", "Packages"), []byte("Package"), 0644)
		}},
		{"missing-payload", func(p string) {
			entries, _ := os.ReadDir(filepath.Join(p, "pool"))
			if len(entries) > 0 {
				os.Remove(filepath.Join(p, "pool", entries[0].Name()))
			}
		}},
		{"bad-checksum", func(p string) {
			entries, _ := os.ReadDir(filepath.Join(p, "pool"))
			if len(entries) > 0 {
				os.WriteFile(filepath.Join(p, "pool", entries[0].Name()), []byte("bad"), 0644)
			}
		}},
		{"missing-new-package", func(p string) { omitSemanticPackage(t, p, "new.deb", cfg) }},
		{"self-consistent-replaced-payload", func(p string) {
			alternate := t.TempDir()
			entries, err := os.ReadDir(filepath.Join(p, "pool"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, readErr := os.ReadFile(filepath.Join(p, "pool", entry.Name()))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if entry.Name() == "new.deb" {
					data = []byte("different signed package bytes")
				}
				if writeErr := os.WriteFile(filepath.Join(alternate, entry.Name()), data, 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if err = os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			writeSemanticCandidate(t, p, alternate, cfg)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, root, j := workerFailureFixture(t)
			s.verifyCandidate = nil
			s.cfg.GPGHome, s.cfg.GPGFingerprint = cfg.GPGHome, cfg.GPGFingerprint
			s.runner = semanticCandidateRunner{t: t, cfg: cfg, mutate: tc.mutate}
			s.process(j)
			assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
		})
	}
}

func treeDigest(t *testing.T, root string) [32]byte {
	t.Helper()
	h := sha256.New()
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			h.Write([]byte(rel))
			data, _ := os.ReadFile(path)
			h.Write(data)
		}
		return nil
	})
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
func TestVerifiedCandidateIsExactPromotedTree(t *testing.T) {
	s, repo, root, j := workerFailureFixture(t)
	var verified [32]byte
	s.verifyCandidate = func(candidate, _ string) error { verified = treeDigest(t, candidate); return nil }
	s.process(j)
	active, err := s.db.ActiveGeneration(context.Background(), repo)
	if err != nil || active != j.ID {
		t.Fatalf("active=%s err=%v", active, err)
	}
	promoted := treeDigest(t, filepath.Join(root, "generations", j.ID))
	if promoted != verified {
		t.Fatal("promoted tree differs from verified candidate")
	}
}

func TestWorkerPointerRenameFailureRollsBackAndRetrySucceeds(t *testing.T) {
	s, repo, root, j := workerFailureFixture(t)
	s.pointerSwap = func(string, string) error { return unix.EIO }
	s.process(j)
	assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
	s.pointerSwap = nil
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "retry")
	os.MkdirAll(incoming, 0700)
	os.WriteFile(filepath.Join(incoming, "retry.deb"), []byte("retry"), 0600)
	next := store.Job{ID: "retry", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "later"}
	s.db.CreateJob(context.Background(), next)
	s.process(next)
	active, _ := s.db.ActiveGeneration(context.Background(), repo)
	if active != "retry" {
		t.Fatalf("retry active=%s", active)
	}
}
func TestWorkerENOSPCCapacityFailurePreservesAuthorityAndRetry(t *testing.T) {
	s, repo, root, j := workerFailureFixture(t)
	s.diskFree = func() (int64, error) { return 0, nil }
	s.cfg.MinDiskFreeBytes = 1
	s.process(j)
	assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
	s.diskFree = func() (int64, error) { return 1 << 30, nil }
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "retry-space")
	os.MkdirAll(incoming, 0700)
	os.WriteFile(filepath.Join(incoming, "retry.deb"), []byte("retry"), 0600)
	next := store.Job{ID: "retry-space", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "later"}
	s.db.CreateJob(context.Background(), next)
	s.process(next)
	active, _ := s.db.ActiveGeneration(context.Background(), repo)
	if active != "retry-space" {
		t.Fatalf("retry active=%s", active)
	}
}

func TestWorkerCandidateHardBoundENOSPCPreservesAuthorityAndRetry(t *testing.T) {
	s, repo, root, j := workerFailureFixture(t)
	s.cfg.PublishTimeout = time.Minute
	bin := filepath.Join(t.TempDir(), "candidate-overrun-tpa")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nout=''\nwhile [ \"$#\" -gt 0 ]; do [ \"$1\" = '--atomic-publish' ] && { shift; out=$1; }; shift; done\nmkdir -p \"$out\"\nfor n in 1 2 3; do dd if=/dev/zero of=\"$out/blob-$n\" bs=524288 count=1 status=none; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	s.runner = tpa.Runner{Bin: bin}
	s.process(j)
	assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
	failed, err := s.db.Job(context.Background(), j.ID)
	if err != nil || !strings.Contains(strings.ToLower(failed.Error), "space") {
		t.Fatalf("candidate hard bound did not return ENOSPC: job=%+v err=%v", failed, err)
	}
	s.runner = copyingPublicationRunner{}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "retry-hard-bound")
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "retry.deb"), []byte("retry"), 0600); err != nil {
		t.Fatal(err)
	}
	next := store.Job{ID: "retry-hard-bound", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "later"}
	if err := s.db.CreateJob(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	s.process(next)
	active, _ := s.db.ActiveGeneration(context.Background(), repo)
	if active != next.ID {
		t.Fatalf("hard-bound retry active=%s", active)
	}
}
func TestWorkerSigningFailurePreservesAuthorityAndRetry(t *testing.T) {
	s, repo, root, j := workerFailureFixture(t)
	s.cfg.PublishTimeout = time.Minute
	tpaBin := buildCurrentTPA(t)
	buildWorkerTestDeb(t, filepath.Join(root, "generations", "old", "old.deb"), "old-worker-package")
	buildWorkerTestDeb(t, filepath.Join(j.IncomingPath, "new.deb"), "new-worker-package")
	s.runner = tpa.Runner{Bin: tpaBin, GPGHome: t.TempDir(), Fingerprint: "0000000000000000000000000000000000000000"}
	s.process(j)
	assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
	failed, err := s.db.Job(context.Background(), j.ID)
	if err != nil || !strings.Contains(strings.ToLower(failed.Error), "sign") {
		t.Fatalf("worker did not reach signing path: job=%+v err=%v", failed, err)
	}
	signing := testCandidateSigningConfig(t)
	s.runner = tpa.Runner{Bin: tpaBin, GPGHome: signing.GPGHome, Fingerprint: signing.GPGFingerprint}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "retry-sign")
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	buildWorkerTestDeb(t, filepath.Join(incoming, "retry.deb"), "retry-worker-package")
	next := store.Job{ID: "retry-sign", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "later"}
	if err := s.db.CreateJob(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	s.process(next)
	active, _ := s.db.ActiveGeneration(context.Background(), repo)
	if active != "retry-sign" {
		t.Fatalf("retry active=%s", active)
	}
}
func TestOrdinaryPostSwapFailuresRestoreDurableAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*server)
	}{{"checkpoint", func(s *server) {
		s.publicationFault = func(stage string) error {
			if stage == "after-live-swap" {
				return errors.New("ordinary post-swap error")
			}
			return nil
		}
	}}, {"durable-record", func(s *server) {
		s.durablePublicationFault = func() error { return errors.New("database write failed") }
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, root, j := workerFailureFixture(t)
			tc.inject(s)
			s.process(j)
			assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
		})
	}
}

func TestDurableGenerationControlsReadsDuringPointerSwapWindow(t *testing.T) {
	s, repo, root, _ := workerFailureFixture(t)
	if err := os.WriteFile(filepath.Join(root, "generations", "old", "package.deb"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	newGeneration := filepath.Join(root, "generations", "new")
	if err := os.MkdirAll(newGeneration, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newGeneration, "package.deb"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("generations", "new"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s.routes())
	defer h.Close()
	response, err := http.Get(h.URL + "/r/" + repo + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "old" {
		t.Fatalf("status=%d body=%q err=%v", response.StatusCode, body, readErr)
	}
}

func TestPostCommitFailureLeavesActiveGenerationAndJobConsistent(t *testing.T) {
	s, repo, root, job := workerFailureFixture(t)
	s.publicationFault = func(stage string) error {
		if stage == "after-durable-record" {
			return errors.New("simulated post-commit process loss")
		}
		return nil
	}
	s.process(job)
	active, err := s.db.ActiveGeneration(context.Background(), repo)
	if err != nil || active != job.ID {
		t.Fatalf("active=%q err=%v", active, err)
	}
	stored, err := s.db.Job(context.Background(), job.ID)
	if err != nil || stored.State != "succeeded" {
		t.Fatalf("job=%+v err=%v", stored, err)
	}
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil || filepath.Base(target) != job.ID {
		t.Fatalf("pointer=%q err=%v", target, err)
	}
}

func TestStartupRemovesFailedUncommittedGeneration(t *testing.T) {
	s, repo, root, _ := workerFailureFixture(t)
	orphan := filepath.Join(root, "generations", "orphan")
	if err := os.MkdirAll(orphan, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "orphan.deb"), []byte("orphan"), 0644); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "orphan", RepositoryID: repo, State: "failed", IncomingPath: filepath.Join(s.cfg.DataDir, "incoming", "orphan"), CreatedAt: "now"}
	if err := s.db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePublicationState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("uncommitted failed generation remains: %v", err)
	}
}

func TestPublicationProcessDeathRecoveryMatrix(t *testing.T) {
	for _, stage := range []string{"candidate-built", "candidate-verified", "before-live-swap", "after-live-swap", "after-durable-record"} {
		t.Run(stage, func(t *testing.T) {
			data := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run=^TestPublicationProcessDeathHelper$")
			command.Env = append(os.Environ(), "TPARUN_CRASH_HELPER=1", "TPARUN_CRASH_DATA="+data, "TPARUN_CRASH_STAGE="+stage)
			if err := command.Run(); err == nil {
				t.Fatal("crash helper exited successfully")
			}
			db, err := store.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := &server{cfg: config.Config{DataDir: data, PublishTimeout: time.Minute, MaxStagingBytes: 1 << 30}, db: db, ctx: context.Background(), runner: copyingPublicationRunner{}, verifyCandidate: func(string, string) error { return nil }}
			if err = s.reconcilePublicationState(); err != nil {
				t.Fatal(err)
			}
			if err = s.reconcileStagingState(); err != nil {
				t.Fatal(err)
			}
			active, err := db.ActiveGeneration(context.Background(), "crash-repo")
			if err != nil {
				t.Fatal(err)
			}
			job, err := db.Job(context.Background(), "crash-job")
			if err != nil {
				t.Fatal(err)
			}
			wantActive, wantState := "old", "failed"
			if stage == "after-durable-record" {
				wantActive, wantState = "crash-job", "succeeded"
			}
			if active != wantActive || job.State != wantState {
				t.Fatalf("active=%s state=%s want=%s/%s", active, job.State, wantActive, wantState)
			}
			if wantState == "failed" {
				if _, statErr := os.Stat(filepath.Join(data, "repos", "crash-repo", "generations", "crash-job")); !os.IsNotExist(statErr) {
					t.Fatalf("failed crash generation remains: %v", statErr)
				}
			}
			incoming := filepath.Join(data, "incoming", "crash-retry")
			if err = os.MkdirAll(incoming, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(incoming, "retry.deb"), []byte("retry"), 0600); err != nil {
				t.Fatal(err)
			}
			retry := store.Job{ID: "crash-retry", RepositoryID: "crash-repo", State: "running", IncomingPath: incoming, CreatedAt: "later"}
			if err = db.CreateJob(context.Background(), retry); err != nil {
				t.Fatal(err)
			}
			s.process(retry)
			active, _ = db.ActiveGeneration(context.Background(), retry.RepositoryID)
			retried, _ := db.Job(context.Background(), retry.ID)
			if active != retry.ID || retried.State != "succeeded" {
				t.Fatalf("retry active=%s job=%+v", active, retried)
			}
		})
	}
}

func TestPublicationProcessDeathHelper(t *testing.T) {
	if os.Getenv("TPARUN_CRASH_HELPER") != "1" {
		return
	}
	data := os.Getenv("TPARUN_CRASH_DATA")
	for _, name := range []string{"incoming", "work", "artifacts", "repos"} {
		if err := os.MkdirAll(filepath.Join(data, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateAccount(context.Background(), store.Account{ID: "crash-account", Email: "crash@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2024-01-01T00:00:00Z"}, defaultTPAGrants("2024-01-01T00:00:00Z")...); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(context.Background(), store.Repository{ID: "crash-repo", AccountID: "crash-account", Name: "crash", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(data, "repos", "crash-repo")
	old := filepath.Join(repositoryRoot, "generations", "old")
	if err = os.MkdirAll(old, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(old, "old.deb"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join("generations", "old"), filepath.Join(repositoryRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if err = db.SetActiveGeneration(context.Background(), "crash-repo", "old", "now"); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(data, "incoming", "crash-job")
	if err = os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(incoming, "new.deb"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "crash-job", RepositoryID: "crash-repo", State: "running", IncomingPath: incoming, CreatedAt: "now"}
	if err = db.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	stage := os.Getenv("TPARUN_CRASH_STAGE")
	s := &server{cfg: config.Config{DataDir: data, PublishTimeout: time.Minute, MaxStagingBytes: 1 << 30}, db: db, ctx: context.Background(), runner: copyingPublicationRunner{}, verifyCandidate: func(string, string) error { return nil }}
	s.publicationFault = func(current string) error {
		if current == stage {
			os.Exit(42)
		}
		return nil
	}
	s.process(job)
	t.Fatal("publication did not reach crash checkpoint")
}

func TestDisabledGrantAtFinalAuthorizationCannotActivate(t *testing.T) {
	s, repo, root, j := workerFailureFixture(t)
	grant, err := s.db.GetGrant(context.Background(), "a", tpahubproduct.ProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		calls++
		if calls >= 3 {
			return commerce.Grant{}, commerce.ErrGrantDisabled
		}
		return grant, nil
	})
	s.process(j)
	assertOldAuthorityAndFailed(t, s, repo, root, j.ID)
}

func TestLargeBaselineRejectedBeforeWorkCopy(t *testing.T) {
	s, _, root, j := workerFailureFixture(t)
	runner := &recordingPublicationRunner{}
	s.runner = runner
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(filepath.Join(root, "generations", "old", "old.deb"), 3*block); err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxStagingBytes = 6 * block
	s.cfg.MinDiskFreeBytes = 0
	before, err := fileTreeAllocatedSize(filepath.Join(s.cfg.DataDir, "incoming"))
	if err != nil || before > s.cfg.MaxStagingBytes {
		t.Fatalf("incoming fixture already exceeds policy: bytes=%d err=%v", before, err)
	}
	s.process(j)
	stored, err := s.db.Job(context.Background(), j.ID)
	if err != nil || !strings.Contains(stored.Error, "staging") || !strings.Contains(stored.Error, "exceeded") {
		t.Fatalf("job=%+v err=%v", stored, err)
	}
	if runner.called {
		t.Fatal("runner was invoked after work reservation denial")
	}
}

func TestEgressUncertaintySurvivesRestartUntilExplicitRecovery(t *testing.T) {
	s, h := testServer(t)
	repo := "34343434343434343434343434343434"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: "uncertain", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	os.MkdirAll(root, 0755)
	os.WriteFile(filepath.Join(root, "package.deb"), []byte("payload"), 0644)
	var deliveryID string
	s.completeEgressDelivery = func(_ context.Context, id, _ string, _ string, _ int64, _ string) error {
		deliveryID = id
		return errors.New("injected accounting commit failure")
	}
	res, err := http.Get(h.URL + "/r/" + repo + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(res.Body)
	res.Body.Close()
	if readErr != nil || res.StatusCode != http.StatusOK || string(body) != "payload" {
		t.Fatalf("delivered response status=%d body=%q err=%v", res.StatusCode, body, readErr)
	}
	uncertain, err := s.db.AccountEgressUncertain(context.Background(), "a")
	if err != nil || !uncertain {
		t.Fatalf("durable uncertainty=%v err=%v", uncertain, err)
	}
	h.Close()
	s.db.Close()
	db, err := store.Open(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s2 := &server{cfg: s.cfg, db: db}
	h2 := httptest.NewServer(s2.routes())
	defer h2.Close()
	res, err = http.Get(h2.URL + "/r/" + repo + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("uncertain restart status=%d", res.StatusCode)
	}
	if deliveryID == "" {
		t.Fatal("delivery marker was not captured")
	}
	if err = db.CompleteEgressDelivery(context.Background(), deliveryID, repo, "2026-09-04", 7, "now"); err != nil {
		t.Fatal(err)
	}
	res, err = http.Get(h2.URL + "/r/" + repo + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("recovered status=%d", res.StatusCode)
	}
}

func TestEgressBeginFailureServesNoBody(t *testing.T) {
	s, h := testServer(t)
	repo := "78787878787878787878787878787878"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: "begin-failure", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	s.beginEgressDelivery = func(context.Context, string, string, string) error { return errors.New("database unavailable") }
	response, err := http.Get(h.URL + "/r/" + repo + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "payload") {
		t.Fatalf("status=%d body=%q err=%v", response.StatusCode, body, readErr)
	}
}

func TestUnownedRepositoryUncertaintyFailsClosedAfterRestart(t *testing.T) {
	s, h := testServer(t)
	repo := "89898989898989898989898989898989"
	if err := insertCorruptUnownedRepository(s, store.Repository{ID: repo, Name: "unowned", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.db.BeginEgressDelivery(context.Background(), "uncertain-unowned", repo, "now"); err != nil {
		t.Fatal(err)
	}
	h.Close()
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted := &server{cfg: s.cfg, db: db}
	h = httptest.NewServer(restarted.routes())
	defer h.Close()
	response, err := http.Get(h.URL + "/r/" + repo + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", response.StatusCode)
	}
}
