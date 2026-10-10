package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/persistence"
)

type backend interface {
	intake.Acceptor
	Migrate(context.Context) error
	Activate(context.Context, intake.Connection, time.Time) error
	Activation(context.Context, intake.Connection) (time.Time, error)
	Get(context.Context, string) (intake.Request, error)
	List(context.Context, intake.Connection) ([]string, error)
	DeliverOne(context.Context, queue.Store) (bool, error)
	AcknowledgeDue(context.Context, intake.Acknowledger) (int, error)
	Acknowledgement(context.Context, intake.Connection, string) (intake.AcknowledgementStatus, error)
	RedriveAcknowledgement(context.Context, intake.Connection, string) error
}

type postgresBackend struct{ intake.Store }

func (store postgresBackend) Migrate(ctx context.Context) error {
	if err := store.Store.Migrate(ctx); err != nil {
		return err
	}
	return postgres.Migrate(ctx, store.DB)
}

func openStorage(ctx context.Context, selection, namespace string) (backend, queue.Store, func(), error) {
	if selection == "aws" {
		store, err := persistence.OpenAWS(ctx, persistence.AWSConfig{
			Table: os.Getenv("INTAKE_TABLE"), Bucket: os.Getenv("INTAKE_BUCKET"), Prefix: os.Getenv("INTAKE_CONTENT_PREFIX"),
			ResourceNamespace: namespace + "-intake-resources", OwnerNamespace: namespace + "-intake-work", FactoryNamespace: namespace,
			DynamoDBEndpoint: os.Getenv("INTAKE_DYNAMODB_ENDPOINT"), S3Endpoint: os.Getenv("INTAKE_S3_ENDPOINT"),
		})
		if err != nil {
			return nil, nil, nil, err
		}
		return store, store.Factory, func() {}, nil
	}
	if selection != "postgres" {
		return nil, nil, nil, errors.New("storage must be aws or postgres")
	}
	dsn := os.Getenv("INTAKE_DSN")
	if dsn == "" {
		return nil, nil, nil, errors.New("INTAKE_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, nil, nil, errors.New("open intake PostgreSQL connection")
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	target, err := postgres.New(db, namespace)
	if err != nil {
		db.Close()
		return nil, nil, nil, err
	}
	return postgresBackend{Store: intake.Store{DB: db}}, target, func() { db.Close() }, nil
}

func storageDefault() string {
	if selection := os.Getenv("INTAKE_STORAGE"); selection != "" {
		return selection
	}
	return "aws"
}

// runDispatch needs AWS credentials only. Provider availability cannot prevent
// factory handoff. A separate provider worker processes connection effects.
func runDispatch(ctx context.Context, store backend) error {
	aws, ok := store.(*persistence.AWS)
	if !ok {
		return errors.New("worker requires AWS resource storage")
	}
	for {
		_, err := aws.Drain(ctx, nil)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
