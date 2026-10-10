package memory

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := claim.Validate(); err != nil {
		return err
	}
	if err := completion.Validate(); err != nil {
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
	if completion.Failure != "" && entry.resource.Failures == math.MaxUint32 {
		return errors.New("failure count would overflow")
	}
	entry.resource.LastError = completion.Failure
	if completion.Failure != "" {
		entry.resource.Failures++
	} else {
		entry.resource.Failures = 0
		entry.resource.Abandoned = 0
	}
	entry.notBefore = time.Time{}
	if completion.ProtectDelay {
		entry.notBefore = now.Add(completion.After)
	}
	entry.active = false
	entry.leaseUntil = time.Time{}
	entry.resource.Pending = entry.sequence != claim.Sequence || (completion.Again && !completion.Stop)
	if entry.sequence != claim.Sequence {
		entry.dueAt = now
	} else {
		entry.dueAt = now.Add(completion.After)
	}
	if !entry.resource.Pending && completion.Failure == "" {
		entry.resource.Priority = 0
	}
	store.reschedule(entry, now)
	return nil
}
