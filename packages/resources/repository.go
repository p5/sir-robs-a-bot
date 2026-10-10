// Package resources commits application resources and recoverable notifications.
package resources

import (
	"context"
	"encoding/json/jsontext"
	"errors"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

// Repository binds an owner's state namespace and content store. It does not
// authenticate callers or grant cross-service access to either store.
type Repository struct {
	content *content.Repository
	state   datastore.Store
}

func New(objects content.Store, state datastore.Store) (*Repository, error) {
	if state == nil {
		return nil, errors.New("resource state store is required")
	}
	snapshots, err := content.New(objects)
	if err != nil {
		return nil, err
	}
	return &Repository{content: snapshots, state: state}, nil
}

// Create uploads content before committing its immutable reference, initial
// state, and notification. Losing uploads can leave unreferenced objects. A
// duplicate returns the original record without changing it or recreating work.
func (repository *Repository) Create(ctx context.Context, key queue.Key, data []byte, state jsontext.Value) (datastore.Record, bool, error) {
	if err := datastore.ValidateKey(key); err != nil {
		return datastore.Record{}, false, err
	}
	if err := datastore.ValidateState(state); err != nil {
		return datastore.Record{}, false, err
	}
	// Avoid uploading changed observations once an authoritative winner exists.
	existing, err := repository.state.Get(ctx, key)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, datastore.ErrNotFound) {
		return datastore.Record{}, false, err
	}
	ref, err := repository.content.Put(ctx, data)
	if err != nil {
		return datastore.Record{}, false, err
	}
	return repository.state.Create(ctx, datastore.Record{Key: key, Version: 1, Content: ref, State: state})
}

func (repository *Repository) Get(ctx context.Context, key queue.Key) (datastore.Record, []byte, error) {
	record, err := repository.state.Get(ctx, key)
	if err != nil {
		return datastore.Record{}, nil, err
	}
	data, err := repository.content.Get(ctx, record.Content)
	if err != nil {
		return datastore.Record{}, nil, err
	}
	return record, data, nil
}

func (repository *Repository) Update(ctx context.Context, key queue.Key, expected uint64, state jsontext.Value) (datastore.Record, error) {
	return repository.state.Update(ctx, key, expected, state)
}

// Enqueuer is the queue operation needed by delivery. Queue Store satisfies it.
type Enqueuer interface {
	Enqueue(context.Context, queue.Key, ...queue.EnqueueOptions) error
}

// DeliverOne leaves the obligation intact on failure or an unknown enqueue
// result. A stale acknowledgement cannot remove a newer resource version.
func (repository *Repository) DeliverOne(ctx context.Context, target Enqueuer) (bool, error) {
	if target == nil {
		return false, errors.New("delivery queue is required")
	}
	deliveries, err := repository.state.Pending(ctx, 1)
	if err != nil {
		return false, err
	}
	if len(deliveries) == 0 {
		return false, nil
	}
	delivery := deliveries[0]
	if err := target.Enqueue(ctx, delivery.Key); err != nil {
		return true, err
	}
	return true, repository.state.Delivered(ctx, delivery)
}
