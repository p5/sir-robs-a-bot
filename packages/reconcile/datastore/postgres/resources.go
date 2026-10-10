package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Enqueue creates or coalesces an immediate request without revoking ownership.
func (store *Store) Enqueue(ctx context.Context, key datastore.Key, options ...datastore.EnqueueOptions) error {
	priority, err := datastore.EnqueuePriority(options)
	if err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	return store.writeTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, enqueueQuery, store.namespace, key.Kind, key.ID, priority)
		return err
	})
}

// Get reads the queue metadata for a known key.
func (store *Store) Get(ctx context.Context, key datastore.Key) (datastore.Item, error) {
	if err := validateKey(key); err != nil {
		return datastore.Item{}, err
	}
	resource, err := scanItem(store.db.QueryRowContext(ctx, getItemQuery, store.namespace, key.Kind, key.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return datastore.Item{}, datastore.ErrNotFound
	}
	return resource, err
}

// Claim locks one due resource without waiting on other claimed rows.
func (store *Store) Claim(ctx context.Context, kinds []string, leaseDuration time.Duration) (datastore.Claim, error) {
	if leaseDuration <= 0 || len(kinds) == 0 {
		return datastore.Claim{}, errors.New("positive lease and registered kinds are required")
	}
	args := []any{store.namespace, durationMicroseconds(leaseDuration)}
	placeholders := make([]string, len(kinds))
	for i, kind := range kinds {
		if err := datastore.ValidateKind(kind); err != nil {
			return datastore.Claim{}, err
		}
		if len(kind) > 256 {
			return datastore.Claim{}, errors.New("resource kind exceeds 256 bytes")
		}
		args = append(args, kind)
		placeholders[i] = fmt.Sprintf("$%d", i+3)
	}
	var claim datastore.Claim
	err := store.writeTransaction(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, fmt.Sprintf(claimKeyQuery, strings.Join(placeholders, ", ")), args...)
		err := row.Scan(&claim.Key.Kind, &claim.Key.ID, &claim.Priority, &claim.Abandoned, &claim.Failures, &claim.Fence, &claim.Sequence)
		if errors.Is(err, sql.ErrNoRows) {
			return datastore.ErrNoWork
		}
		return err
	})
	if err != nil {
		return datastore.Claim{}, err
	}
	return claim, nil
}

// Commit completes queue work only while this claim still owns the resource.
func (store *Store) Commit(ctx context.Context, claim datastore.Claim, completion datastore.Completion) error {
	if err := validateClaim(claim); err != nil {
		return err
	}
	if err := completion.Validate(); err != nil {
		return err
	}

	return store.writeTransaction(ctx, func(tx *sql.Tx) error {
		var locked int
		// Serialize completion with claims and enqueues. The fence check in the
		// update rejects a successor that won ownership before this lock.
		err := tx.QueryRowContext(ctx, lockClaimQuery, store.namespace, claim.Key.Kind, claim.Key.ID, claim.Fence).Scan(&locked)
		if errors.Is(err, sql.ErrNoRows) {
			return datastore.ErrLeaseLost
		}
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, commitItemQuery,
			store.namespace,
			claim.Key.Kind,
			claim.Key.ID,
			claim.Fence,
			claim.Sequence,
			completion.Failure,
			completion.Again && !completion.Stop,
			durationMicroseconds(completion.After),
			completion.ProtectDelay,
		)
		if err != nil {
			return fmt.Errorf("publish completion: %w", err)
		}
		return requireUpdatedRow(result, datastore.ErrLeaseLost)
	})
}
