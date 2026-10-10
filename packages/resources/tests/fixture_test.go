package resourcestests

import (
	"context"
	"crypto/rand"
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
	adapter "github.com/p5/sir-robs-a-bot/packages/resources/datastore/dynamodb"
)

const image = "docker.io/amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab"
const table = "factory-resource-tests"

var endpoint string

func credentials(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
}

func newClient() *sdk.Client {
	return sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: new(endpoint), Credentials: aws.CredentialsProviderFunc(credentials), Retryer: aws.NopRetryer{}})
}

func TestMain(m *testing.M) { os.Exit(integration(m)) }

func integration(m *testing.M) int {
	runtime, err := exec.LookPath("podman")
	if err != nil {
		runtime, err = exec.LookPath("docker")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "resource tests require Podman or Docker")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := "factory-resources-" + strings.ToLower(rand.Text())
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if output, err := exec.CommandContext(cleanup, runtime, "rm", "-fv", name).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: %v %s\n", err, output)
		}
	}()
	output, err := exec.CommandContext(ctx, runtime, "run", "-d", "--name", name, "-p", "127.0.0.1::8000", image, "-jar", "DynamoDBLocal.jar", "-sharedDb", "-dbPath", "/home/dynamodblocal").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start DynamoDB: %v %s\n", err, output)
		return 1
	}
	output, err = exec.CommandContext(ctx, runtime, "port", name, "8000/tcp").CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	if _, _, err := net.SplitHostPort(address); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	endpoint = "http://" + address
	client := newClient()
	for {
		if _, err = client.ListTables(ctx, &sdk.ListTablesInput{Limit: new(int32(1))}); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "DynamoDB readiness:", err)
			return 1
		case <-time.After(25 * time.Millisecond):
		}
	}
	_, err = client.CreateTable(ctx, &sdk.CreateTableInput{TableName: new(table), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: new("pk"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: new("sk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: new("pk"), KeyType: types.KeyTypeHash}, {AttributeName: new("sk"), KeyType: types.KeyTypeRange}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create table:", err)
		return 1
	}
	return m.Run()
}

func openState(t *testing.T, namespace string) *adapter.Store {
	t.Helper()
	store, err := adapter.New(newClient(), table, namespace)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
