package reconciletests

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	adapter "github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/dynamodb"
)

func FuzzDynamoDBValidation(f *testing.F) {
	f.Add("factory", "namespace", "fixture", "one", int64(0))
	f.Add("bad/name", "\xff", "\x00", "", int64(-1))
	f.Add(strings.Repeat("t", 256), strings.Repeat("n", 129), strings.Repeat("k", 257), strings.Repeat("i", 1025), int64(1))
	f.Fuzz(func(t *testing.T, table, namespace, kind, id string, lease int64) {
		validTable := len(table) >= 3 && len(table) <= 255
		for _, char := range table {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-", char) {
				validTable = false
			}
		}
		validConfig := validTable && identityIsValid(namespace) && len(namespace) <= 128
		_, err := adapter.New(new(sdk.Client), adapter.Config{Table: table, Namespace: namespace})
		if (err == nil) != validConfig {
			t.Fatalf("constructor validation = %v, valid=%v", err, validConfig)
		}
		store, err := adapter.New(new(sdk.Client), adapter.Config{Table: "factory", Namespace: "namespace"})
		if err != nil {
			t.Fatal(err)
		}
		key := datastore.Key{Kind: kind, ID: id}
		if !identityIsValid(kind) || !identityIsValid(id) || len(kind) > 256 || len(id) > 1024 {
			if _, err := store.Get(t.Context(), key); err == nil {
				t.Fatal("get accepted invalid key")
			}
			if err := store.Enqueue(t.Context(), key); err == nil {
				t.Fatal("update accepted invalid key")
			}
			if err := store.Enqueue(t.Context(), key); err == nil {
				t.Fatal("wake accepted invalid key")
			}
			if err := store.Commit(t.Context(), datastore.Claim{Key: key, Fence: 1, Sequence: 1}, datastore.Completion{}); err == nil {
				t.Fatal("commit accepted invalid key")
			}
		}
		if lease <= 0 {
			claim := datastore.Claim{Key: datastore.Key{Kind: "fixture", ID: "one"}, Fence: 1, Sequence: 1}
			if err := store.Renew(t.Context(), claim, time.Duration(lease)); err == nil {
				t.Fatal("invalid renewal duration accepted")
			}
		}
		if !identityIsValid(kind) || len(kind) > 256 || lease <= 0 {
			if _, err := store.Claim(t.Context(), []string{kind}, time.Duration(lease)); err == nil {
				t.Fatal("claim accepted invalid kind or duration")
			}
		}
	})
}

type fuzzCredentials struct{}

func (fuzzCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
}

type responseHTTP struct{ body []byte }

func (client responseHTTP) Do(request *http.Request) (*http.Response, error) {
	body := client.body
	if request.Header.Get("X-Amz-Target") == "DynamoDB_20120810.GetItem" {
		input, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if bytes.Contains(input, []byte("settings:")) {
			body = []byte(`{"Item":{"pk":{"S":"settings:bmFtZXNwYWNl"},"sk":{"S":"schedule"},"layout":{"S":"transactional-schedules-v1"},"shards":{"N":"1"}}}`)
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
		Body:       io.NopCloser(bytes.NewReader(body)), Request: request,
	}, nil
}

func FuzzDynamoDBReadResponse(f *testing.F) {
	f.Add([]byte(`{"Item":{"":{"":{"":0A0`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"Items":[],"LastEvaluatedKey":{"sk":{"S":"one"}}}`))
	f.Add([]byte(`{"Items":[{"sk":{"S":"one"}}]}`))
	f.Add([]byte(`{"Item":{"schema":{"N":"1"}}}`))
	f.Add([]byte(`{"Item":{"pk":{"S":"bmFtZXNwYWNl:Zml4dHVyZQ"},"sk":{"S":"one"},"schema":{"N":"1"},"revision":{"S":"revision"},"fence":{"N":"0"},"sequence":{"N":"1"},"active":{"BOOL":false},"due":{"N":"0"},"lease":{"N":"0"},"not_before":{"N":"0"},"item":{"M":{"Key":{"M":{"Kind":{"S":"fixture"},"ID":{"S":"one"}}},"Priority":{"N":"0"},"Abandoned":{"N":"0"},"Failures":{"N":"0"},"LastError":{"S":""},"Pending":{"BOOL":true}}}}}`))
	digest := sha256.Sum256([]byte("one"))
	f.Add([]byte(fmt.Sprintf(`{"Item":{"pk":{"S":"bmFtZXNwYWNl:Zml4dHVyZQ"},"sk":{"S":"one"},"schema":{"N":"1"},"layout":{"S":"transactional-schedules-v1"},"shards":{"N":"1"},"schedule_state":{"S":"ready"},"schedule_pk":{"S":"schedule:bmFtZXNwYWNl:Zml4dHVyZQ:ready:00"},"schedule_sk":{"S":"4294967295#%020d#%x"},"revision":{"S":"revision"},"fence":{"N":"0"},"sequence":{"N":"1"},"active":{"BOOL":false},"due":{"N":"0"},"lease":{"N":"0"},"not_before":{"N":"0"},"item":{"M":{"Key":{"M":{"Kind":{"S":"fixture"},"ID":{"S":"one"}}},"Priority":{"N":"0"},"Abandoned":{"N":"0"},"Failures":{"N":"0"},"LastError":{"S":""},"Pending":{"BOOL":true}}}}}`, 0, digest)))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1024*1024 {
			t.Skip("response exceeds the fuzz harness budget")
		}
		client := sdk.New(sdk.Options{
			Region: "us-east-1", BaseEndpoint: new("http://example.invalid"),
			Credentials: fuzzCredentials{}, Retryer: aws.NopRetryer{}, HTTPClient: responseHTTP{body: body},
		})
		store, err := adapter.New(client, adapter.Config{Table: "factory", Namespace: "namespace"})
		if err != nil {
			t.Fatal(err)
		}
		key := datastore.Key{Kind: "fixture", ID: "one"}
		resource, err := store.Get(t.Context(), key)
		if err == nil && (resource.Key != key || len(resource.LastError) > adapter.MaxFailureBytes) {
			t.Fatalf("accepted malformed queue item: %+v", resource)
		}
		page, err := store.List(t.Context(), datastore.ListRequest{Kind: key.Kind, Limit: 2})
		if err == nil {
			if len(page.Entries) > 2 {
				t.Fatal("inspection accepted an oversized response")
			}
			previous := ""
			for _, entry := range page.Entries {
				if entry.Item.Key.Kind != key.Kind || entry.Item.Key.ID <= previous || entry.DueAt.IsZero() {
					t.Fatalf("malformed inspection entry: %+v", entry)
				}
				previous = entry.Item.Key.ID
			}
			if page.Next != "" && (previous == "" || page.Next != previous) {
				t.Fatalf("malformed continuation: %+v", page)
			}
		}
	})
}

// Scheduling responses use a separate transport so arbitrary resource responses
// cannot prevent the fuzzer from reaching either scheduling query boundary.
type scheduleResponseHTTP struct {
	body  []byte
	timer bool
}

func (client scheduleResponseHTTP) Do(request *http.Request) (*http.Response, error) {
	body := []byte(`{}`)
	if request.Header.Get("X-Amz-Target") == "DynamoDB_20120810.Query" {
		input, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		timer := bytes.Contains(input, []byte(":timer:"))
		if timer == client.timer {
			body = client.body
		}
	}
	return (responseHTTP{body: body}).Do(request)
}
func FuzzDynamoDBSchedulingResponse(f *testing.F) {
	f.Add([]byte(`{}`), false)
	f.Add([]byte(`{"Items":[{"pk":{"S":"foreign"}}]}`), true)
	f.Add([]byte(`{"Items":[],"LastEvaluatedKey":{"pk":{"S":"foreign"},"sk":{"S":"one"}}}`), false)
	digest := sha256.Sum256([]byte("one"))
	for _, timer := range []bool{false, true} {
		state, order := "ready", fmt.Sprintf("4294967295#%020d#%x", 0, digest)
		if timer {
			state, order = "timer", fmt.Sprintf("%020d#4294967295#%x", 0, digest)
		}
		seed := fmt.Sprintf(`{"Items":[{"pk":{"S":"schedule:bmFtZXNwYWNl:Zml4dHVyZQ:%s:00"},"sk":{"S":"%s"},"schema":{"N":"1"},"revision":{"S":"revision"},"key":{"M":{"Kind":{"S":"fixture"},"ID":{"S":"one"}}},"due":{"N":"0"},"eligible":{"N":"0"},"priority":{"N":"0"}}]}`, state, order)
		f.Add([]byte(seed), timer)
		// The fake service repeats its body on the next page. A valid cursor
		// followed by a repeated item must fail rather than loop forever.
		continuation := fmt.Sprintf(`,"LastEvaluatedKey":{"pk":{"S":"schedule:bmFtZXNwYWNl:Zml4dHVyZQ:%s:00"},"sk":{"S":"%s"}}}`, state, order)
		f.Add([]byte(strings.TrimSuffix(seed, "}")+continuation), timer)
	}
	f.Fuzz(func(t *testing.T, body []byte, timer bool) {
		if len(body) > 64*1024 {
			t.Skip("response exceeds scheduling fuzz budget")
		}
		client := sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: new("http://example.invalid"), Credentials: fuzzCredentials{}, Retryer: aws.NopRetryer{}, HTTPClient: scheduleResponseHTTP{body: body, timer: timer}})
		store, err := adapter.New(client, adapter.Config{Table: "factory", Namespace: "namespace"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		claim, err := store.Claim(ctx, []string{"fixture"}, time.Minute)
		if err == nil {
			t.Fatalf("scheduling hints granted a claim without authority: %+v", claim)
		}
	})
}
