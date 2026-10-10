// Package datastore defines durable queue coordination and adapter contracts.
package datastore

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("key not found")
	ErrBusy      = errors.New("key has an active claim")
	ErrNoWork    = errors.New("no work due")
	ErrLeaseLost = errors.New("claim completed or superseded")
)

// Key identifies a resource to reconcile. Kind selects its reconciler; ID is an
// opaque resource reference. Application state belongs to the consuming service.
type Key struct {
	Kind string
	ID   string
}

// Item describes queue metadata for a known key. Completed entries remain stored
// so their ownership fences cannot be reused. Pending includes active work.
type Item struct {
	Key Key
	// Abandoned counts expired active claims recovered since success or redrive.
	Abandoned uint32
	Failures  uint32
	Priority  uint32
	LastError string
	Pending   bool
}

// EnqueueOptions selects scheduling priority without changing retry budgets.
type EnqueueOptions struct {
	// Priority raises the priority of pending work. Higher numbers run first.
	Priority uint32
}

// EnqueuePriority rejects ambiguous option lists before backend access.
func EnqueuePriority(options []EnqueueOptions) (uint32, error) {
	if len(options) > 1 {
		return 0, errors.New("at most one enqueue options value is allowed")
	}
	if len(options) == 0 {
		return 0, nil
	}
	return options[0].Priority, nil
}

// Claim grants temporary queue write authority. Fence increases on every claim.
// Sequence tracks enqueue requests, including those arriving during processing.
// Pass claims back unchanged. Tokens protect queue writes, not application state.
type Claim struct {
	Key       Key
	Priority  uint32
	Abandoned uint32
	Failures  uint32
	Fence     uint64
	Sequence  uint64
}

// Completion releases ownership and records the next queue schedule. It contains
// no application checkpoint. Persist application progress before completing work.
// Failure records an unsuccessful call. Stop suspends automatic retries.
type Completion struct {
	Failure string
	Stop    bool
	Again   bool
	After   time.Duration
	// ProtectDelay keeps After as a lower bound despite concurrent enqueues.
	ProtectDelay bool
}

// Store owns queue coordination only. Adapters must use atomic conditional
// operations. They document scheduling clocks, precision, and durability.
// Unknown mutation outcomes return errors; callers must not infer non-delivery.
// Operations honor cancellation and validate their inputs.
type Store interface {
	// Enqueue creates or coalesces an immediate request. It preserves an active
	// claim and failure diagnostics. A request during processing survives completion.
	// Options can raise priority but never bypass a protected scheduling delay.
	// Duplicate delivery can cause another call; reconcilers must be idempotent.
	Enqueue(context.Context, Key, ...EnqueueOptions) error
	Get(context.Context, Key) (Item, error)
	// Redrive resets failure and abandonment budgets and enqueues a known key.
	// It rejects active claims with ErrBusy, including expired claims not recovered.
	Redrive(context.Context, Key) error
	// Renew extends an active claim without shortening its current lease.
	// The fence decides ownership, even after expiry. Unknown outcomes are errors.
	Renew(context.Context, Claim, time.Duration) error
	// Release returns an interrupted claim to immediate pending work. It preserves
	// diagnostics and budgets. Call only after the reconciler has stopped.
	Release(context.Context, Claim) error
	// Claim selects due work for registered kinds and advances its fence. Expiry
	// permits recovery; it does not revoke ownership until a successor wins.
	Claim(context.Context, []string, time.Duration) (Claim, error)
	// Commit validates the active fence and atomically releases ownership, records
	// diagnostics, and schedules follow-up. A newer enqueue always retains work.
	// Duplicate or superseded completion returns ErrLeaseLost without mutations.
	Commit(context.Context, Claim, Completion) error
}
