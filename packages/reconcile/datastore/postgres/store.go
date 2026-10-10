// Package postgres implements the reconciliation store with PostgreSQL.
// Applications own the database pool, credentials, and connection configuration.
package postgres

import (
	"database/sql"
	"errors"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

// Store uses a namespace to isolate independent factory installations.
// Namespaces do not provide authorization between database clients.
type Store struct {
	db        *sql.DB
	namespace string
}

var _ datastore.Store = (*Store)(nil)

// New binds an existing pool without opening connections or running migrations.
func New(db *sql.DB, namespace string) (*Store, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	return &Store{
		db:        db,
		namespace: namespace,
	}, nil
}

func requireUpdatedRow(result sql.Result, missing error) error {
	updatedRows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updatedRows != 1 {
		return missing
	}
	return nil
}

// Round upward so PostgreSQL's microsecond precision never shortens a delay.
func durationMicroseconds(duration time.Duration) int64 {
	microseconds := duration / time.Microsecond
	if duration%time.Microsecond != 0 {
		microseconds++
	}
	return int64(microseconds)
}
