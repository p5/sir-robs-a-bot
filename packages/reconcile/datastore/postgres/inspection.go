package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

var _ datastore.Inspector = (*Store)(nil)

// List uses a keyset cursor. It does not lock rows or affect dispatch order.
func (store *Store) List(ctx context.Context, request datastore.ListRequest) (datastore.Page, error) {
	if err := request.Validate(); err != nil {
		return datastore.Page{}, err
	}
	if len(request.Kind) > 256 || len(request.After) > 1024 {
		return datastore.Page{}, errors.New("resource kind must fit 256 bytes and cursor must fit 1024 bytes")
	}

	rows, err := store.db.QueryContext(ctx, listEntriesQuery, store.namespace, request.Kind, request.After, request.Limit+1)
	if err != nil {
		return datastore.Page{}, err
	}
	defer rows.Close()
	page := datastore.Page{}
	for rows.Next() {
		var entry datastore.Entry
		var lease, floor sql.NullTime
		err := rows.Scan(&entry.Item.Key.Kind, &entry.Item.Key.ID, &entry.Item.Priority, &entry.Item.Abandoned,
			&entry.Item.Failures, &entry.Item.LastError, &entry.Item.Pending, &entry.DueAt, &lease, &floor)
		if err != nil {
			return datastore.Page{}, err
		}
		if len(page.Entries) == request.Limit {
			page.Next = page.Entries[len(page.Entries)-1].Item.Key.ID
			break
		}
		entry.DueAt = entry.DueAt.UTC()
		if floor.Valid {
			entry.NotBefore = floor.Time.UTC()
		}
		if lease.Valid {
			entry.LeaseUntil = lease.Time.UTC()
		}
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return datastore.Page{}, err
	}
	return page, nil
}

const listEntriesQuery = `
 SELECT ` + itemColumns + `, due_at, lease_until, not_before
 FROM factory_reconcile_resources
 WHERE namespace = $1 AND kind = $2 AND id COLLATE "C" > $3
 ORDER BY id COLLATE "C"
 LIMIT $4
`
