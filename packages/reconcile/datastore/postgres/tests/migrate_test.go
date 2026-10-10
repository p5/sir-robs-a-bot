package postgrestests

import (
	"context"
	"database/sql"
	"testing"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

func TestMigrationIsRepeatable(t *testing.T) {
	db := openTestPool(t)
	if err := postgres.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRejectsUnknownVersion(t *testing.T) {
	db := openTestPool(t)
	// Isolate the metadata change from the shared integration database schema.
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "CREATE SCHEMA migration_test"); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), "DROP SCHEMA migration_test CASCADE")
	isolated, err := sql.Open("postgres", dsn+"&search_path=migration_test")
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err := postgres.Migrate(t.Context(), isolated); err != nil {
		t.Fatalf("install isolated schema: %v", err)
	}
	if _, err := isolated.ExecContext(t.Context(), "UPDATE factory_reconcile_schema SET version=999"); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), isolated); err == nil {
		t.Fatal("migration accepted unknown schema version")
	}
	store, err := postgres.New(isolated, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	key := datastore.Key{Kind: "fixture", ID: "one"}
	if err := store.Enqueue(t.Context(), key); err == nil {
		t.Fatal("write accepted unknown schema version")
	}
}
