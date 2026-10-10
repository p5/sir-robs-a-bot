package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Renew extends only the current claim; elapsed time alone does not revoke it.
func (store *Store) Renew(ctx context.Context, claim datastore.Claim, duration time.Duration) error {
	if err := validateClaim(claim); err != nil {
		return err
	}
	if duration <= 0 {
		return errors.New("lease duration must be positive")
	}
	return store.writeTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, renewClaimQuery, store.namespace, claim.Key.Kind, claim.Key.ID, claim.Fence, durationMicroseconds(duration))
		if err != nil {
			return err
		}
		return requireUpdatedRow(result, datastore.ErrLeaseLost)
	})
}

// Release relinquishes ownership without recording a reconciliation failure.
func (store *Store) Release(ctx context.Context, claim datastore.Claim) error {
	if err := validateClaim(claim); err != nil {
		return err
	}
	return store.writeTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, releaseClaimQuery, store.namespace, claim.Key.Kind, claim.Key.ID, claim.Fence)
		if err != nil {
			return err
		}
		return requireUpdatedRow(result, datastore.ErrLeaseLost)
	})
}

// Redrive clears budgets only when no controller owns the key.
func (store *Store) Redrive(ctx context.Context, key datastore.Key) error {
	if err := validateKey(key); err != nil {
		return err
	}
	return store.writeTransaction(ctx, func(tx *sql.Tx) error {
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT lease_until IS NOT NULL FROM factory_reconcile_resources
   WHERE namespace=$1 AND kind=$2 AND id=$3 FOR UPDATE`, store.namespace, key.Kind, key.ID).Scan(&active)
		if errors.Is(err, sql.ErrNoRows) {
			return datastore.ErrNotFound
		}
		if err != nil {
			return err
		}
		if active {
			return datastore.ErrBusy
		}
		_, err = tx.ExecContext(ctx, redriveKeyQuery, store.namespace, key.Kind, key.ID)
		return err
	})
}
