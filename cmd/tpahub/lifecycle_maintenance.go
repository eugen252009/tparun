package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

const (
	lifecycleEphemeralPoll = time.Minute
	lifecycleCustomerPoll  = 24 * time.Hour
	lifecyclePurgeBatch    = 32
)

// PurgeAccount is the server-owned wrapper around the single store purge
// primitive. Database state is removed transactionally; repository filesystem
// roots are then removed using the existing safe path cleanup helper.
func (s *server) PurgeAccount(ctx context.Context, accountID string, now time.Time) error {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	repositories, err := s.db.PurgeAccount(ctx, accountID, now)
	if err != nil || repositories == nil {
		return err
	}
	for _, repositoryID := range repositories {
		for _, path := range []string{
			filepath.Join(s.cfg.DataDir, "repos", repositoryID),
			filepath.Join(s.cfg.DataDir, "artifacts", repositoryID),
		} {
			if err := s.removePath(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *server) runLifecycleMaintenance(ctx context.Context, now time.Time) {
	for _, kind := range []store.AccountLifecycleKind{store.AccountLifecycleEphemeral, store.AccountLifecycleCustomer} {
		ids, err := s.db.DueAccounts(ctx, kind, now, lifecyclePurgeBatch)
		if err != nil {
			log.Printf("lifecycle %s discovery: %v", kind, err)
			continue
		}
		for _, id := range ids {
			if err := s.PurgeAccount(ctx, id, now); err != nil {
				log.Printf("lifecycle %s purge account=%s: %v", kind, id, err)
			}
		}
	}
}

func (s *server) lifecycleMaintenanceLoop() {
	if s.ctx == nil {
		return
	}
	fast := time.NewTicker(lifecycleEphemeralPoll)
	defer fast.Stop()
	daily := time.NewTicker(lifecycleCustomerPoll)
	defer daily.Stop()
	// Run once on startup so bounded backlog does not wait for the first tick.
	s.runLifecycleMaintenance(s.ctx, s.currentTime())
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-fast.C:
			ids, err := s.db.DueAccounts(s.ctx, store.AccountLifecycleEphemeral, now.UTC(), lifecyclePurgeBatch)
			if err != nil {
				log.Printf("ephemeral lifecycle discovery: %v", err)
				continue
			}
			for _, id := range ids {
				if err := s.PurgeAccount(s.ctx, id, now.UTC()); err != nil {
					log.Printf("ephemeral purge account=%s: %v", id, err)
				}
			}
		case now := <-daily.C:
			ids, err := s.db.DueAccounts(s.ctx, store.AccountLifecycleCustomer, now.UTC(), lifecyclePurgeBatch)
			if err != nil {
				log.Printf("customer lifecycle discovery: %v", err)
				continue
			}
			for _, id := range ids {
				if err := s.PurgeAccount(s.ctx, id, now.UTC()); err != nil {
					log.Printf("customer purge account=%s: %v", id, err)
				}
			}
		}
	}
}
