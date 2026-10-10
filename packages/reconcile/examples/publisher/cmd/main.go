package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	_ "github.com/lib/pq"
	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/examples/publisher"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("publisher failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	backend := flag.String("queue", "postgres", "coordination backend: postgres or dynamodb")
	namespace := flag.String("namespace", "publisher-example", "application and queue namespace")
	table := flag.String("table", "", "existing DynamoDB table with string pk and sk")
	endpoint := flag.String("endpoint", "", "optional DynamoDB endpoint for local tests")
	region := flag.String("region", "us-east-1", "DynamoDB AWS region")
	delay := flag.Duration("delay", 200*time.Millisecond, "simulated publication delay")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		return errors.New("usage: publisher [flags] migrate | submit ID BODY [PRIORITY] | relay | work | inspect ID | redrive ID")
	}
	if *delay <= 0 {
		return errors.New("delay must be positive")
	}
	dsn := os.Getenv("PUBLISHER_DSN")
	if dsn == "" {
		return errors.New("PUBLISHER_DSN is required for application state")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	app := publisher.Application{DB: db, Namespace: *namespace}
	if args[0] == "migrate" {
		if len(args) != 1 {
			return errors.New("migrate takes no arguments")
		}
		if err := app.Migrate(ctx); err != nil {
			return err
		}
		if *backend == "postgres" {
			return postgres.Migrate(ctx, db)
		}
		if *backend != "dynamodb" {
			return errors.New("queue must be postgres or dynamodb")
		}
		return nil
	}
	config := publisher.QueueConfig{
		Backend: *backend, Namespace: *namespace, Table: *table, Endpoint: *endpoint, Region: *region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			key, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
			if key == "" || secret == "" {
				return aws.Credentials{}, errors.New("this example requires explicit AWS credentials in the environment")
			}
			return aws.Credentials{AccessKeyID: key, SecretAccessKey: secret, SessionToken: os.Getenv("AWS_SESSION_TOKEN")}, nil
		}),
	}
	store, err := config.Open(db)
	if err != nil {
		return err
	}
	switch args[0] {
	case "submit":
		if len(args) < 3 || len(args) > 4 {
			return errors.New("submit requires ID BODY [PRIORITY]")
		}
		var priority uint64
		if len(args) == 4 {
			priority, err = strconv.ParseUint(args[3], 10, 32)
			if err != nil {
				return err
			}
		}
		revision, err := app.Submit(ctx, args[1], args[2], uint32(priority))
		if err == nil {
			fmt.Println("saved revision", revision, "with pending outbox delivery")
		}
		return err
	case "relay":
		if len(args) != 1 {
			return errors.New("relay takes no arguments")
		}
		for {
			delivered, err := app.DeliverOne(ctx, store)
			if err != nil {
				return err
			}
			if !delivered {
				return nil
			}
		}
	case "work":
		if len(args) != 1 {
			return errors.New("work takes no arguments")
		}
		metrics := reconcile.NewMetrics()
		registry := prometheus.NewRegistry()
		if err := registry.Register(metrics); err != nil {
			return err
		}
		engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
			publisher.Kind: publisher.Controller{Application: app, Delay: *delay},
		}, reconcile.Config{
			Concurrency: 2, LeaseDuration: 3 * time.Second, CallTimeout: time.Second,
			PollInterval: 50 * time.Millisecond, RetryInitial: 100 * time.Millisecond,
			RetryMax: time.Second, MaxFailures: 3, MaxAbandoned: 3, Metrics: metrics,
		})
		if err != nil {
			return err
		}
		err = engine.Run(ctx)
		families, gatherErr := registry.Gather()
		if gatherErr != nil {
			return errors.Join(err, gatherErr)
		}
		for _, family := range families {
			if _, writeErr := expfmt.MetricFamilyToText(os.Stdout, family); writeErr != nil {
				return errors.Join(err, writeErr)
			}
		}
		return err
	case "inspect":
		if len(args) != 2 {
			return errors.New("inspect requires ID")
		}
		doc, err := app.Get(ctx, args[1])
		if err != nil {
			return err
		}
		item, err := store.Get(ctx, datastore.Key{Kind: publisher.Kind, ID: args[1]})
		if err != nil {
			return err
		}
		fmt.Printf("desired=%d observed=%d pending=%t failures=%d abandoned=%d priority=%d error=%q\n",
			doc.Revision, doc.ObservedRevision, item.Pending, item.Failures, item.Abandoned, item.Priority, item.LastError)
		return nil
	case "redrive":
		if len(args) != 2 {
			return errors.New("redrive requires ID")
		}
		return store.Redrive(ctx, datastore.Key{Kind: publisher.Kind, ID: args[1]})
	default:
		return errors.New("unknown publisher command")
	}
}
