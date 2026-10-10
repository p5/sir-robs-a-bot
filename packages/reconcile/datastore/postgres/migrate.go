package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

const schemaVersion = 1
const migrationLockID = 731829410605121

//go:embed schema.sql
var initialSchema string

// Migrate installs the schema in the pool's configured search path.
// It serializes migrations across clients and rejects unknown versions.
// Call it explicitly with a migration identity before starting controllers.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database is required")
	}

	return withWriteTransaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
			return fmt.Errorf("lock schema migration: %w", err)
		}

		var durable bool
		err := tx.QueryRowContext(ctx, `
   SELECT current_setting('fsync') = 'on'
      AND current_setting('full_page_writes') = 'on'
  `).Scan(&durable)
		if err != nil {
			return fmt.Errorf("check database durability settings: %w", err)
		}
		if !durable {
			return errors.New("PostgreSQL requires fsync and full_page_writes")
		}

		_, err = tx.ExecContext(ctx, `
   CREATE TABLE IF NOT EXISTS factory_reconcile_schema (
    singleton boolean PRIMARY KEY CHECK (singleton),
    version integer NOT NULL
   )
  `)
		if err != nil {
			return fmt.Errorf("create schema version table: %w", err)
		}

		var version int
		err = tx.QueryRowContext(ctx, "SELECT version FROM factory_reconcile_schema WHERE singleton FOR UPDATE").Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, initialSchema); err != nil {
				return fmt.Errorf("install reconciliation schema: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		return validateSchemaVersion(version)
	})
}

func validateSchemaVersion(version int) error {
	if version != schemaVersion {
		return fmt.Errorf("unsupported reconciliation schema version %d", version)
	}
	return nil
}
