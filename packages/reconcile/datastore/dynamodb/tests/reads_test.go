package dynamodbtests

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

type observedRequest struct {
	target string
	body   []byte
}
type observeHTTP struct {
	lock     sync.Mutex
	requests []observedRequest
}

func (client *observeHTTP) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	client.lock.Lock()
	client.requests = append(client.requests, observedRequest{request.Header.Get("X-Amz-Target"), body})
	client.lock.Unlock()
	return http.DefaultClient.Do(request)
}
func TestQueueReadsAndEnqueueStayMetadataOnly(t *testing.T) {
	store, _, config, _ := newFixture(t)
	key := enqueueKey(t, store, "reads")
	transport := new(observeHTTP)
	options := newClient().Options()
	options.HTTPClient = transport
	observed, err := adapter.New(sdk.New(options), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.Enqueue(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 3 || transport.requests[0].target != "DynamoDB_20120810.GetItem" || transport.requests[1].target != "DynamoDB_20120810.GetItem" || transport.requests[2].target != "DynamoDB_20120810.TransactWriteItems" {
		t.Fatalf("enqueue did not check settings, read authority, and transact: %+v", transport.requests)
	}
	transport.requests = nil
	claim, err := observed.Claim(t.Context(), []string{key.Kind}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	queries, gets := 0, 0
	for _, request := range transport.requests {
		switch request.target {
		case "DynamoDB_20120810.Query":
			queries++
			if bytes.Contains(request.body, []byte(`"FilterExpression"`)) || !bytes.Contains(request.body, []byte("schedule:")) {
				t.Fatalf("discovery evaluated resource records or filtered a backlog: %s", request.body)
			}
		case "DynamoDB_20120810.GetItem":
			gets++
		}
	}
	if queries != 2 || gets != 1 {
		t.Fatalf("query=%d get=%d", queries, gets)
	}
	if err := observed.Commit(t.Context(), claim, datastore.Completion{}); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionUsesOneBoundedQuery(t *testing.T) {
	store, _, config, _ := newFixture(t)
	for _, id := range []string{"a", "b", "c"} {
		enqueueKey(t, store, id)
	}
	transport := new(observeHTTP)
	options := newClient().Options()
	options.HTTPClient = transport
	observed, err := adapter.New(sdk.New(options), config)
	if err != nil {
		t.Fatal(err)
	}
	// Warm the immutable settings check before measuring one inspection page.
	if _, err := observed.Get(t.Context(), datastore.Key{Kind: "fixture", ID: "a"}); err != nil {
		t.Fatal(err)
	}
	transport.requests = nil
	page, err := observed.List(t.Context(), datastore.ListRequest{Kind: "fixture", After: "a", Limit: 1})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Item.Key.ID != "b" || page.Next != "b" {
		t.Fatalf("inspection: %+v %v", page, err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("inspection made %d requests", len(transport.requests))
	}
	request := transport.requests[0]
	if request.target != "DynamoDB_20120810.Query" || !bytes.Contains(request.body, []byte(`"Limit":1`)) ||
		!bytes.Contains(request.body, []byte(`"ConsistentRead":true`)) || !bytes.Contains(request.body, []byte(`"ExclusiveStartKey"`)) {
		t.Fatalf("unbounded or inconsistent inspection: %s %s", request.target, request.body)
	}
}
