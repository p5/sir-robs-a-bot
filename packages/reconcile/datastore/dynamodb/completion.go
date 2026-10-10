package dynamodb

import (
	"context"
	"errors"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"math"
)

// Commit preserves notifications that arrived after this claim was acquired.
func (store *Store) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if err := validateCompletion(completion); err != nil {
		return err
	}
	return store.changeRecord(ctx, claim.Key, datastore.ErrLeaseLost, func(current *record, now int64) error {
		if !current.Active || current.Fence != claim.Fence {
			return datastore.ErrLeaseLost
		}
		newerEnqueue := current.Sequence != claim.Sequence
		current.Due = now
		if !newerEnqueue {
			current.Due += durationMilliseconds(completion.After)
		}
		current.NotBefore = 0
		if completion.ProtectDelay {
			current.NotBefore = now + durationMilliseconds(completion.After)
		}
		current.Item.Pending = newerEnqueue || (completion.Again && !completion.Stop)
		if !current.Item.Pending && completion.Failure == "" {
			current.Item.Priority = 0
		}
		if completion.Failure != "" {
			if current.Item.Failures == math.MaxUint32 {
				return errors.New("failure count would overflow")
			}
			current.Item.Failures++
		} else {
			current.Item.Failures = 0
			current.Item.Abandoned = 0
		}
		current.Item.LastError = completion.Failure
		current.Active = false
		current.Lease = 0
		return nil
	})
}
