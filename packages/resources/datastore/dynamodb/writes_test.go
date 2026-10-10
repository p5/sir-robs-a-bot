package dynamodb

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	queue "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
)

type transactionFailure struct {
	Client
	reasons []types.CancellationReason
}

func (client transactionFailure) TransactWriteItems(context.Context, *sdk.TransactWriteItemsInput, ...func(*sdk.Options)) (*sdk.TransactWriteItemsOutput, error) {
	return nil, &types.TransactionCanceledException{CancellationReasons: client.reasons}
}

func TestTransactionFailureClassification(t *testing.T) {
	cases := []struct {
		name     string
		codes    []string
		conflict bool
	}{
		{name: "stale resource", codes: []string{"ConditionalCheckFailed", "None"}, conflict: true},
		{name: "resource contention", codes: []string{"TransactionConflict", "None"}, conflict: true},
		{name: "outbox contention", codes: []string{"None", "TransactionConflict"}, conflict: true},
		{name: "capacity failure", codes: []string{"None", "ProvisionedThroughputExceeded"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reasons := make([]types.CancellationReason, len(test.codes))
			for index, code := range test.codes {
				reasons[index].Code = new(code)
			}
			store, err := New(transactionFailure{reasons: reasons}, "resources", "owner")
			if err != nil {
				t.Fatal(err)
			}
			record := datastore.Record{Key: queue.Key{Kind: "request", ID: "one"}, Version: 1,
				Content: content.Reference{SHA256: strings.Repeat("a", 64)}, State: []byte(`{}`)}
			err = store.commit(t.Context(), record, 0)
			if err == nil || errors.Is(err, datastore.ErrConflict) != test.conflict {
				t.Fatalf("classification: %v", err)
			}
		})
	}
}
