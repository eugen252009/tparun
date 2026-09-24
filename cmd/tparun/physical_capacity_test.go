package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/sys/unix"
)

func awaitCapacityEvent(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("capacity event timed out")
	}
}

func TestInodeHeadroomDistinguishesUnavailableFromExhausted(t *testing.T) {
	tests := []struct {
		name, want string
		stat       unix.Statfs_t
		additional int64
	}{
		{name: "available", stat: unix.Statfs_t{Files: 1000, Ffree: 500}, additional: 1, want: "pass"},
		{name: "exhausted", stat: unix.Statfs_t{Files: 1000, Ffree: 0}, additional: 1, want: "reject"},
		{name: "unavailable", stat: unix.Statfs_t{Files: 0, Ffree: 0}, additional: 1, want: "pass"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			free, known, err := inodeHeadroomFromStat(test.stat)
			if err != nil {
				t.Fatal(err)
			}
			err = checkInodeHeadroomValues(free, known, 10, test.additional)
			if test.want == "pass" && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if test.want == "reject" && err == nil {
				t.Fatal("inode exhaustion accepted")
			}
		})
	}
}

func TestUnavailableInodesDoNotBypassByteHeadroom(t *testing.T) {
	free, known, err := inodeHeadroomFromStat(unix.Statfs_t{})
	if err != nil || known || free != 0 {
		t.Fatalf("unexpected unavailable inode metric: free=%d known=%t err=%v", free, known, err)
	}
	if err = checkInodeHeadroomValues(free, known, 10, 100); err != nil {
		t.Fatalf("unavailable inode metric rejected admission: %v", err)
	}
	s := &server{cfg: config.Config{MinDiskFreeBytes: 100}, diskFree: func() (int64, error) { return 50, nil }}
	if err = s.checkDiskHeadroomFor(1); err == nil {
		t.Fatal("byte exhaustion bypassed when inode metric was unavailable")
	}
}

type capacityReader struct {
	reached chan struct{}
	release <-chan struct{}
	rest    io.Reader
	once    sync.Once
}

func (r *capacityReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.reached); <-r.release })
	return r.rest.Read(p)
}

func TestPhysicalSnapshotSerializesLiveUploadGrowth(t *testing.T) {
	s, _ := admissionFixture(t, 1<<20)
	ctx := context.Background()
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming")
	for _, name := range []string{"incoming", "work"} {
		if err = os.MkdirAll(filepath.Join(s.cfg.DataDir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	payload := 16 * block
	bound := payload + block
	s.cfg.MaxUpload = payload
	// Exact budget: both staging roots plus two complete physical reservations.
	s.cfg.MaxStagingBytes = 2*block + 2*bound
	s.cfg.MinDiskFreeBytes = block
	s.cfg.MaxAccountStagingBytes = 2 * bound
	s.diskFree = func() (int64, error) {
		var used int64
		for _, name := range []string{"incoming", "work"} {
			n, e := fileTreeAllocatedSize(filepath.Join(s.cfg.DataDir, name))
			if e != nil {
				return 0, e
			}
			used += n
		}
		return s.cfg.MinDiskFreeBytes + s.cfg.MaxStagingBytes - used, nil
	}
	readerRelease := make(chan struct{})
	var releaseReader sync.Once
	scanRelease := make(chan struct{})
	var releaseScan sync.Once
	defer releaseReader.Do(func() { close(readerRelease) })
	defer releaseScan.Do(func() { close(scanRelease) })
	reader := &capacityReader{reached: make(chan struct{}), release: readerRelease, rest: bytes.NewReader(bytes.Repeat([]byte("x"), int(payload-1)))}
	scanReached := make(chan struct{})
	writeAttempt := make(chan struct{})
	var scanOnce, writeOnce sync.Once
	var armed atomic.Bool
	var clock, scanFinished, writeFinished atomic.Int64
	s.capacityObserve = func(stage, path string) {
		if !armed.Load() {
			return
		}
		if stage == "scan" && path == incoming {
			scanOnce.Do(func() { close(scanReached); <-scanRelease })
		}
		if stage == "before-write" {
			writeOnce.Do(func() { close(writeAttempt) })
		}
		if stage == "after-write" {
			writeFinished.CompareAndSwap(0, clock.Add(1))
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/?filename=live.deb", io.MultiReader(bytes.NewReader([]byte("p")), reader))
	request.ContentLength = payload
	request = request.WithContext(context.WithValue(request.Context(), accountKey, "a"))
	uploadDone := make(chan struct{})
	go func() { defer close(uploadDone); s.upload(response, request, admissionRepo) }()
	awaitCapacityEvent(t, reader.reached) // first byte is already materialized
	armed.Store(true)
	scanResult := make(chan error, 1)
	go func() {
		s.stagingMu.Lock()
		defer s.stagingMu.Unlock()
		e := s.reserveCapacityWithResources(ctx, "a", admissionRepo, "probe", "incoming", bound, 2)
		scanFinished.Store(clock.Add(1))
		scanResult <- e
	}()
	awaitCapacityEvent(t, scanReached)
	releaseReader.Do(func() { close(readerRelease) })
	awaitCapacityEvent(t, writeAttempt)
	// The real copy is attempting growth while the real allocator owns the lock.
	if s.stagingMu.TryLock() {
		s.stagingMu.Unlock()
		t.Fatal("scan did not hold ownership lock")
	}
	paths, e := filepath.Glob(filepath.Join(incoming, "*", "live.deb"))
	if e != nil || len(paths) != 1 {
		t.Fatalf("live input=%v %v", paths, e)
	}
	info, e := os.Stat(paths[0])
	if e != nil || info.Size() != 1 {
		t.Fatalf("copy advanced during frozen scan: %v %v", info, e)
	}
	releaseScan.Do(func() { close(scanRelease) })
	select {
	case e = <-scanResult:
		if e != nil {
			t.Fatalf("exact-fit physical admission: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan stalled")
	}
	awaitCapacityEvent(t, uploadDone)
	if response.Code != http.StatusAccepted {
		t.Fatalf("upload=%d %s", response.Code, response.Body.String())
	}
	if writeFinished.Load() <= scanFinished.Load() {
		t.Fatal("materialization preceded snapshot completion")
	}
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	p, e := s.physicalCapacity(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	if p.committed+p.reserved != s.cfg.MaxStagingBytes {
		t.Fatalf("handoff changed exact-fit budget: %+v", p)
	}
	if e = s.reserveCapacityWithResources(ctx, "a", admissionRepo, "overflow", "incoming", 1, 0); e == nil {
		t.Fatal("over-capacity admission accepted")
	}
	s.releaseCapacity("probe")
}

func TestPhysicalReservationMaterializationResizeAndFailure(t *testing.T) {
	s, _ := admissionFixture(t, 1<<20)
	ctx := context.Background()
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"incoming", "work"} {
		if err = os.MkdirAll(filepath.Join(s.cfg.DataDir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s.cfg.MaxStagingBytes = 6 * block
	s.cfg.MaxAccountStagingBytes = 4 * block
	s.cfg.MinDiskFreeBytes = block
	free := 7 * block
	s.diskFree = func() (int64, error) { return free, nil }
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	reserve := func(n int64) error {
		return s.reserveCapacityWithResources(ctx, "a", admissionRepo, "held", "incoming", n, 2)
	}
	if err = reserve(3 * block); err != nil {
		t.Fatal(err)
	}
	p, err := s.physicalCapacity(ctx, "a")
	if err != nil || p.remaining != 3*block || p.committed != 2*block {
		t.Fatalf("before materialization: %+v %v", p, err)
	}
	path := filepath.Join(s.cfg.DataDir, "incoming", "held")
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "package.deb"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	free -= 2 * block
	p, err = s.physicalCapacity(ctx, "a")
	if err != nil || p.remaining != block || p.committed != 2*block {
		t.Fatalf("materialization counted twice: %+v %v", p, err)
	}
	if err = s.resizeCapacity("held", 4*block); err != nil {
		t.Fatal(err)
	}
	if err = s.resizeCapacity("held", 5*block); err == nil {
		t.Fatal("resize bypassed global/account ceiling")
	}
	if s.reservations["held"].Bytes != 4*block {
		t.Fatal("failed resize mutated reservation")
	}
	if err = s.resizeCapacity("held", block); err == nil {
		t.Fatal("resize below actual allocation accepted")
	}
	if err = s.resizeCapacity("held", 2*block); err != nil {
		t.Fatal(err)
	}
	// Atomic transfer under the same lock, through the actual durable job API.
	if err = s.resizeCapacity("held", 0); err != nil {
		t.Fatal(err)
	}
	if err = s.db.CreateJob(ctx, store.Job{ID: "held", RepositoryID: admissionRepo, State: "queued", IncomingPath: path, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	p, err = s.physicalCapacity(ctx, "a")
	if err != nil || p.remaining != 0 || p.committed != 4*block || p.accountCommitted != 2*block {
		t.Fatalf("durable handoff: %+v %v", p, err)
	}
	free = s.cfg.MinDiskFreeBytes
	if err = reserve(block); err == nil {
		t.Fatal("real disk headroom exhaustion admitted")
	}
	if err = s.db.Finish(ctx, "held", "failed", "controlled failure"); err != nil {
		t.Fatal(err)
	}
	// Reconciliation takes the lock itself; no process-local authority survives.
	s.stagingMu.Unlock()
	err = s.reconcileStagingState()
	s.stagingMu.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed job input survived: %v", err)
	}
	p, err = s.physicalCapacity(ctx, "a")
	if err != nil || p.committed != 2*block || p.remaining != 0 {
		t.Fatalf("cleanup ownership: %+v %v", p, err)
	}
}
