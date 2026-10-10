package persistence

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/p5/sir-robs-a-bot/services/intake/internal/intake"
)

// Index stores bounded reference rows partitioned by connection. Its contents can
// be rebuilt from request resources; the dispatcher's Indexed flag tracks writes.
type Index struct {
	Client    *sdk.Client
	Table     string
	Namespace string
}

func text(value string) types.AttributeValue { return &types.AttributeValueMemberS{Value: value} }

func (index *Index) partition(connection intake.Connection) string {
	return index.Namespace + "#index#" + intake.ConnectionKey(connection).ID
}

func (index *Index) Put(ctx context.Context, request intake.Request) error {
	if request.Validate() != nil {
		return errors.New("invalid request index input")
	}
	raw, err := json.Marshal(request.Source.Connection)
	if err != nil {
		return err
	}
	_, err = index.Client.PutItem(ctx, &sdk.PutItemInput{TableName: new(index.Table), Item: map[string]types.AttributeValue{
		"pk": text(index.partition(request.Source.Connection)), "sk": text(request.ID), "id": text(request.ID), "connection": &types.AttributeValueMemberB{Value: raw},
	}})
	return err
}

func (index *Index) List(ctx context.Context, connection intake.Connection) ([]string, error) {
	if connection.Validate() != nil {
		return nil, errors.New("invalid index connection")
	}
	result, err := index.Client.Query(ctx, &sdk.QueryInput{TableName: new(index.Table), ConsistentRead: new(true), KeyConditionExpression: new("pk = :pk"), ExpressionAttributeValues: map[string]types.AttributeValue{":pk": text(index.partition(connection))}, Limit: new(int32(100))})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("missing index response")
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		id, ok := item["id"].(*types.AttributeValueMemberS)
		account, accountOK := item["connection"].(*types.AttributeValueMemberB)
		pk, pkOK := item["pk"].(*types.AttributeValueMemberS)
		sk, skOK := item["sk"].(*types.AttributeValueMemberS)
		var stored intake.Connection
		if !ok || !accountOK || !pkOK || !skOK || pk.Value != index.partition(connection) || sk.Value != id.Value || !validIndexID(id.Value, connection.Provider) || len(account.Value) > 4096 || json.Unmarshal(account.Value, &stored) != nil || stored != connection {
			return nil, errors.New("invalid request index row")
		}
		ids = append(ids, id.Value)
	}
	return ids, nil
}

func validIndexID(id, provider string) bool {
	prefix := provider + ":"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	digest := strings.TrimPrefix(id, prefix)
	raw, err := hex.DecodeString(digest)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == digest
}
