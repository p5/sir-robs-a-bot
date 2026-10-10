package dynamodb

import (
	"context"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func (store *Store) read(ctx context.Context, key datastore.Key) (record, error) {
	output, err := store.client.GetItem(ctx, &sdk.GetItemInput{
		TableName: new(store.table), Key: store.itemKey(key), ConsistentRead: new(true),
	}, containDecoderPanics)
	if err != nil {
		return record{}, fmt.Errorf("read coordination record: %w", err)
	}
	if len(output.Item) == 0 {
		return record{}, datastore.ErrNotFound
	}
	return store.decode(output.Item, key)
}
