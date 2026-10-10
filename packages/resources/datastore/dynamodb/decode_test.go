package dynamodb

import (
	"encoding/json/v2"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func FuzzStoredRecord(f *testing.F) {
	f.Add([]byte(`{"kind":{"S":"request"},"id":{"S":"one"},"version":{"N":"1"},"digest":{"S":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"size":{"N":"0"},"state":{"B":"e30="}}`))
	f.Add([]byte(`{"version":{"S":"wrong type"}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Preserve untrusted DynamoDB attribute types and numeric strings rather than
		// marshaling a valid Go row, which would hide conversion error paths.
		var raw map[string]struct {
			S *string
			N *string
			B []byte
		}
		if json.Unmarshal(data, &raw) != nil {
			return
		}
		item := make(map[string]types.AttributeValue, len(raw))
		for name, value := range raw {
			switch {
			case value.S != nil:
				item[name] = &types.AttributeValueMemberS{Value: *value.S}
			case value.N != nil:
				item[name] = &types.AttributeValueMemberN{Value: *value.N}
			case value.B != nil:
				item[name] = &types.AttributeValueMemberB{Value: value.B}
			default:
				item[name] = &types.AttributeValueMemberNULL{Value: true}
			}
		}
		record, err := decode(item)
		if err == nil && record.Validate() != nil {
			t.Fatal("decoded invalid resource")
		}
	})
}
