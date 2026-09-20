package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhysicalSnapshotSerializesOrphanRemoval(t *testing.T) {
	s, _ := admissionFixture(t, 1<<20)
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "incoming")
	orphan := filepath.Join(root, "orphan")
	for _, p := range []string{orphan, filepath.Join(s.cfg.DataDir, "work")} {
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(orphan, "old.deb")
	if err = os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxStagingBytes = 5 * block
	reached := make(chan struct{})
	release := make(chan struct{})
	attempt := make(chan struct{})
	var releaseOnce, snapshotOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var sequence, scanned, removed atomic.Int64
	s.capacityObserve = func(stage, path string) {
		if stage == "scan" && path == root {
			snapshotOnce.Do(func() { close(reached); <-release })
		}
		if stage == "before-cleanup" {
			close(attempt)
		}
	}
	s.removeAll = func(path string) error { removed.Store(sequence.Add(1)); return os.RemoveAll(path) }
	scanDone := make(chan error, 1)
	go func() {
		s.stagingMu.Lock()
		defer s.stagingMu.Unlock()
		e := s.reserveCapacityWithLogicalResources(context.Background(), "a", admissionRepo, "probe", "incoming", block, 0, 1)
		scanned.Store(sequence.Add(1))
		scanDone <- e
	}()
	awaitCapacityEvent(t, reached)
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- s.reconcileOrphanedStagingState() }()
	awaitCapacityEvent(t, attempt)
	if _, err = os.Stat(file); err != nil {
		t.Fatalf("cleanup removed input during frozen snapshot: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err = <-scanDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan stalled")
	}
	select {
	case err = <-cleanupDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup stalled")
	}
	if removed.Load() <= scanned.Load() {
		t.Fatal("cleanup preceded coherent snapshot")
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("orphan retained: %v", err)
	}
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	p, err := s.physicalCapacity(context.Background(), "a")
	if err != nil || p.committed != 2*block || p.reserved != block {
		t.Fatalf("cleanup ledger: %+v %v", p, err)
	}
}
