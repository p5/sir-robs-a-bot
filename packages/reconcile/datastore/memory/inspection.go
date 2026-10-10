package memory

import (
	"context"
	"slices"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) List(ctx context.Context, request datastore.ListRequest) (datastore.Page, error) {
	if err := request.Validate(); err != nil {
		return datastore.Page{}, err
	}
	store.lock.Lock()
	defer store.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return datastore.Page{}, err
	}
	ids := []string{}
	for key := range store.entries {
		if key.Kind == request.Kind && key.ID > request.After {
			ids = append(ids, key.ID)
		}
	}
	slices.Sort(ids)
	page := datastore.Page{}
	for _, id := range ids {
		if len(page.Entries) == request.Limit {
			page.Next = page.Entries[len(page.Entries)-1].Item.Key.ID
			break
		}
		entry := store.entries[datastore.Key{Kind: request.Kind, ID: id}]
		observation := datastore.Entry{Item: entry.resource, DueAt: entry.dueAt.UTC()}
		if !entry.notBefore.IsZero() {
			observation.NotBefore = entry.notBefore.UTC()
		}
		if entry.active {
			observation.LeaseUntil = entry.leaseUntil.UTC()
		}
		page.Entries = append(page.Entries, observation)
	}
	return page, nil
}
