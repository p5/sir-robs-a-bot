package dynamodbtests

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/storetest"
)

// This index includes Linux amd64 and arm64. Use disk-backed mode so restarting
// the local service tests persistence rather than a replacement in-memory model.
const image = "docker.io/amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab"
const table = "factory-reconcile-tests"

var endpoint, runtimePath, containerName string

type testCredentials struct{}

func (testCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
}

func newClient() *sdk.Client {
	return sdk.New(sdk.Options{
		Region: "us-east-1", BaseEndpoint: new(endpoint),
		Credentials: testCredentials{}, Retryer: aws.NopRetryer{},
	})
}

func command(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, runtimePath, args...).CombinedOutput()
}

func TestMain(m *testing.M) {
	if os.Getenv("FACTORY_DYNAMODB_CHILD") == "1" {
		os.Exit(runInterruptedController())
	}
	os.Exit(runIntegrationTests(m))
}

func runIntegrationTests(m *testing.M) int {
	for _, name := range []string{"podman", "docker"} {
		if path, err := exec.LookPath(name); err == nil {
			runtimePath = path
			break
		}
	}
	if runtimePath == "" {
		fmt.Fprintln(os.Stderr, "DynamoDB integration tests require Podman or Docker")
		return 1
	}
	containerName = "sir-robs-dynamodb-" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if out, err := command(cleanup, "rm", "-fv", containerName); err != nil {
			fmt.Fprintf(os.Stderr, "container cleanup: %v %s\n", err, out)
		}
	}()
	out, err := command(ctx, "run", "-d", "--name", containerName, "-p", "127.0.0.1::8000", image, "-jar", "DynamoDBLocal.jar", "-sharedDb", "-dbPath", "/home/dynamodblocal")
	if err != nil {
		fmt.Fprintf(os.Stderr, "DynamoDB start: %v %s\n", err, out)
		return 1
	}
	out, err = command(ctx, "port", containerName, "8000/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "DynamoDB port: %v %s\n", err, out)
		return 1
	}
	address := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	if _, _, err := net.SplitHostPort(address); err != nil {
		fmt.Fprintln(os.Stderr, "unexpected DynamoDB address", address)
		return 1
	}
	endpoint = "http://" + address
	client := newClient()
	if err := waitReady(ctx, client); err != nil {
		logs, _ := command(ctx, "logs", containerName)
		fmt.Fprintf(os.Stderr, "%v\n%s", err, logs)
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
	if err != nil {
		fmt.Fprintln(os.Stderr, "create table:", err)
		return 1
	}
	if err := sdk.NewTableExistsWaiter(client).Wait(ctx, &sdk.DescribeTableInput{TableName: new(table)}, time.Minute); err != nil {
		fmt.Fprintln(os.Stderr, "wait for table:", err)
		return 1
	}
	return m.Run()
}

func waitReady(ctx context.Context, client *sdk.Client) error {
	var last error
	for ctx.Err() == nil {
		if _, last = client.ListTables(ctx, &sdk.ListTablesInput{Limit: new(int32(1))}); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fmt.Errorf("DynamoDB readiness: %w: %v", ctx.Err(), last)
}

type testClock struct{ milliseconds atomic.Int64 }

func newClock() *testClock {
	clock := new(testClock)
	clock.milliseconds.Store(time.Now().UnixMilli())
	return clock
}

func (clock *testClock) now() time.Time { return time.UnixMilli(clock.milliseconds.Load()) }
func (clock *testClock) advance(duration time.Duration) {
	clock.milliseconds.Add(int64(duration / time.Millisecond))
}

func newFixture(t *testing.T) (*adapter.Store, *sdk.Client, adapter.Config, *testClock) {
	t.Helper()
	clock := newClock()
	config := adapter.Config{Table: table, Namespace: "test-" + rand.Text(), Clock: clock.now}
	client := newClient()
	store, err := adapter.New(client, config)
	if err != nil {
		t.Fatal(err)
	}
	return store, client, config, clock
}

func contractFixture(t *testing.T) storetest.Fixture {
	t.Helper()
	store, _, config, clock := newFixture(t)
	return storetest.Fixture{
		Store: store, Elapse: clock.advance,
		Reopen: func() datastore.Store {
			reopened, err := adapter.New(newClient(), config)
			if err != nil {
				t.Fatal(err)
			}
			return reopened
		},
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

func claimKey(t *testing.T, store datastore.Store) datastore.Claim {
	t.Helper()
	claim, err := store.Claim(t.Context(), []string{"fixture"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
