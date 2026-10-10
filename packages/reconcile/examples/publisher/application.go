// Package publisher is an executable consumer example, not a factory service.
// Its application records and outbox are independent of the coordination store.
package publisher

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

const Kind = "document"

//go:embed schema.sql
var schema string

// Application owns desired revisions, delivery, and duplicate-safe publications.
// Namespace isolates test data; it is not an authorization boundary.
type Application struct {
	DB        *sql.DB
	Namespace string
}

type Document struct {
	ID               string
	Revision         int64
	Body             string
	ObservedRevision int64
}

// Migrate initializes only the example's application tables.
func (app Application) Migrate(ctx context.Context) error {
	_, err := app.DB.ExecContext(ctx, schema)
	return err
}

// Submit atomically saves desired state and its delivery obligation. Empty bodies
// deliberately reach the reconciler so the example can demonstrate suspension.
func (app Application) Submit(ctx context.Context, id, body string, priority uint32) (int64, error) {
	if err := (datastore.Key{Kind: Kind, ID: id}).Validate(); err != nil {
		return 0, err
	}
	tx, err := app.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, `
        INSERT INTO prototype_documents (namespace, id, revision, body)
        VALUES ($1, $2, 1, $3)
        ON CONFLICT (namespace, id) DO UPDATE
        SET revision = prototype_documents.revision + 1, body = EXCLUDED.body
        RETURNING revision`, app.Namespace, id, body).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("save desired revision: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
        INSERT INTO prototype_outbox (namespace, id, revision, priority)
        VALUES ($1, $2, $3, $4)`, app.Namespace, id, revision, int64(priority))
	if err != nil {
		return 0, fmt.Errorf("save delivery obligation: %w", err)
	}
	return revision, tx.Commit()
}

func (app Application) Get(ctx context.Context, id string) (Document, error) {
	doc := Document{ID: id}
	err := app.DB.QueryRowContext(ctx, `
        SELECT revision, body, observed_revision FROM prototype_documents
        WHERE namespace = $1 AND id = $2`, app.Namespace, id).
		Scan(&doc.Revision, &doc.Body, &doc.ObservedRevision)
	return doc, err
}

// DeliverOne holds an outbox row until enqueue is acknowledged. A lost reply
// leaves the row for retry. Duplicate deliveries are safe, not suppressed.
func (app Application) DeliverOne(ctx context.Context, store datastore.Store) (bool, error) {
	tx, err := app.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id string
	var revision int64
	var priority uint32
	err = tx.QueryRowContext(ctx, `
        SELECT id, revision, priority FROM prototype_outbox WHERE namespace = $1
        ORDER BY id, revision FOR UPDATE SKIP LOCKED LIMIT 1`, app.Namespace).
		Scan(&id, &revision, &priority)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := store.Enqueue(ctx, datastore.Key{Kind: Kind, ID: id}, datastore.EnqueueOptions{Priority: priority}); err != nil {
		return true, fmt.Errorf("deliver outbox: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM prototype_outbox
        WHERE namespace = $1 AND id = $2 AND revision = $3`, app.Namespace, id, revision)
	if err != nil {
		return true, err
	}
	return true, tx.Commit()
}

// ensurePublication models a remote operation with a stable idempotency key.
// The output owner enforces uniqueness, independently of queue ownership.
// A real publisher must provide the same guarantee through its own interface.
func (app Application) ensurePublication(ctx context.Context, doc Document, delay time.Duration) (bool, error) {
	_, err := app.DB.ExecContext(ctx, `
        INSERT INTO prototype_publications (namespace, id, revision, body, ready_at)
        VALUES ($1, $2, $3, $4, clock_timestamp() + $5 * interval '1 microsecond')
        ON CONFLICT (namespace, id, revision) DO NOTHING`,
		app.Namespace, doc.ID, doc.Revision, doc.Body, delay.Microseconds())
	if err != nil {
		return false, err
	}
	var body string
	var ready bool
	err = app.DB.QueryRowContext(ctx, `SELECT body, ready_at <= clock_timestamp()
        FROM prototype_publications WHERE namespace = $1 AND id = $2 AND revision = $3`,
		app.Namespace, doc.ID, doc.Revision).Scan(&body, &ready)
	if err != nil {
		return false, err
	}
	if body != doc.Body {
		return false, errors.New("publication identity reused with different content")
	}
	return ready, nil
}

// observe uses the application's revision, not a queue token. A former callback
// cannot acknowledge a newer desired revision with an older publication.
func (app Application) observe(ctx context.Context, doc Document) (bool, error) {
	result, err := app.DB.ExecContext(ctx, `UPDATE prototype_documents
        SET observed_revision = $3
        WHERE namespace = $1 AND id = $2 AND revision = $3`, app.Namespace, doc.ID, doc.Revision)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}
