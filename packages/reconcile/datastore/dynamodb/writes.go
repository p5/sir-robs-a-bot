package dynamodb

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// replace changes the authority and its discovery entry in one transaction.
// next retains the old schedule identity until this function computes its new
// one. False means only a definitive resource-revision conflict, never an
// uncertain response or a failed scheduling invariant.
func (store *Store) replace(ctx context.Context, next record, previousRevision string) (bool, error) {
	oldPartition, oldID := next.SchedulePartition, next.ScheduleID
	now, err := store.schedulingTime()
	if err != nil {
		return false, err
	}
	next.Revision = rand.Text()
	next.Layout, next.Shards = schedulingLayout, store.shards
	next.ScheduleState, next.SchedulePartition, next.ScheduleID = "", "", ""
	var entry scheduleEntry
	if next.Item.Pending {
		next.ScheduleState = readyState
		if eligibility(next) > now {
			next.ScheduleState = timerState
		}
		entry = store.entryFor(next, next.ScheduleState)
		next.SchedulePartition, next.ScheduleID = entry.Partition, entry.ID
	}
	actions, err := store.transactionItems(next, oldPartition, oldID, previousRevision, entry)
	if err != nil {
		return false, err
	}
	_, err = store.client.TransactWriteItems(ctx, &sdk.TransactWriteItemsInput{TransactItems: actions, ClientRequestToken: new(rand.Text())}, func(options *sdk.Options) {
		options.Retryer = aws.NopRetryer{}
		containDecoderPanics(options)
	})
	if resourceRevisionConflict(err, len(actions)) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("write coordination transaction (outcome may be unknown): %w", err)
	}
	return true, nil
}

// transactionItems never targets an item twice. An unchanged scheduling key is
// replaced in place; a changed key is deleted and recreated with the authority.
func (store *Store) transactionItems(next record, oldPartition, oldID, previousRevision string, entry scheduleEntry) ([]types.TransactWriteItem, error) {
	item, err := attributevalue.MarshalMap(next)
	if err != nil {
		return nil, fmt.Errorf("encode coordination record: %w", err)
	}
	authority := &types.Put{TableName: new(store.table), Item: item,
		ConditionExpression: new("attribute_not_exists(#pk)"), ExpressionAttributeNames: map[string]string{"#pk": "pk"}}
	if previousRevision != "" {
		authority.ConditionExpression = new("#revision = :revision")
		authority.ExpressionAttributeNames = map[string]string{"#revision": "revision"}
		authority.ExpressionAttributeValues = map[string]types.AttributeValue{":revision": &types.AttributeValueMemberS{Value: previousRevision}}
	}
	actions := []types.TransactWriteItem{{Put: authority}}
	sameEntry := oldPartition == next.SchedulePartition && oldID == next.ScheduleID
	if oldPartition != "" && !sameEntry {
		actions = append(actions, types.TransactWriteItem{Delete: &types.Delete{TableName: new(store.table), Key: scheduleKey(oldPartition, oldID),
			ConditionExpression: new("#revision = :revision"), ExpressionAttributeNames: map[string]string{"#revision": "revision"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":revision": &types.AttributeValueMemberS{Value: previousRevision}}}})
	}
	if next.SchedulePartition != "" {
		marker, err := attributevalue.MarshalMap(entry)
		if err != nil {
			return nil, fmt.Errorf("encode scheduling entry: %w", err)
		}
		put := &types.Put{TableName: new(store.table), Item: marker, ConditionExpression: new("attribute_not_exists(#pk)"), ExpressionAttributeNames: map[string]string{"#pk": "pk"}}
		if sameEntry {
			put.ConditionExpression = new("#revision = :revision AND #key.#id = :id")
			put.ExpressionAttributeNames = map[string]string{"#revision": "revision", "#key": "key", "#id": "ID"}
			put.ExpressionAttributeValues = map[string]types.AttributeValue{":revision": &types.AttributeValueMemberS{Value: previousRevision}, ":id": &types.AttributeValueMemberS{Value: next.ID}}
		}
		actions = append(actions, types.TransactWriteItem{Put: put})
	}
	return actions, nil
}

// resourceRevisionConflict permits retries only when DynamoDB explicitly
// cancelled the whole transaction because the authority changed. Marker
// invariant failures alone must surface instead of looping forever.
func resourceRevisionConflict(err error, actions int) bool {
	if cancelled, ok := errors.AsType[*types.TransactionCanceledException](err); ok {
		if len(cancelled.CancellationReasons) == actions && cancelled.CancellationReasons[0].Code != nil && *cancelled.CancellationReasons[0].Code == "ConditionalCheckFailed" {
			// Stale transactions may also observe stale marker revisions. No action
			// committed, and rereading the resource is sufficient to try again.
			definite := true
			for _, reason := range cancelled.CancellationReasons {
				if reason.Code == nil || (*reason.Code != "None" && *reason.Code != "ConditionalCheckFailed") {
					definite = false
				}
			}
			if definite {
				return true
			}
		}
	}
	return false
}
