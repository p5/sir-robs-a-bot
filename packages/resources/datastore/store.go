// Package datastore defines authoritative resource state and durable notifications.
// It is separate from the reconciliation queue's coordination store.
package datastore

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"math"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
)

const MaxStateBytes = 32 << 10

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource version conflict")
	ErrCorrupt  = errors.New("invalid stored resource")
)

// Record fixes Content at creation. State changes use compare-and-swap versions.
// State is application-owned JSON, never queue scheduling or ownership metadata.
type Record struct {
	Key     queue.Key
	Version uint64
	Content content.Reference
	State   jsontext.Value
}

func ValidateKey(key queue.Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if len(key.Kind) > 128 || len(key.ID) > 512 {
		return errors.New("resource key exceeds limit")
	}
	return nil
}

func ValidateState(state jsontext.Value) error {
	if len(state) == 0 || len(state) > MaxStateBytes || !state.IsValid() {
		return errors.New("invalid resource state")
	}
	return nil
}

func (record Record) Validate() error {
	if err := ValidateKey(record.Key); err != nil {
		return err
	}
	if record.Version == 0 || record.Version > math.MaxInt64 {
		return errors.New("invalid resource version")
	}
	if err := record.Content.Validate(); err != nil {
		return err
	}
	return ValidateState(record.State)
}

// Delivery identifies the committed generation of a pending notification.
type Delivery struct {
	Key     queue.Key
	Version uint64
}

// Store must atomically commit state and a delivery obligation. Create preserves
// the first record and returns it on duplicates, even when their content differs.
// Update changes only State, checks the exact version, and increments it once.
// Pending need not claim work: duplicate enqueue is safe. Delivered removes only
// the observed generation; it must preserve a concurrent update's obligation.
// Ordinary errors can have unknown outcomes. Callers reread authoritative state.
type Store interface {
	Create(context.Context, Record) (Record, bool, error)
	Get(context.Context, queue.Key) (Record, error)
	Update(context.Context, queue.Key, uint64, jsontext.Value) (Record, error)
	Pending(context.Context, int) ([]Delivery, error)
	Delivered(context.Context, Delivery) error
}
