package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/enforcement"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/usage"
)

// deliveryBehavior is composed once, before handlers are exposed. Measurement,
// enforcement and read-model selection are distinct. Strict enforcement owns
// required accounting, NOT observation fan-out. Storage/publication are unchanged.
type deliveryBehavior struct {
	measure     bool
	enforcement enforcement.Strategy
	accounted   bool // selected read-model authority, not a strategy type assertion
	local       *usage.Local
	consumers   []*usage.Mailbox
}

func (s *server) initDelivery() error {
	s.deliveryOnce.Do(func() {
		mode, err := config.ResolveDeliveryMode(s.cfg.DeliveryMode)
		if err != nil {
			s.deliveryErr = err
			return
		}
		selected, err := config.ResolveEgressEnforcement(mode, s.cfg.EgressEnforcement)
		if err != nil {
			s.deliveryErr = err
			return
		}
		d := &deliveryBehavior{measure: mode != config.DeliveryUnmetered}
		switch selected {
		case config.EnforcementNone:
			d.enforcement = enforcement.None{}
			if d.measure {
				d.local = usage.NewLocal(s.currentTime())
			}
		case config.EnforcementStrictAccounted:
			d.enforcement = strictAccountingEnforcement{backend: s.strictEnforcementBackend}
			d.accounted = true
		}
		if d.measure {
			for _, c := range s.usageConsumers {
				if c == nil {
					s.deliveryErr = fmt.Errorf("nil local usage consumer")
					return
				}
				d.consumers = append(d.consumers, c)
			}
		}
		s.delivery = d
	})
	return s.deliveryErr
}

// serveRepositoryFile is the sole byte-serving path after common authorization,
// durable-generation resolution and regular-file checks in files.
func (s *server) serveRepositoryFile(w http.ResponseWriter, r *http.Request, repo store.Repository, files fs.FS, path string, size int64) {
	if err := s.initDelivery(); err != nil {
		bad(w, 503, "delivery configuration unavailable")
		return
	}
	d := s.delivery // fixed for the lifetime of this server, including settlement
	var started time.Time
	output := w
	var counted *repositoryCountingWriter
	if d.measure {
		started = s.currentTime().UTC()
		counted = &repositoryCountingWriter{ResponseWriter: w}
		output = counted
	}
	guard, err := d.enforcement.Prepare(r.Context(), enforcement.Request{RepositoryID: repo.ID, AccountID: repo.AccountID, Size: size, StartedAt: started})
	if err != nil || guard == nil {
		message := "delivery enforcement unavailable"
		var unavailable *enforcement.Unavailable
		if errors.As(err, &unavailable) {
			message = unavailable.Message
		}
		bad(w, http.StatusServiceUnavailable, message)
		return
	}
	completed := false
	defer func() {
		var written int64
		if counted != nil {
			written = counted.bytes
		}
		guard.Complete(written)
		if counted == nil {
			return
		}
		status := counted.status
		if status == 0 {
			status = http.StatusOK
		}
		fact := usage.Delivery{RepositoryID: repo.ID, AccountID: repo.AccountID,
			StartedAt: started, FinishedAt: s.currentTime().UTC(), Method: r.Method,
			Status: status, Bytes: counted.bytes, WriteFailed: counted.writeFailed,
			Interrupted: !completed || r.Context().Err() != nil}
		if d.local != nil {
			d.local.Observe(fact)
		}
		for _, consumer := range d.consumers {
			consumer.Offer(fact)
		}
	}()
	// Register completion before invoking a modifier: abnormal Wrap/Serve unwind
	// must not strand a successfully prepared strict reservation.
	output = guard.Wrap(output)
	http.ServeFileFS(output, r, files, path)
	completed = true
}

// deliveryUsageView is a read model, not a billing snapshot. Strict enforcement reads
// the existing authoritative ledger; it never replays observations into it.
type deliveryUsageView struct {
	Bytes              int64
	Requests           uint64
	Measured, Enforced bool
	Limit              int64
	Period, Status     string
}

func (s *server) readDeliveryUsage(accountID string, now time.Time) (deliveryUsageView, error) {
	if err := s.initDelivery(); err != nil {
		return deliveryUsageView{}, err
	}
	d := s.delivery
	if d.local != nil {
		t := d.local.Account(accountID)
		return deliveryUsageView{Bytes: t.Bytes, Requests: t.Requests, Measured: true,
			Period: "since server start " + d.local.Since.Format(time.RFC3339),
			Status: "Volatile local metrics; reset on restart. No egress quota enforcement."}, nil
	}
	if !d.accounted {
		return deliveryUsageView{Status: "Egress measurement and accounting are disabled."}, nil
	}
	used, limit, err := s.accountEgress(accountID, now)
	if err != nil {
		return deliveryUsageView{}, err
	}
	start, end, err := s.egressPeriod(accountID, now)
	if err != nil {
		return deliveryUsageView{}, err
	}
	return deliveryUsageView{Bytes: used, Limit: limit, Measured: true, Enforced: true,
		Period: start + " / " + end, Status: egressCapacityMessage(used, limit)}, nil
}

func (d *deliveryBehavior) displayPeriods() (summary, repository string) {
	if d.local != nil {
		return "observed since server start (volatile; resets on restart)", "since server start"
	}
	if !d.measure {
		return "not measured", "not measured"
	}
	return "in the current entitlement period", "30d"
}

func (s *server) repositoryDisplayEgress(repo store.Repository, accounted int64) int64 {
	if s.delivery.local != nil {
		return s.delivery.local.Repository(repo.AccountID, repo.ID).Bytes
	}
	if !s.delivery.measure {
		return 0
	}
	return accounted
}
