package resourceunit

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	"testing"
)

func FuzzRecord(f *testing.F) {
	f.Add([]byte(`{"Key":{"Kind":"request","ID":"one"},"Version":1,"Content":{"SHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Size":1},"State":{}}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var record datastore.Record
		if json.Unmarshal(data, &record) != nil || record.Validate() != nil {
			return
		}
		encoded, err := json.Marshal(record)
		if err != nil || !jsontext.Value(encoded).IsValid() {
			t.Fatalf("valid record cannot round trip: %v", err)
		}
	})
}
