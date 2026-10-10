package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	dynamo "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	queuedb "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/resources/content/s3"
	statedb "github.com/p5/sir-robs-a-bot/packages/resources/datastore/dynamodb"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

type AWSConfig struct {
	Table             string
	Bucket            string
	Prefix            string
	ResourceNamespace string
	OwnerNamespace    string
	FactoryNamespace  string
	DynamoDBEndpoint  string
	S3Endpoint        string
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("invalid AWS endpoint origin")
	}
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return errors.New("AWS endpoint requires HTTPS or numeric HTTP loopback")
	}
	return nil
}

// OpenAWS uses the standard AWS credential chain, including workload identity.
// Tables and buckets are provisioned separately. Runtime performs no provisioning.
func OpenAWS(ctx context.Context, options AWSConfig) (*AWS, error) {
	if options.Table == "" || options.Bucket == "" || options.ResourceNamespace == "" || options.OwnerNamespace == "" || options.FactoryNamespace == "" || options.OwnerNamespace == options.FactoryNamespace {
		return nil, errors.New("AWS storage requires table, bucket and distinct owner/factory namespaces")
	}
	if err := validateEndpoint(options.DynamoDBEndpoint); err != nil {
		return nil, err
	}
	if err := validateEndpoint(options.S3Endpoint); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	settings, err := config.LoadDefaultConfig(ctx, config.WithHTTPClient(client), config.WithRetryMaxAttempts(3))
	if err != nil {
		return nil, errors.New("load AWS configuration")
	}
	if settings.Region == "" {
		return nil, errors.New("AWS_REGION is required")
	}
	db := dynamo.NewFromConfig(settings, func(o *dynamo.Options) {
		if options.DynamoDBEndpoint != "" {
			o.BaseEndpoint = new(options.DynamoDBEndpoint)
		}
	})
	objectsClient := s3sdk.NewFromConfig(settings, func(o *s3sdk.Options) {
		if options.S3Endpoint != "" {
			o.BaseEndpoint = new(options.S3Endpoint)
			o.UsePathStyle = true
		}
	})
	objects, err := s3.New(objectsClient, options.Bucket, options.Prefix)
	if err != nil {
		return nil, err
	}
	state, err := statedb.New(db, options.Table, options.ResourceNamespace)
	if err != nil {
		return nil, err
	}
	requests, err := intake.NewResourceStore(objects, state)
	if err != nil {
		return nil, err
	}
	owner, err := queuedb.New(db, queuedb.Config{Table: options.Table, Namespace: options.OwnerNamespace})
	if err != nil {
		return nil, err
	}
	factory, err := queuedb.New(db, queuedb.Config{Table: options.Table, Namespace: options.FactoryNamespace})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(options.ResourceNamespace))
	index := &Index{Client: db, Table: options.Table, Namespace: hex.EncodeToString(digest[:]) + "#intake"}
	return &AWS{ResourceStore: requests, Owner: owner, Factory: factory, Index: index}, nil
}
