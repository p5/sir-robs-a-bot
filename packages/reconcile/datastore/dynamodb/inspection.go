package dynamodb

import (
	"context"
	"errors"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

var _ datastore.Inspector = (*Store)(nil)

// List makes one strongly consistent, bounded Query. It never scans the table
// or follows continuation keys internally. Diagnostics belong to this query;
// dispatch reads separate, smaller scheduling entries.
func (store *Store) List(ctx context.Context, request datastore.ListRequest) (datastore.Page, error) {
	if err := request.Validate(); err != nil {
		return datastore.Page{}, err
	}
	if len(request.Kind) > 256 || len(request.After) > 1024 {
		return datastore.Page{}, errors.New("resource kind must fit 256 bytes and cursor must fit 1024 bytes")
	}
	if err := store.checkSettings(ctx, false); err != nil {
		return datastore.Page{}, err
	}

	input := &sdk.QueryInput{
		TableName: new(store.table), ConsistentRead: new(true), Limit: new(int32(request.Limit)),
		KeyConditionExpression:   new("#pk = :pk"),
		ExpressionAttributeNames: map[string]string{"#pk": "pk"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: store.partition(request.Kind)},
		},
	}
	if request.After != "" {
		input.ExclusiveStartKey = store.itemKey(datastore.Key{Kind: request.Kind, ID: request.After})
	}
	output, err := store.client.Query(ctx, input, containDecoderPanics)
	if err != nil {
		return datastore.Page{}, err
	}
	if len(output.Items) > request.Limit {
		return datastore.Page{}, errors.New("inspection response exceeds requested limit")
	}
	page := datastore.Page{}
	previous := request.After
	for _, item := range output.Items {
		id, ok := item["sk"].(*types.AttributeValueMemberS)
		if !ok || id.Value <= previous {
			return datastore.Page{}, errors.New("invalid inspection response order")
		}
		current, err := store.decode(item, datastore.Key{Kind: request.Kind, ID: id.Value})
		if err != nil {
			return datastore.Page{}, err
		}
		entry := datastore.Entry{Item: current.Item, DueAt: time.UnixMilli(current.Due).UTC()}
		if current.NotBefore != 0 {
			entry.NotBefore = time.UnixMilli(current.NotBefore).UTC()
		}
		if current.Active {
			entry.LeaseUntil = time.UnixMilli(current.Lease).UTC()
		}
		page.Entries = append(page.Entries, entry)
		previous = id.Value
	}
	if len(output.LastEvaluatedKey) != 0 {
		partition, pkOK := output.LastEvaluatedKey["pk"].(*types.AttributeValueMemberS)
		id, idOK := output.LastEvaluatedKey["sk"].(*types.AttributeValueMemberS)
		if !pkOK || !idOK || partition.Value != store.partition(request.Kind) ||
			id.Value <= request.After || id.Value != previous || len(page.Entries) == 0 {
			return datastore.Page{}, errors.New("invalid inspection continuation key")
		}
		page.Next = id.Value
	}
	return page, nil
}
