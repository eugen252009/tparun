// Package enforcement defines the statically composed repository-delivery
// strategy contract. It has no metering, accounting-store or commercial imports.
package enforcement

import (
	"context"
	"net/http"
	"time"
)

// Request contains already-authorized local delivery facts. Strategies do not
// resolve repositories, authenticate readers or choose publication generations.
type Request struct {
	RepositoryID, AccountID string
	Size                    int64 // candidate file size, NOT measured delivery bytes
	StartedAt               time.Time
}

type Strategy interface {
	// Prepare runs before serving. On failure the strategy owns any necessary
	// reservation recovery/uncertainty; no guard is returned to the caller.
	Prepare(context.Context, Request) (Guard, error)
}

type Guard interface {
	// Wrap may modify delivery (e.g. throttle). It must preserve HTTP semantics
	// and forward actual writes synchronously to the supplied writer, where
	// measurement occurs. It must not buffer response writes until Complete.
	Wrap(http.ResponseWriter) http.ResponseWriter
	// Complete runs on handler unwind, including cancellation, write error and
	// panic, with actual accepted body bytes. There is no discard-on-abort path:
	// partially delivered bytes must still be settled. Strategies own completion
	// failure handling; HTTP status cannot be undone after bytes were served.
	// Complete must not write more response bytes.
	Complete(written int64)
}

// Unavailable is an intentional fail-closed admission error with a public
// message. Unknown strategy errors also fail closed without exposing internals.
type Unavailable struct{ Message string }

func (e *Unavailable) Error() string { return e.Message }

type None struct{}
type passthrough struct{}

func (None) Prepare(context.Context, Request) (Guard, error)       { return passthrough{}, nil }
func (passthrough) Wrap(w http.ResponseWriter) http.ResponseWriter { return w }
func (passthrough) Complete(int64)                                 {}
