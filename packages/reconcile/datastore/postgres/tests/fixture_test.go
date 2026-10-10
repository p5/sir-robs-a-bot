package postgrestests

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

const image = "docker.io/library/postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2"

var dsn, containerName, runtimePath string

func command(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, runtimePath, args...).CombinedOutput()
}

func waitReady(ctx context.Context, db *sql.DB) error {
	var last error
	for ctx.Err() == nil {
		if last = db.PingContext(ctx); last == nil {
			return nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return fmt.Errorf("database readiness: %w: %v", ctx.Err(), last)
}

func TestMain(m *testing.M) {
	if os.Getenv("FACTORY_POSTGRES_CHILD") == "1" {
		os.Exit(runInterruptedController())
	}
	os.Exit(runIntegrationTests(m))
}

func runIntegrationTests(m *testing.M) int {
	for _, name := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			runtimePath = p
			break
		}
	}
	if runtimePath == "" {
		fmt.Fprintln(os.Stderr, "PostgreSQL integration tests require Podman or Docker")
		return 1
	}
	containerName = "sir-robs-postgres-" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if out, err := command(cleanup, "rm", "-fv", containerName); err != nil {
			fmt.Fprintf(os.Stderr, "container cleanup: %v %s\n", err, out)
		}
	}()
	out, err := command(ctx, "run", "-d", "--name", containerName, "-p", "127.0.0.1::5432", "-e", "POSTGRES_HOST_AUTH_METHOD=trust", image)
	if err != nil {
		fmt.Fprintf(os.Stderr, "database start: %v %s\n", err, out)
		return 1
	}
	out, err = command(ctx, "port", containerName, "5432/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "database port: %v %s\n", err, out)
		return 1
	}
	address := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	_, port, ok := strings.Cut(address, ":")
	if !ok {
		fmt.Fprintln(os.Stderr, "unexpected database address", address)
		return 1
	}
	if _, err := strconv.Atoi(port); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dsn = "postgres://postgres@127.0.0.1:" + port + "/postgres?sslmode=disable&connect_timeout=2"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	if err := waitReady(ctx, db); err != nil {
		logs, _ := command(ctx, "logs", containerName)
		fmt.Fprintf(os.Stderr, "%v\n%s", err, logs)
		return 1
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, "migration:", err)
		return 1
	}
	return m.Run()
}

func openTestPool(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(12)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func newFixture(t *testing.T) (*postgres.Store, *sql.DB, string) {
	t.Helper()
	db := openTestPool(t)
	namespace := "test-" + rand.Text()
	store, err := postgres.New(db, namespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DELETE FROM factory_reconcile_resources WHERE namespace=$1", namespace); err != nil {
			t.Error(err)
		}
	})
	return store, db, namespace
}

func advanceStoreTime(t *testing.T, db *sql.DB, duration time.Duration) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), "SELECT pg_sleep($1)", duration.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func enqueueKey(t *testing.T, store datastore.Store, id string) datastore.Key {
	t.Helper()
	key := datastore.Key{Kind: "fixture", ID: id}
	if err := store.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return key
}

func claimKey(t *testing.T, store datastore.Store, ttl time.Duration) datastore.Claim {
	t.Helper()
	ownedClaim, err := store.Claim(t.Context(), []string{"fixture"}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return ownedClaim
}

func waitForBlockedCommit(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		err := db.QueryRowContext(ctx, `
            SELECT EXISTS (
                SELECT 1 FROM pg_stat_activity
                WHERE datname = current_database()
                  AND wait_event_type = 'Lock'
                  AND query LIKE '%factory_reconcile_resources%'
                  AND pid <> pg_backend_pid()
            )
        `).Scan(&blocked)
		if err != nil {
			t.Fatalf("observe blocked commit: %v", err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("commit did not wait for row lock: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
