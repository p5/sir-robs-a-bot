package postgres

import (
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

type scanner interface {
	Scan(...any) error
}

func scanItem(row scanner) (datastore.Item, error) {
	var resource datastore.Item
	err := row.Scan(
		&resource.Key.Kind,
		&resource.Key.ID,
		&resource.Priority,
		&resource.Abandoned,
		&resource.Failures,
		&resource.LastError,
		&resource.Pending,
	)
	return resource, err
}
