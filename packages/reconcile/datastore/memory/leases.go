package memory

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) Renew(ctx context.Context, claim datastore.Claim, duration time.Duration) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if duration <= 0 {
		return errors.New("lease duration must be positive")
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := store.schedulingTime()
	if err != nil {
		return err
	}
	entry, ok := store.entries[claim.Key]
	if !ok || !entry.active || entry.fence != claim.Fence {
		return datastore.ErrLeaseLost
	}
	next := now.Add(duration)
	if next.After(entry.leaseUntil) {
		entry.leaseUntil = next
	}
	store.reschedule(entry, now)
	return nil
}

func (store *Store) Release(ctx context.Context, claim datastore.Claim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := store.schedulingTime()
	if err != nil {
		return err
	}
	entry, ok := store.entries[claim.Key]
	if !ok || !entry.active || entry.fence != claim.Fence {
		return datastore.ErrLeaseLost
	}
	entry.active = false
	entry.leaseUntil = time.Time{}
	entry.resource.Pending = true
	entry.dueAt = now
	store.reschedule(entry, now)
	return nil
}

func (store *Store) Redrive(ctx context.Context, key datastore.Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := store.schedulingTime()
	if err != nil {
		return err
	}
	entry, ok := store.entries[key]
	if !ok {
		return datastore.ErrNotFound
	}
	if entry.active {
		return datastore.ErrBusy
	}
	if entry.sequence == math.MaxUint64 {
		return errors.New("enqueue sequence would overflow")
	}
	entry.notBefore = time.Time{}
	entry.resource.Abandoned = 0
	entry.resource.Failures = 0
	entry.resource.LastError = ""
	entry.resource.Pending = true
	entry.dueAt = now
	entry.sequence++
	store.reschedule(entry, now)
	return nil
}
