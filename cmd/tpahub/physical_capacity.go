package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type physicalUsage struct{ bytes, inodes int64 }
type physicalCapacity struct {
	committed, reserved, remaining, remainingInodes int64
	accountCommitted, accountReserved               int64
	used                                            map[string]physicalUsage
}

// stagingMu protects the complete observation and every Hub-controlled mutation.
// Each entry belongs either to an existing reservation or to committed storage,
// never both. A reservation's unmaterialized remainder is its disk/inode claim.
func (s *server) physicalCapacity(ctx context.Context, account string) (physicalCapacity, error) {
	p := physicalCapacity{used: make(map[string]physicalUsage)}
	owned := map[string]string{}
	for id, r := range s.reservations {
		var path string
		switch r.Kind {
		case "incoming":
			path = filepath.Join(s.cfg.DataDir, "incoming", id)
		case "work":
			path = filepath.Join(s.cfg.DataDir, "work", id)
		case "candidate":
			path = filepath.Join(s.cfg.DataDir, "work", id+"-published")
		case "repository":
			path = filepath.Join(s.cfg.DataDir, "repos", r.RepositoryID)
		default:
			return p, fmt.Errorf("unknown physical reservation kind %q", r.Kind)
		}
		owned[path] = id
	}
	accountPaths := map[string]bool{}
	if account != "" && s.cfg.MaxAccountStagingBytes > 0 {
		paths, err := s.db.AccountStagingPaths(ctx, account)
		if err != nil {
			return p, err
		}
		for _, path := range paths {
			accountPaths[filepath.Clean(path)] = true
			accountPaths[filepath.Join(s.cfg.DataDir, "work", filepath.Base(path))] = true
			accountPaths[filepath.Join(s.cfg.DataDir, "work", filepath.Base(path)+"-published")] = true
		}
	}
	block, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		return p, err
	}
	add := func(dst *int64, n int64) error {
		v, overflow := addCapacity(*dst, n)
		if overflow {
			return fmt.Errorf("physical capacity overflow")
		}
		*dst = v
		return nil
	}
	for _, root := range []string{filepath.Join(s.cfg.DataDir, "incoming"), filepath.Join(s.cfg.DataDir, "work")} {
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if path == root && os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if s.capacityObserve != nil {
				s.capacityObserve("scan", path)
			}
			info, e := entry.Info()
			if e != nil {
				return e
			}
			allocated, e := allocatedFileBytes(info, block)
			if e != nil {
				return e
			}
			bucket := root
			if path != root {
				rel, e := filepath.Rel(root, path)
				if e != nil {
					return e
				}
				bucket = filepath.Join(root, strings.SplitN(rel, string(os.PathSeparator), 2)[0])
			}
			if id, ok := owned[bucket]; ok {
				u := p.used[id]
				if e = add(&u.bytes, allocated); e != nil {
					return e
				}
				if e = add(&u.inodes, 1); e != nil {
					return e
				}
				p.used[id] = u
			} else {
				if e = add(&p.committed, allocated); e != nil {
					return e
				}
				if accountPaths[bucket] {
					if e = add(&p.accountCommitted, allocated); e != nil {
						return e
					}
				}
			}
			return nil
		})
		if err != nil {
			return p, fmt.Errorf("physical capacity scan: %w", err)
		}
	}
	for id, r := range s.reservations {
		u := p.used[id]
		if r.Bytes < u.bytes || r.Inodes < u.inodes {
			return p, fmt.Errorf("physical reservation bound exceeded: %s", id)
		}
		if err = add(&p.reserved, r.Bytes); err != nil {
			return p, err
		}
		if err = add(&p.remaining, r.Bytes-u.bytes); err != nil {
			return p, err
		}
		if err = add(&p.remainingInodes, r.Inodes-u.inodes); err != nil {
			return p, err
		}
		if r.AccountID == account {
			if err = add(&p.accountReserved, r.Bytes); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}

func allocatedFileBytes(info os.FileInfo, block int64) (int64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Blocks < 0 || stat.Blocks > math.MaxInt64/512 {
		return 0, fmt.Errorf("filesystem allocation metadata unavailable")
	}
	n := stat.Blocks * 512
	if info.IsDir() && n < block {
		n = block
	}
	return n, nil
}

// Read from the network without the allocator lock; serialize only filesystem
// writes. No network read or client wait is permitted under stagingMu.
type uploadWriter struct {
	s              *server
	file           *os.File
	limit, written int64
}

func (w *uploadWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.written {
		return 0, fmt.Errorf("upload too large")
	}
	if w.s.capacityObserve != nil {
		w.s.capacityObserve("before-write", w.file.Name())
	}
	w.s.stagingMu.Lock()
	defer w.s.stagingMu.Unlock()
	n, err := w.file.Write(p)
	w.written += int64(n)
	if w.s.capacityObserve != nil {
		w.s.capacityObserve("after-write", w.file.Name())
	}
	return n, err
}
func (s *server) copyUploadFile(path string, src io.Reader, limit int64) (int64, error) {
	s.stagingMu.Lock()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	s.stagingMu.Unlock()
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(&uploadWriter{s: s, file: file, limit: limit - 1}, io.LimitReader(src, limit))
	s.stagingMu.Lock()
	closeErr := file.Close()
	s.stagingMu.Unlock()
	if err != nil {
		return n, err
	}
	return n, closeErr
}
