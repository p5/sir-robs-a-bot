package factorytests

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/providers/github"
)

const postgresImage = "docker.io/library/postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2"

var databaseURL string

func TestMain(m *testing.M) { os.Exit(runIntegration(m)) }

func runIntegration(m *testing.M) int {
	runtime, err := exec.LookPath("podman")
	if err != nil {
		runtime, err = exec.LookPath("docker")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory tests require Podman or Docker")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name := "factory-intake-" + strings.ToLower(rand.Text())
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if output, err := exec.CommandContext(cleanup, runtime, "rm", "-fv", name).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: %v %s\n", err, output)
		}
	}()
	if output, err := exec.CommandContext(ctx, runtime, "run", "-d", "--name", name, "-p", "127.0.0.1::5432", "-e", "POSTGRES_HOST_AUTH_METHOD=trust", postgresImage).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "start PostgreSQL: %v %s\n", err, output)
		return 1
	}
	output, err := exec.CommandContext(ctx, runtime, "port", name, "5432/tcp").CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	if _, _, err := net.SplitHostPort(address); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	databaseURL = "postgres://postgres@" + address + "/postgres?sslmode=disable&connect_timeout=2"
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	for {
		if err = db.PingContext(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "PostgreSQL readiness failed")
			return 1
		case <-time.After(20 * time.Millisecond):
		}
	}
	return m.Run()
}

type fixture struct {
	Store intake.Store
	Queue *postgres.Store
	DSN   string
	Since time.Time
}

func open(t *testing.T) fixture {
	t.Helper()
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "intake_" + strings.ToLower(rand.Text())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	dsn := databaseURL + "&search_path=" + schema
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	db.SetMaxOpenConns(12)
	store := intake.Store{DB: db}
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	queue, err := postgres.New(db, "factory")
	if err != nil {
		t.Fatal(err)
	}
	floor := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := store.Activate(t.Context(), github.Connection(42), floor); err != nil {
		t.Fatal(err)
	}
	return fixture{Store: store, Queue: queue, DSN: dsn, Since: floor}
}

func submissionID(t *testing.T, submission intake.Submission) string {
	t.Helper()
	id, err := submission.Source.RequestID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func githubRequestID(t *testing.T, id string) string {
	t.Helper()
	source := intake.Source{Connection: github.Connection(42), Scope: "11", Kind: "comment", ID: id}
	key, err := source.RequestID()
	if err != nil {
		t.Fatal(err)
	}
	return key
}
