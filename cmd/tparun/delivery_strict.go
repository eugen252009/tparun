package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/eugen252009/tpahub/internal/enforcement"
)

type strictAccountingEnforcement struct {
	backend func() strictEnforcementBackend
}

var _ enforcement.Strategy = strictAccountingEnforcement{}

// Prepare preserves the existing lookup/begin/reserve ordering. It has no
// response counter dependency: completion receives the measured byte result.
func (e strictAccountingEnforcement) Prepare(ctx context.Context, request enforcement.Request) (enforcement.Guard, error) {
	backend := e.backend()
	repoID, accountID, size, startedAt := request.RepositoryID, request.AccountID, request.Size, request.StartedAt
	usageAt := startedAt.Format(time.RFC3339Nano)
	limited := false
	egressLimit := int64(0)
	if uncertain, err := backend.authority.RepositoryEgressUncertain(ctx, repoID); err != nil || uncertain {
		return nil, &enforcement.Unavailable{Message: "egress accounting unavailable"}
	}
	if used, limit, err := backend.lookup(accountID, startedAt); err != nil {
		limited = grantDenied(err)
		if limited {
			egressLimit = backend.unlimitedReservationLimit
		} // Existing explicit reservation sentinel, NOT policy selection.
		log.Printf("repository egress enforcement lookup: repository=%s: %v", repoID, err)
		if !limited {
			return nil, &enforcement.Unavailable{Message: "egress authority unavailable"}
		}
	} else {
		egressLimit = limit
		limited = egressLimited(used, limit)
	}
	deliveryID := newID()
	if err := backend.begin(ctx, deliveryID, repoID, startedAt.Format(time.RFC3339Nano)); err != nil {
		return nil, &enforcement.Unavailable{Message: "egress accounting unavailable"}
	}
	periodStart, periodEnd, periodErr := backend.period(accountID, startedAt)
	if periodErr != nil {
		if !grantDenied(periodErr) {
			_ = backend.authority.MarkEgressDeliveryUncertain(context.Background(), deliveryID, repoID, periodErr.Error())
			return nil, &enforcement.Unavailable{Message: "egress accounting unavailable"}
		}
		periodStart = time.Date(startedAt.Year(), time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		periodEnd = time.Date(startedAt.Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	}
	reservationLimited, reserveErr := backend.reserve(ctx, deliveryID, repoID, periodStart, periodEnd, size, egressLimit, startedAt.Format(time.RFC3339Nano))
	if reserveErr != nil {
		_ = backend.authority.MarkEgressDeliveryUncertain(context.Background(), deliveryID, repoID, reserveErr.Error())
		return nil, &enforcement.Unavailable{Message: "egress accounting unavailable"}
	}
	limited = limited || reservationLimited
	finish := func(written int64) {
		if err := backend.complete(context.Background(), deliveryID, repoID, usageAt, written, backend.now().UTC().Format(time.RFC3339Nano)); err != nil {
			if markErr := backend.authority.MarkEgressDeliveryUncertain(context.Background(), deliveryID, repoID, err.Error()); markErr != nil {
				log.Printf("repository egress uncertainty: repository=%s delivery=%s: %v", repoID, deliveryID, markErr)
			}
			backend.degraded(accountID)
			log.Printf("repository egress accounting: repository=%s bytes=%d at=%s: %v", repoID, written, usageAt, err)
		}
	}
	return &strictDeliveryGuard{limited: limited, finish: finish}, nil
}

type strictDeliveryGuard struct {
	limited bool
	finish  func(int64)
	once    sync.Once
}

func (g *strictDeliveryGuard) Wrap(w http.ResponseWriter) http.ResponseWriter {
	if !g.limited {
		return w
	}
	w.Header().Set("X-TPAHub-Egress-Limited", "true")
	return &rateLimitedResponseWriter{ResponseWriter: w, rate: NanoOverageBytesPerSecond}
}
func (g *strictDeliveryGuard) Complete(written int64) { g.once.Do(func() { g.finish(written) }) }

// Throttling is an enforcement modifier. The supplied writer performs actual
// measurement; this limiter never changes how accepted body bytes are counted.
type rateLimitedResponseWriter struct {
	http.ResponseWriter
	rate    int64
	started time.Time
	written int64
}

func (w *rateLimitedResponseWriter) Write(p []byte) (int, error) {
	if w.started.IsZero() {
		w.started = time.Now()
	}
	target := time.Duration(float64(w.written+int64(len(p))) / float64(w.rate) * float64(time.Second))
	if wait := target - time.Since(w.started); wait > 0 {
		time.Sleep(wait)
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}
func (w *rateLimitedResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
