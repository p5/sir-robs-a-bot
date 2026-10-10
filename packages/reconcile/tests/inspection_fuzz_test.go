package reconciletests

import (
	"database/sql"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/internal/teststore"
)

func FuzzInspectionInputs(f *testing.F) {
	f.Add("fixture", "", int64(1))
	f.Add("fixture", "https://example.test/a?x=1", int64(100))
	f.Add("\xff", "\x00", int64(-1))
	f.Add("fixture", "one", int64(101))
	f.Fuzz(func(t *testing.T, kind, after string, limit int64) {
		request := datastore.ListRequest{Kind: kind, After: after, Limit: int(limit)}
		valid := identityIsValid(kind) && (after == "" || identityIsValid(after)) && limit >= 1 && limit <= 100
		if err := request.Validate(); (err == nil) != valid {
			t.Fatalf("inspection validation: %v, valid=%v", err, valid)
		}
		model := teststore.New()
		page, err := model.List(t.Context(), request)
		if (err == nil) != valid || len(page.Entries) != 0 || page.Next != "" {
			t.Fatalf("empty model inspection: %+v %v", page, err)
		}
		if valid && len(kind) <= 256 && len(after) <= 1024 {
			return
		}
		pg, err := postgres.New(new(sql.DB), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		ddb, err := dynamodb.New(new(sdk.Client), dynamodb.Config{Table: "factory", Namespace: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		// Zero clients would fail if an invalid input reached the backend.
		for _, inspector := range []datastore.Inspector{pg, ddb} {
			if _, err := inspector.List(t.Context(), request); err == nil {
				t.Fatal("invalid inspection reached the backend")
			}
		}
	})
}
