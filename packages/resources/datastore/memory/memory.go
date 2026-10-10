// Package memory provides an ephemeral resource store for tests and demos.
package memory

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"math"
	"sync"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

type Store struct {
	mu      sync.Mutex
	records map[queue.Key]datastore.Record
	pending map[queue.Key]uint64
}

func New() *Store {
	return &Store{records: make(map[queue.Key]datastore.Record), pending: make(map[queue.Key]uint64)}
}

func clone(record datastore.Record) datastore.Record {
	record.State = bytes.Clone(record.State)
	return record
}

func (store *Store) Create(ctx context.Context, record datastore.Record) (datastore.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return datastore.Record{}, false, err
	}
	if err := record.Validate(); err != nil {
		return datastore.Record{}, false, err
	}
	if record.Version != 1 {
		return datastore.Record{}, false, errors.New("new resource version must be one")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.records[record.Key]; ok {
		return clone(existing), false, nil
	}
	store.records[record.Key] = clone(record)
	store.pending[record.Key] = record.Version
	return clone(record), true, nil
}

func (store *Store) Get(ctx context.Context, key queue.Key) (datastore.Record, error) {
	if err := ctx.Err(); err != nil {
		return datastore.Record{}, err
	}
	if err := datastore.ValidateKey(key); err != nil {
		return datastore.Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[key]
	if !ok {
		return datastore.Record{}, datastore.ErrNotFound
	}
	return clone(record), nil
}

func (store *Store) Update(ctx context.Context, key queue.Key, expected uint64, state jsontext.Value) (datastore.Record, error) {
	if err := ctx.Err(); err != nil {
		return datastore.Record{}, err
	}
	if err := datastore.ValidateKey(key); err != nil {
		return datastore.Record{}, err
	}
	if err := datastore.ValidateState(state); err != nil {
		return datastore.Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[key]
	if !ok {
		return datastore.Record{}, datastore.ErrNotFound
	}
	if expected != record.Version || expected == math.MaxInt64 {
		return datastore.Record{}, datastore.ErrConflict
	}
	record.Version++
	record.State = bytes.Clone(state)
	store.records[key] = record
	store.pending[key] = record.Version
	return clone(record), nil
}

func (store *Store) Pending(ctx context.Context, limit int) ([]datastore.Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("delivery limit must be between one and 100")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	deliveries := make([]datastore.Delivery, 0, min(limit, len(store.pending)))
	for key, version := range store.pending {
		deliveries = append(deliveries, datastore.Delivery{Key: key, Version: version})
		if len(deliveries) == limit {
			break
		}
	}
	return deliveries, nil
}

func (store *Store) Delivered(ctx context.Context, delivery datastore.Delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateKey(delivery.Key); err != nil {
		return err
	}
	if delivery.Version == 0 || delivery.Version > math.MaxInt64 {
		return errors.New("invalid delivery version")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.pending[delivery.Key] == delivery.Version {
		delete(store.pending, delivery.Key)
	}
	return nil
}
