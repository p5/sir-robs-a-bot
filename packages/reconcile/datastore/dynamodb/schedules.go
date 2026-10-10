package dynamodb

import (
	"context"
	"errors"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"math"
)

// Enqueue coalesces notifications while atomically maintaining discovery.
func (store *Store) Enqueue(ctx context.Context, key datastore.Key, options ...datastore.EnqueueOptions) error {
	priority, err := datastore.EnqueuePriority(options)
	if err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := store.schedulingTime(); err != nil {
		return err
	}
	if err := store.checkSettings(ctx, true); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := store.read(ctx, key)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return err
		}
		if errors.Is(err, datastore.ErrNotFound) {
			current = record{Partition: store.partition(key.Kind), ID: key.ID, Schema: schemaVersion, Item: datastore.Item{Key: key}}
		}
		if current.Sequence == math.MaxUint64 {
			return errors.New("enqueue sequence would overflow")
		}
		now, err := store.schedulingTime()
		if err != nil {
			return err
		}
		previous := current.Revision
		current.Sequence++
		current.Due = now
		current.Item.Pending = true
		current.Item.Priority = max(current.Item.Priority, priority)
		written, err := store.replace(ctx, current, previous)
		if err != nil || written {
			return err
		}
	}
}
