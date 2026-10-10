package publishertests

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

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	_ "github.com/lib/pq"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/examples/publisher"
)

const postgresImage = "docker.io/library/postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2"
const dynamodbImage = "docker.io/amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab"
const table = "publisher-example"

var dsn, endpoint string

func TestMain(m *testing.M) {
	if mode := os.Getenv("PUBLISHER_TEST_CHILD"); mode != "" {
		os.Exit(runChild(mode))
	}
	os.Exit(runIntegration(m))
}

func runIntegration(m *testing.M) int {
	runtime, err := exec.LookPath("podman")
	if err != nil {
		runtime, err = exec.LookPath("docker")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "publisher integration tests require Podman or Docker")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var containers []string
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, name := range containers {
			if output, err := exec.CommandContext(cleanup, runtime, "rm", "-fv", name).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "container cleanup: %v %s\n", err, output)
			}
		}
	}()
	start := func(image, port string, args ...string) (string, error) {
		name := "publisher-" + strings.ToLower(rand.Text())
		containers = append(containers, name)
		command := []string{"run", "-d", "--name", name, "-p", "127.0.0.1::" + port}
		if image == postgresImage {
			command = append(command, "-e", "POSTGRES_HOST_AUTH_METHOD=trust")
		}
		command = append(command, image)
		command = append(command, args...)
		if output, err := exec.CommandContext(ctx, runtime, command...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("start %s: %w %s", image, err, output)
		}
		output, err := exec.CommandContext(ctx, runtime, "port", name, port+"/tcp").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("port: %w %s", err, output)
		}
		address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
		if _, _, err := net.SplitHostPort(address); err != nil {
			return "", err
		}
		return address, nil
	}
	address, err := start(postgresImage, "5432")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dsn = "postgres://postgres@" + address + "/postgres?sslmode=disable&connect_timeout=2"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	if err := waitReady(ctx, func() error { return db.PingContext(ctx) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := (publisher.Application{DB: db}).Migrate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	address, err = start(dynamodbImage, "8000", "-jar", "DynamoDBLocal.jar", "-sharedDb", "-dbPath", "/home/dynamodblocal")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	endpoint = "http://" + address
	client := sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: new(endpoint), Credentials: localCredentials(), Retryer: aws.NopRetryer{}})
	if err := waitReady(ctx, func() error {
		_, err := client.ListTables(ctx, &sdk.ListTablesInput{Limit: new(int32(1))})
		return err
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, err = client.CreateTable(ctx, &sdk.CreateTableInput{
		TableName: new(table), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: new("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: new("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: new("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: new("sk"), KeyType: types.KeyTypeRange},
		},
	})
	if err == nil {
		err = sdk.NewTableExistsWaiter(client).Wait(ctx, &sdk.DescribeTableInput{TableName: new(table)}, time.Minute)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func waitReady(ctx context.Context, check func() error) error {
	var last error
	for ctx.Err() == nil {
		if last = check(); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("readiness: %w: %v", ctx.Err(), last)
}

func localCredentials() aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
	})
}

func open(t *testing.T, backend, namespace string) (publisher.Application, datastore.Store) {
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
	app := publisher.Application{DB: db, Namespace: namespace}
	config := publisher.QueueConfig{Backend: backend, Namespace: namespace, Table: table,
		Endpoint: endpoint, Region: "us-east-1", Credentials: localCredentials()}
	store, err := config.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return app, store
}
