package factorytests

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/persistence"
)

const intakeDynamoImage = "docker.io/amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab"

type awsFixture struct {
	Endpoint string
	Objects  *httptest.Server
	Config   persistence.AWSConfig
}

func openAWS(t *testing.T) awsFixture {
	t.Helper()
	if os.Getenv("INTAKE_LIVE_AWS") == "1" {
		return openLiveAWS(t)
	}
	runtime, err := exec.LookPath("podman")
	if err != nil {
		runtime, err = exec.LookPath("docker")
	}
	if err != nil {
		t.Fatal("AWS integration requires Podman or Docker")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	name := "factory-intake-aws-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if output, err := exec.CommandContext(cleanup, runtime, "rm", "-fv", name).CombinedOutput(); err != nil {
			t.Errorf("DynamoDB cleanup: %v %s", err, output)
		}
	})
	if output, err := exec.CommandContext(ctx, runtime, "run", "-d", "--name", name, "-p", "127.0.0.1::8000", intakeDynamoImage, "-jar", "DynamoDBLocal.jar", "-sharedDb", "-dbPath", "/home/dynamodblocal").CombinedOutput(); err != nil {
		t.Fatalf("start DynamoDB: %v %s", err, output)
	}
	output, err := exec.CommandContext(ctx, runtime, "port", name, "8000/tcp").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	if _, _, err := net.SplitHostPort(address); err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + address
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
	})
	client := sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: new(endpoint), Credentials: credentials, Retryer: aws.NopRetryer{}})
	for {
		if _, err = client.ListTables(ctx, &sdk.ListTablesInput{}); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("DynamoDB readiness", err)
		case <-time.After(25 * time.Millisecond):
		}
	}
	table := "intake-tests"
	_, err = client.CreateTable(ctx, &sdk.CreateTableInput{TableName: new(table), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: new("pk"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: new("sk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: new("pk"), KeyType: types.KeyTypeHash}, {AttributeName: new("sk"), KeyType: types.KeyTypeRange}}})
	if err != nil {
		t.Fatal(err)
	}
	storage := &objectFixture{objects: map[string][]byte{}}
	objects := httptest.NewServer(storage)
	t.Cleanup(objects.Close)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "local")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	namespace := rand.Text()
	options := persistence.AWSConfig{Table: table, Bucket: "intake-inputs", Prefix: "requests", ResourceNamespace: namespace + "-intake-resources", OwnerNamespace: namespace + "-intake-work", FactoryNamespace: namespace, DynamoDBEndpoint: endpoint, S3Endpoint: objects.URL}
	return awsFixture{Endpoint: endpoint, Objects: objects, Config: options}
}

func (fixture awsFixture) open(t *testing.T) *persistence.AWS {
	t.Helper()
	store, err := persistence.OpenAWS(t.Context(), fixture.Config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// objectFixture models conditional S3 storage for the real signed SDK. It is not
// evidence of live AWS permissions, replication, or another provider's behavior.
type objectFixture struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (fixture *objectFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(401)
		return
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(400)
			return
		}
		if _, exists := fixture.objects[r.URL.Path]; exists {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(412)
			fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code></Error>`)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		digest := sha256.Sum256(raw)
		if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(digest[:]) {
			w.WriteHeader(400)
			return
		}
		fixture.objects[r.URL.Path] = bytes.Clone(raw)
		w.Header().Set("ETag", `"local"`)
	case http.MethodGet:
		raw, exists := fixture.objects[r.URL.Path]
		if !exists {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
		w.Write(raw)
	default:
		w.WriteHeader(405)
	}
}

func (fixture awsFixture) command(t *testing.T, origin string, args ...string) *exec.Cmd {
	t.Helper()
	binary := os.Getenv("INTAKE_BINARY")
	if binary == "" {
		t.Fatal("INTAKE_BINARY required")
	}
	command := exec.CommandContext(t.Context(), binary, append([]string{"-queue-namespace", fixture.Config.FactoryNamespace, "-api-url", origin, "-allow-user-ids", "7"}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "INTAKE_") && !strings.HasPrefix(value, "GITHUB_INGRESS_TOKEN=") && !strings.HasPrefix(value, "GITLAB_INGRESS_TOKEN=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "INTAKE_TABLE="+fixture.Config.Table, "INTAKE_BUCKET="+fixture.Config.Bucket, "INTAKE_CONTENT_PREFIX="+fixture.Config.Prefix, "INTAKE_DYNAMODB_ENDPOINT="+fixture.Config.DynamoDBEndpoint, "INTAKE_S3_ENDPOINT="+fixture.Config.S3Endpoint, "GITHUB_INGRESS_TOKEN=test-secret", "GITLAB_INGRESS_TOKEN=test-secret")
	return command
}

func (fixture awsFixture) cli(t *testing.T, origin string, args ...string) string {
	t.Helper()
	output, err := fixture.command(t, origin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("AWS CLI %v: %v %s", args, err, output)
	}
	return string(output)
}
