package memory

import (
	"context"
	"errors"
	"math"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) Enqueue(ctx context.Context, key datastore.Key, options ...datastore.EnqueueOptions) error {
	priority, err := datastore.EnqueuePriority(options)
	if err != nil {
		return err
	}
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
		entry = &resourceEntry{resource: datastore.Item{Key: key}, position: -1}
		store.entries[key] = entry
	}
	if entry.sequence == math.MaxUint64 {
		return errors.New("enqueue sequence would overflow")
	}
	entry.resource.Priority = max(entry.resource.Priority, priority)
	entry.resource.Pending = true
	entry.sequence++
	entry.dueAt = now
	store.reschedule(entry, now)
	return nil
}

func (store *Store) Get(ctx context.Context, key datastore.Key) (datastore.Item, error) {
	if err := key.Validate(); err != nil {
		return datastore.Item{}, err
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return datastore.Item{}, err
	}
	entry, ok := store.entries[key]
	if !ok {
		return datastore.Item{}, datastore.ErrNotFound
	}
	return entry.resource, nil
}
