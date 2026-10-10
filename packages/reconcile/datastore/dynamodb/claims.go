package dynamodb

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

type candidate struct {
	key      datastore.Key
	due      int64
	priority uint32
}

// Claim compares elapsed timers with priority-ordered ready entries.
// Conditional resource transactions remain the authority for every claim.
func (store *Store) Claim(ctx context.Context, kinds []string, leaseDuration time.Duration) (datastore.Claim, error) {
	if leaseDuration <= 0 || len(kinds) == 0 {
		return datastore.Claim{}, errors.New("positive lease and registered kinds are required")
	}
	for _, kind := range kinds {
		if err := validateKey(datastore.Key{Kind: kind, ID: "validation"}); err != nil {
			return datastore.Claim{}, err
		}
	}
	seen := make(map[string]bool, len(kinds))
	if err := store.checkSettings(ctx, false); err != nil {
		return datastore.Claim{}, err
	}
	var candidates candidateHeap
	for _, kind := range kinds {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		found, err := store.discoverKind(ctx, kind)
		if err != nil {
			return datastore.Claim{}, err
		}
		for _, value := range found {
			candidates.offer(value)
		}
	}
	// Priority wins among eligible work. Due time breaks equal-priority ties
	// across kinds, so immediate requeue does not win by ID alone.
	slices.SortFunc(candidates, compareCandidates)
	for _, candidate := range candidates {
		claim, err := store.claimCandidate(ctx, candidate.key, leaseDuration)
		if !errors.Is(err, datastore.ErrNoWork) {
			return claim, err
		}
	}
	if err := ctx.Err(); err != nil {
		return datastore.Claim{}, err
	}
	return datastore.Claim{}, datastore.ErrNoWork
}

func (store *Store) claimCandidate(ctx context.Context, key datastore.Key, leaseDuration time.Duration) (datastore.Claim, error) {
	current, err := store.read(ctx, key)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.Claim{}, datastore.ErrNoWork
	}
	if err != nil {
		return datastore.Claim{}, err
	}
	claimTime, err := store.schedulingTime()
	if err != nil {
		return datastore.Claim{}, err
	}
	// Query results are discovery hints, even with consistent reads. Check the
	// freshly read revision and eligibility before attempting the atomic write.
	if !current.Item.Pending || eligibility(current) > claimTime {
		// A backwards clock can put previously ready work back in the future.
		// Repair its index so those entries cannot permanently fill the candidate window.
		if current.Item.Pending && current.ScheduleState == readyState {
			if _, err := store.replace(ctx, current, current.Revision); err != nil {
				return datastore.Claim{}, err
			}
		}
		return datastore.Claim{}, datastore.ErrNoWork
	}
	if current.Fence == math.MaxUint64 {
		return datastore.Claim{}, errors.New("claim fence would overflow")
	}
	if current.Active {
		if current.Item.Abandoned == math.MaxUint32 {
			return datastore.Claim{}, errors.New("abandonment count would overflow")
		}
		current.Item.Abandoned++
	}
	revision := current.Revision
	current.Fence++
	current.Active = true
	current.Lease = claimTime + durationMilliseconds(leaseDuration)
	written, err := store.replace(ctx, current, revision)
	if err != nil {
		return datastore.Claim{}, err
	}
	if !written {
		return datastore.Claim{}, datastore.ErrNoWork
	}
	return datastore.Claim{Key: key, Priority: current.Item.Priority, Failures: current.Item.Failures, Abandoned: current.Item.Abandoned, Fence: current.Fence, Sequence: current.Sequence}, nil
}
