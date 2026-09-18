// Package usage contains local measurement facts, not accounting or billing.
package usage

import (
	"sync"
	"sync/atomic"
	"time"
)

// Delivery is one admitted file-serving attempt, emitted when serving unwinds.
// Bytes is the additive number of response-body bytes accepted by Write, including
// partial writes and HTTP error bodies produced by ServeFile. It is not wire bytes.
// HEAD/304 normally have zero bytes. Requests rejected before serving are excluded.
// StartedAt is also the existing strict ledger's attribution time.
type Delivery struct {
	RepositoryID, AccountID  string
	StartedAt, FinishedAt    time.Time
	Method                   string
	Status                   int
	Bytes                    int64
	WriteFailed, Interrupted bool
}

// Storage is a point-in-time gauge, never an additive delta. Generation is empty
// for a pre-generation repository. Scope identifies the existing payload scan.
// A high-water value is max(observations), not sum(observations); durable account
// high-water accounting remains owned by the existing store.
type Storage struct {
	RepositoryID, AccountID, Generation, Scope string
	ObservedAt                                 time.Time
	Bytes                                      int64
}

const PublishedPackagePayloads = "published-package-payloads"

type Totals struct {
	Bytes         int64
	Requests      uint64
	WriteFailures uint64
}

type key struct{ account, repository string }

// Local is a volatile, process-lifetime read model. One entry per observed
// account/repository pair, not per request or reporting window.
type Local struct {
	Since  time.Time
	mu     sync.RWMutex
	totals map[key]Totals
}

func NewLocal(now time.Time) *Local { return &Local{Since: now.UTC(), totals: make(map[key]Totals)} }
func (l *Local) Observe(d Delivery) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := key{d.AccountID, d.RepositoryID}
	t := l.totals[k]
	t.Bytes += d.Bytes
	t.Requests++
	if d.WriteFailed {
		t.WriteFailures++
	}
	l.totals[k] = t
}
func (l *Local) Account(account string) Totals {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var t Totals
	for k, v := range l.totals {
		if k.account == account {
			t.Bytes += v.Bytes
			t.Requests += v.Requests
			t.WriteFailures += v.WriteFailures
		}
	}
	return t
}
func (l *Local) Repository(account, repository string) Totals {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.totals[key{account, repository}]
}

// Mailbox is an optional, bounded, best-effort local consumer input. Offering a
// value never waits for its consumer. Consumers own their asynchronous processing
// and cancellation. There is no worker, network protocol, durability or replay.
// Only a receive-only channel is exposed, so consumers cannot close the input.
// A full mailbox drops the new observation and increments Dropped.
type Mailbox struct {
	events  chan Delivery
	dropped atomic.Uint64
}

func NewMailbox(capacity int) *Mailbox {
	if capacity < 1 {
		panic("usage mailbox capacity must be positive")
	}
	return &Mailbox{events: make(chan Delivery, capacity)}
}
func (m *Mailbox) Events() <-chan Delivery { return m.events }
func (m *Mailbox) Dropped() uint64         { return m.dropped.Load() }
func (m *Mailbox) Offer(d Delivery) {
	select {
	case m.events <- d:
	default:
		m.dropped.Add(1)
	}
}
