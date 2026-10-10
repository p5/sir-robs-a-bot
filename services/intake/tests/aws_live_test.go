package factorytests

import (
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
	dynamo "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	s3content "github.com/p5/sir-robs-a-bot/packages/resources/content/s3"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	statedb "github.com/p5/sir-robs-a-bot/packages/resources/datastore/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore/storetest"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/persistence"
)

// Live tests use externally provisioned, disposable resources. The test process
// never provisions or deletes infrastructure, and each fixture owns a namespace.
func openLiveAWS(t *testing.T) awsFixture {
	t.Helper()
	table := os.Getenv("INTAKE_LIVE_TABLE")
	bucket := os.Getenv("INTAKE_LIVE_BUCKET")
	if !strings.HasPrefix(table, "factory-live-") || !strings.HasPrefix(bucket, "factory-live-") {
		t.Fatal("live tests require disposable factory-live- table and bucket names")
	}
	if os.Getenv("AWS_REGION") == "" {
		t.Fatal("live tests require AWS_REGION")
	}
	namespace := "live-" + rand.Text()
	return awsFixture{Config: persistence.AWSConfig{
		Table: table, Bucket: bucket, Prefix: namespace,
		ResourceNamespace: namespace + "-intake-resources",
		OwnerNamespace:    namespace + "-intake-work", FactoryNamespace: namespace,
	}}
}

func TestAWSLiveStorageContract(t *testing.T) {
	if os.Getenv("INTAKE_LIVE_AWS") != "1" {
		t.Skip("requires explicit live AWS opt-in and disposable resources")
	}
	fixture := openLiveAWS(t)
	settings, err := config.LoadDefaultConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("DynamoDB resource contract", func(t *testing.T) {
		storetest.Run(t, func(t *testing.T) storetest.Fixture {
			namespace := rand.Text()
			open := func() datastore.Store {
				store, err := statedb.New(dynamo.NewFromConfig(settings), fixture.Config.Table, namespace)
				if err != nil {
					t.Fatal(err)
				}
				return store
			}
			return storetest.Fixture{Store: open(), Reopen: open}
		})
	})
	t.Run("S3 conditional writes and integrity", func(t *testing.T) {
		client := s3sdk.NewFromConfig(settings)
		objects, err := s3content.New(client, fixture.Config.Bucket, fixture.Config.Prefix)
		if err != nil {
			t.Fatal(err)
		}
		repository, err := content.New(objects)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := repository.Put(t.Context(), []byte("original"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.Put(t.Context(), []byte("original")); err != nil {
			t.Fatal("conditional duplicate:", err)
		}
		raw, err := repository.Get(t.Context(), ref)
		if err != nil || string(raw) != "original" {
			t.Fatalf("verified read: %q %v", raw, err)
		}
		if err := objects.Put(t.Context(), ref.Key(), []byte("changed")); !errors.Is(err, content.ErrCorrupt) {
			t.Fatalf("conditional collision accepted different bytes: %v", err)
		}
		if _, err := objects.Get(t.Context(), "sha256/"+strings.Repeat("a", 64)); !errors.Is(err, content.ErrNotFound) {
			t.Fatalf("missing object: %v", err)
		}
	})
}
