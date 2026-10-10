package dynamodb

import (
	"context"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Get reads queue metadata for a known key with a strongly consistent read.
func (store *Store) Get(ctx context.Context, key datastore.Key) (datastore.Item, error) {
	if err := validateKey(key); err != nil {
		return datastore.Item{}, err
	}
	if err := store.checkSettings(ctx, false); err != nil {
		return datastore.Item{}, err
	}
	current, err := store.read(ctx, key)
	return current.Item, err
}
