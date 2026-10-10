package dynamodb

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) changeRecord(ctx context.Context, key datastore.Key, missing error, change func(*record, int64) error) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := store.checkSettings(ctx, false); err != nil {
		return err
	}
	for {
		current, err := store.read(ctx, key)
		if errors.Is(err, datastore.ErrNotFound) {
			return missing
		}
		if err != nil {
			return err
		}
		now, err := store.schedulingTime()
		if err != nil {
			return err
		}
		revision := current.Revision
		if err := change(&current, now); err != nil {
			return err
		}
		written, err := store.replace(ctx, current, revision)
		if err != nil || written {
			return err
		}
	}
}

// Renew preserves the fence and never shortens a lease after clock rollback.
func (store *Store) Renew(ctx context.Context, claim datastore.Claim, duration time.Duration) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if duration <= 0 {
		return errors.New("lease duration must be positive")
	}
	return store.changeRecord(ctx, claim.Key, datastore.ErrLeaseLost, func(current *record, now int64) error {
		if !current.Active || current.Fence != claim.Fence {
			return datastore.ErrLeaseLost
		}
		current.Lease = max(current.Lease, now+durationMilliseconds(duration))
		return nil
	})
}

// Release makes interrupted work due without changing retry diagnostics.
func (store *Store) Release(ctx context.Context, claim datastore.Claim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	return store.changeRecord(ctx, claim.Key, datastore.ErrLeaseLost, func(current *record, now int64) error {
		if !current.Active || current.Fence != claim.Fence {
			return datastore.ErrLeaseLost
		}
		current.Active = false
		current.Lease = 0
		current.Item.Pending = true
		current.Due = now
		return nil
	})
}

// Redrive rejects active claims, resets budgets, and advances the enqueue sequence.
func (store *Store) Redrive(ctx context.Context, key datastore.Key) error {
	return store.changeRecord(ctx, key, datastore.ErrNotFound, func(current *record, now int64) error {
		if current.Active {
			return datastore.ErrBusy
		}
		if current.Sequence == math.MaxUint64 {
			return errors.New("enqueue sequence would overflow")
		}
		current.NotBefore = 0
		current.Item.Abandoned = 0
		current.Item.Failures = 0
		current.Item.LastError = ""
		current.Item.Pending = true
		current.Due = now
		current.Sequence++
		return nil
	})
}
