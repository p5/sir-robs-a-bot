package intake

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

//go:embed schema.sql
var schema string

// Store owns application state. Its outbox can target any queue Store adapter.
type Store struct{ DB *sql.DB }

// Migrate installs the initial application schema. Run it explicitly at deploy.
func (store Store) Migrate(ctx context.Context) error {
	// Refuse to silently replace a legacy queue's application state. This
	// unreleased schema change needs an explicit migration or a fresh database.
	var legacy bool
	if err := store.DB.QueryRowContext(ctx, `SELECT to_regclass('github_ingress_requests') IS NOT NULL`).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		return errors.New("legacy GitHub intake schema requires an explicit data migration or a fresh database")
	}
	var incompatible bool
	if err := store.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('intake_requests') AND attname='snapshot' AND atttypid <> 'bytea'::regtype)`).Scan(&incompatible); err != nil {
		return err
	}
	if incompatible {
		return errors.New("previous intake snapshot schema requires an explicit migration or a fresh database")
	}
	_, err := store.DB.ExecContext(ctx, schema)
	return err
}

// Activate preserves the original intake floor across restart. Changing it
// requires an explicit data migration, not a process configuration change.
func (store Store) Activate(ctx context.Context, connection Connection, since time.Time) error {
	if connection.Validate() != nil || since.IsZero() || since.After(time.Now()) {
		return errors.New("activation requires a valid connection and a past start time")
	}
	// PostgreSQL timestamps have microsecond precision. Normalize before writing
	// so the first activation and later comparisons use the same exact value.
	since = since.UTC().Truncate(time.Microsecond)
	_, err := store.DB.ExecContext(ctx, `INSERT INTO intake_connections(provider, account, activated_at)
 VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, connection.Provider, connection.Account, since.UTC())
	if err != nil {
		return err
	}
	existing, err := store.Activation(ctx, connection)
	if err != nil {
		return err
	}
	if !existing.Equal(since.UTC().Truncate(time.Microsecond)) {
		return errors.New("account already has a different activation time")
	}
	return nil
}

func (store Store) Activation(ctx context.Context, connection Connection) (time.Time, error) {
	var since time.Time
	err := store.DB.QueryRowContext(ctx, `SELECT activated_at FROM intake_connections WHERE provider=$1 AND account=$2`, connection.Provider, connection.Account).Scan(&since)
	return since, err
}

// Accept atomically records a snapshot, queue work, and any requested receipt.
func (store Store) Accept(ctx context.Context, submission Submission) (Acceptance, error) {
	if err := submission.Validate(); err != nil {
		return Acceptance{}, err
	}
	id, err := submission.Source.RequestID()
	if err != nil {
		return Acceptance{}, err
	}
	request := Request{ID: id, Submission: submission}
	snapshot, err := json.Marshal(request)
	if err != nil {
		return Acceptance{}, err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	source := submission.Source
	result, err := tx.ExecContext(ctx, `INSERT INTO intake_requests
    (id,provider,account,scope,source_kind,source_id,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7)
    ON CONFLICT DO NOTHING`, id, source.Connection.Provider, source.Connection.Account, source.Scope, source.Kind, source.ID, snapshot)
	if err != nil {
		return Acceptance{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Acceptance{}, err
	}
	if count != 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO intake_outbox(request_id) VALUES($1)`, id); err != nil {
			return Acceptance{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO intake_acknowledgements(request_id) SELECT $1 WHERE $2`, id, submission.ReceiptMode == ReceiptAsync); err != nil {
			return Acceptance{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return Acceptance{RequestID: id, Created: count != 0}, nil
}

func (store Store) Get(ctx context.Context, id string) (Request, error) {
	var snapshot []byte
	err := store.DB.QueryRowContext(ctx, `SELECT snapshot FROM intake_requests WHERE id=$1`, id).Scan(&snapshot)
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err := json.Unmarshal(snapshot, &request); err != nil {
		return Request{}, errors.New("invalid stored factory request")
	}
	if request.ID != id || request.Validate() != nil {
		return Request{}, errors.New("stored factory request identity mismatch")
	}
	return request, nil
}

func (store Store) List(ctx context.Context, connection Connection) ([]string, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT id FROM intake_requests WHERE provider=$1 AND account=$2 ORDER BY accepted_at,id LIMIT 100`, connection.Provider, connection.Account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeliverOne deletes an obligation only after the queue acknowledges enqueue.
// Queue failure or unknown acknowledgement preserves it for duplicate-safe retry.
func (store Store) DeliverOne(ctx context.Context, queue datastore.Store) (bool, error) {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT request_id FROM intake_outbox ORDER BY created_at,request_id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := queue.Enqueue(ctx, datastore.Key{Kind: Kind, ID: id}); err != nil {
		return true, fmt.Errorf("deliver factory request: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM intake_outbox WHERE request_id=$1`, id); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

var _ Acceptor = Store{}
