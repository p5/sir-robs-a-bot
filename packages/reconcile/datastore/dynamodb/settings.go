package dynamodb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// checkSettings binds a namespace to immutable scheduling configuration. Without
// this record, clients with different shard counts could silently hide each
// other's resources. Read-only operations never create a namespace. Successful
// checks are cached because adapter operations cannot modify these settings.
func (store *Store) checkSettings(ctx context.Context, create bool) error {
	select {
	case store.settingsGate <- struct{}{}:
		defer func() { <-store.settingsGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if store.settingsChecked {
		return ctx.Err()
	}
	key := scheduleKey("settings:"+base64.RawURLEncoding.EncodeToString([]byte(store.namespace)), "schedule")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		output, err := store.client.GetItem(ctx, &sdk.GetItemInput{TableName: new(store.table), Key: key, ConsistentRead: new(true)}, containDecoderPanics)
		if err != nil {
			return fmt.Errorf("read scheduling settings: %w", err)
		}
		if len(output.Item) != 0 {
			pk, pkOK := output.Item["pk"].(*types.AttributeValueMemberS)
			sk, skOK := output.Item["sk"].(*types.AttributeValueMemberS)
			layout, layoutOK := output.Item["layout"].(*types.AttributeValueMemberS)
			shards, shardsOK := output.Item["shards"].(*types.AttributeValueMemberN)
			if !pkOK || !skOK || pk.Value != key["pk"].(*types.AttributeValueMemberS).Value || sk.Value != "schedule" || !layoutOK || !shardsOK || layout.Value != schedulingLayout || shards.Value != strconv.FormatUint(uint64(store.shards), 10) {
				return errors.New("namespace scheduling settings do not match this adapter")
			}
			store.settingsChecked = true
			return nil
		}
		if !create {
			return nil
		}
		item := scheduleKey(key["pk"].(*types.AttributeValueMemberS).Value, "schedule")
		item["layout"] = &types.AttributeValueMemberS{Value: schedulingLayout}
		item["shards"] = number(uint64(store.shards))
		_, err = store.client.PutItem(ctx, &sdk.PutItemInput{
			TableName: new(store.table), Item: item, ConditionExpression: new("attribute_not_exists(#pk)"),
			ExpressionAttributeNames: map[string]string{"#pk": "pk"},
		}, func(options *sdk.Options) {
			options.Retryer = aws.NopRetryer{}
			containDecoderPanics(options)
		})
		if _, conflict := errors.AsType[*types.ConditionalCheckFailedException](err); conflict {
			continue
		}
		if err != nil {
			return fmt.Errorf("create scheduling settings (outcome may be unknown): %w", err)
		}
		store.settingsChecked = true
		return nil
	}
}
