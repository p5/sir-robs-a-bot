package memory

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) Claim(ctx context.Context, kinds []string, ttl time.Duration) (datastore.Claim, error) {
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return datastore.Claim{}, err
	}
	if ttl <= 0 || len(kinds) == 0 {
		return datastore.Claim{}, errors.New("invalid claim")
	}
	for _, kind := range kinds {
		if err := datastore.ValidateKind(kind); err != nil {
			return datastore.Claim{}, err
		}
	}
	now, err := store.schedulingTime()
	if err != nil {
		return datastore.Claim{}, err
	}
	entry := store.selectDue(kinds, now)
	if entry == nil {
		return datastore.Claim{}, datastore.ErrNoWork
	}
	if entry.fence == math.MaxUint64 || (entry.active && entry.resource.Abandoned == math.MaxUint32) {
		return datastore.Claim{}, errors.New("claim counter would overflow")
	}
	if entry.active {
		entry.resource.Abandoned++
	}
	entry.fence++
	entry.active = true
	entry.leaseUntil = now.Add(ttl)
	store.reschedule(entry, now)
	return datastore.Claim{
		Key:       entry.resource.Key,
		Priority:  entry.resource.Priority,
		Failures:  entry.resource.Failures,
		Abandoned: entry.resource.Abandoned,
		Fence:     entry.fence,
		Sequence:  entry.sequence,
	}, nil
}

func earlier(left, right *resourceEntry) bool {
	if left.resource.Priority != right.resource.Priority {
		return left.resource.Priority > right.resource.Priority
	}
	if !left.dueAt.Equal(right.dueAt) {
		return left.dueAt.Before(right.dueAt)
	}
	if left.resource.Key.Kind != right.resource.Key.Kind {
		return left.resource.Key.Kind < right.resource.Key.Kind
	}
	return left.resource.Key.ID < right.resource.Key.ID
}
