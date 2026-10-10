package s3

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
)

type readClient struct {
	body   string
	length int64
}

func (client readClient) GetObject(context.Context, *sdk.GetObjectInput, ...func(*sdk.Options)) (*sdk.GetObjectOutput, error) {
	return &sdk.GetObjectOutput{Body: io.NopCloser(strings.NewReader(client.body)), ContentLength: new(client.length)}, nil
}
func (readClient) PutObject(context.Context, *sdk.PutObjectInput, ...func(*sdk.Options)) (*sdk.PutObjectOutput, error) {
	return nil, errors.New("unexpected put")
}

func TestOversizedResponseIsRejected(t *testing.T) {
	key := "sha256/" + strings.Repeat("a", 64)
	for _, client := range []readClient{{body: "small", length: content.MaxBytes + 1}, {body: strings.Repeat("x", content.MaxBytes+1)}} {
		store, _ := New(client, "snapshots", "owner")
		if _, err := store.Get(t.Context(), key); !errors.Is(err, content.ErrCorrupt) {
			t.Fatalf("oversized response: %v", err)
		}
	}
}

func FuzzObjectRead(f *testing.F) {
	f.Add("sha256/"+strings.Repeat("a", 64), "body", int64(4))
	f.Add("../unsafe", "", int64(-1))
	f.Fuzz(func(t *testing.T, key, body string, length int64) {
		store, _ := New(readClient{body: body, length: length}, "snapshots", "owner")
		data, err := store.Get(t.Context(), key)
		if err == nil && (content.ValidateKey(key) != nil || len(data) > content.MaxBytes) {
			t.Fatal("invalid successful object read")
		}
	})
}
