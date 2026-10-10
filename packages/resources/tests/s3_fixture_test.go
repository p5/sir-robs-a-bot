package resourcestests

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	adapter "github.com/p5/sir-robs-a-bot/packages/resources/content/s3"
)

// This fixture exercises the real S3 SDK's signed HTTP requests and bounded
// responses. It models storage semantics; it is not evidence of live AWS behavior.
type s3Fixture struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (fixture *s3Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := fixture.objects[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code></Error>`)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, content.MaxBytes+1))
		if err != nil || len(data) > content.MaxBytes {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		digest := sha256.Sum256(data)
		if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(digest[:]) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.objects[r.URL.Path] = bytes.Clone(data)
		w.Header().Set("ETag", `"fixture"`)
	case http.MethodGet:
		data, ok := fixture.objects[r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		digest := sha256.Sum256(data)
		w.Header().Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(digest[:]))
		w.Write(data)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func openObjects(t *testing.T, prefix string) (*adapter.Store, *s3Fixture) {
	t.Helper()
	fixture := &s3Fixture{objects: make(map[string][]byte)}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client := sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: new(server.URL), UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(credentials), Retryer: aws.NopRetryer{}, HTTPClient: server.Client()})
	store, err := adapter.New(client, "snapshots", prefix)
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}
