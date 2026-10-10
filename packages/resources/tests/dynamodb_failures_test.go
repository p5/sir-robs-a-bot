package resourcestests

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/resources/datastore/dynamodb"
)

type unknownTransaction struct {
	adapter.Client
	fail bool
}

func (client *unknownTransaction) TransactWriteItems(ctx context.Context, input *sdk.TransactWriteItemsInput, options ...func(*sdk.Options)) (*sdk.TransactWriteItemsOutput, error) {
	result, err := client.Client.TransactWriteItems(ctx, input, options...)
	if err == nil && client.fail {
		client.fail = false
		return nil, errors.New("injected lost transaction response")
	}
	return result, err
}

func TestDynamoDBUnknownCommitPreservesInputAndOutbox(t *testing.T) {
	client := &unknownTransaction{Client: newClient(), fail: true}
	state, err := adapter.New(client, table, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	objects, _ := openObjects(t, "unknown-commit")
	repository, _ := resources.New(objects, state)
	key := queue.Key{Kind: "request", ID: "unknown"}
	if _, _, err := repository.Create(t.Context(), key, []byte("original"), []byte(`{}`)); err == nil {
		t.Fatal("lost reply did not reach caller")
	}
	record, created, err := repository.Create(t.Context(), key, []byte("changed"), []byte(`{}`))
	if err != nil || created {
		t.Fatalf("retry: %+v %v %v", record, created, err)
	}
	_, data, err := repository.Get(t.Context(), key)
	if err != nil || string(data) != "original" {
		t.Fatalf("unknown commit changed input: %q %v", data, err)
	}
	pending, err := state.Pending(t.Context(), 100)
	if err != nil || len(pending) != 1 {
		t.Fatalf("unknown commit lost delivery: %+v %v", pending, err)
	}
}

func TestDynamoDBNamespaceIsolationAndMaximumEscapedKey(t *testing.T) {
	first := openState(t, rand.Text())
	second := openState(t, rand.Text())
	key := queue.Key{Kind: strings.Repeat(`"`, 128), ID: strings.Repeat(`\`, 512)}
	record := datastore.Record{Key: key, Version: 1, Content: content.Reference{SHA256: strings.Repeat("a", 64)}, State: []byte(`{}`)}
	if _, _, err := first.Create(t.Context(), record); err != nil {
		t.Fatal("maximum key:", err)
	}
	if _, err := second.Get(t.Context(), key); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("namespace leaked: %v", err)
	}
	pending, err := first.Pending(t.Context(), 100)
	if err != nil || len(pending) != 1 || pending[0].Key != key {
		t.Fatalf("escaped identity: %+v %v", pending, err)
	}
	pending, err = second.Pending(t.Context(), 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("outbox namespace leaked: %+v %v", pending, err)
	}
}
