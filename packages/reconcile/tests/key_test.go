package reconciletests

import (
	"testing"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func TestKeyValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   datastore.Key
		valid bool
	}{
		{name: "URL", key: datastore.Key{Kind: "github-path", ID: "https://github.com/p5/example/blob/main/README.md"}, valid: true},
		{name: "URN", key: datastore.Key{Kind: "request", ID: "urn:factory:request:one"}, valid: true},
		{name: "opaque ID", key: datastore.Key{Kind: "fixture", ID: "one"}, valid: true},
		{name: "blank kind", key: datastore.Key{Kind: " ", ID: "one"}},
		{name: "blank ID", key: datastore.Key{Kind: "fixture", ID: "\t"}},
		{name: "control character", key: datastore.Key{Kind: "fixture", ID: "one\x00two"}},
		{name: "invalid encoding", key: datastore.Key{Kind: "fixture", ID: "\xff"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.key.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, valid = %v", err, test.valid)
			}
		})
	}
}
