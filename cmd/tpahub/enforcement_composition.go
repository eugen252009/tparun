package main

import (
	"context"
	"github.com/eugen252009/tpahub/internal/store"
	"time"
)

// Only the strict adapter receives accounting/policy dependencies. Snapshot
// callbacks at Prepare, so a transfer cannot begin against one backend and
// settle against another. This also retains existing fault-injection seams.
func (s *server) strictEnforcementBackend() strictEnforcementBackend {
	db := s.db
	backend := strictEnforcementBackend{
		unlimitedReservationLimit: store.UnlimitedEgressLimit,
		authority:                 db,
		begin:                     db.BeginEgressDelivery,
		reserve:                   db.ReserveEgressDelivery,
		complete:                  db.CompleteEgressDelivery,
		lookup:                    s.accountEgress,
		period:                    s.egressPeriod,
		now:                       s.currentTime,
		degraded: func(account string) {
			s.egressMu.Lock()
			if s.egressDegraded == nil {
				s.egressDegraded = map[string]bool{}
			}
			s.egressDegraded[account] = true
			s.egressMu.Unlock()
		},
	}
	if s.beginEgressDelivery != nil {
		backend.begin = s.beginEgressDelivery
	}
	if s.reserveEgressDelivery != nil {
		backend.reserve = s.reserveEgressDelivery
	}
	if s.completeEgressDelivery != nil {
		backend.complete = s.completeEgressDelivery
	}
	return backend
}

// The strict strategy's actual dependencies, rather than the whole server.
// Grant/policy resolvers only run during preparation. No billing service or
// payment provider participates in this contract.
type strictEnforcementBackend struct {
	unlimitedReservationLimit int64 // existing store sentinel, never mode selection
	authority                 interface {
		RepositoryEgressUncertain(context.Context, string) (bool, error)
		MarkEgressDeliveryUncertain(context.Context, string, string, string) error
	}
	begin    func(context.Context, string, string, string) error
	reserve  func(context.Context, string, string, string, string, int64, int64, string) (bool, error)
	complete func(context.Context, string, string, string, int64, string) error
	lookup   func(string, time.Time) (int64, int64, error)
	period   func(string, time.Time) (string, string, error)
	now      func() time.Time
	degraded func(string)
}
