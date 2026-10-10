package publisher

import (
	"database/sql"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

// QueueConfig is host wiring for this example. Production hosts should use their
// deployment's credential provider and provision stores outside worker startup.
type QueueConfig struct {
	Backend     string
	Namespace   string
	Table       string
	Endpoint    string
	Region      string
	Credentials aws.CredentialsProvider
}

func (config QueueConfig) Open(db *sql.DB) (datastore.Store, error) {
	switch config.Backend {
	case "postgres":
		return postgres.New(db, config.Namespace)
	case "dynamodb":
		if config.Region == "" || config.Credentials == nil {
			return nil, errors.New("DynamoDB requires a region and credential provider")
		}
		options := sdk.Options{Region: config.Region, Credentials: config.Credentials}
		if config.Endpoint != "" {
			options.BaseEndpoint = new(config.Endpoint)
		}
		return dynamodb.New(sdk.New(options), dynamodb.Config{Table: config.Table, Namespace: config.Namespace})
	default:
		return nil, errors.New("queue must be postgres or dynamodb")
	}
}
