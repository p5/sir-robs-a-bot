package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

func (store *Store) writeTransaction(ctx context.Context, operation func(*sql.Tx) error) error {
	return withWriteTransaction(ctx, store.db, func(tx *sql.Tx) error {
		// Hold the version row while writing. A migration must wait for active
		// writers, and an older controller cannot write a newer schema version.
		var version int
		err := tx.QueryRowContext(ctx, "SELECT version FROM factory_reconcile_schema WHERE singleton FOR SHARE").Scan(&version)
		if err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if err := validateSchemaVersion(version); err != nil {
			return err
		}
		return operation(tx)
	})
}

func withWriteTransaction(ctx context.Context, db *sql.DB, operation func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin write transaction: %w", err)
	}
	defer tx.Rollback()

	// A caller's pool may default to asynchronous commits. Every acknowledged
	// coordination write must wait for PostgreSQL's configured WAL durability.
	if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return fmt.Errorf("set synchronous commit: %w", err)
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}
	return nil
}
