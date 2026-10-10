// Package s3 stores immutable snapshots with the AWS S3 SDK.
package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/p5/sir-robs-a-bot/packages/resources/content"
)

// Client permits SDK clients and wire-failure test fixtures. Credentials,
// endpoint selection, retries, and HTTP timeouts belong to the host.
type Client interface {
	PutObject(context.Context, *sdk.PutObjectInput, ...func(*sdk.Options)) (*sdk.PutObjectOutput, error)
	GetObject(context.Context, *sdk.GetObjectInput, ...func(*sdk.Options)) (*sdk.GetObjectOutput, error)
}

type Store struct {
	client Client
	bucket string
	prefix string
}

func New(client Client, bucket, prefix string) (*Store, error) {
	if client == nil || bucket == "" || strings.ContainsAny(bucket, "/\r\n") || strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\r\n") {
		return nil, errors.New("S3 client, bucket, and relative prefix are required")
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Store{client: client, bucket: bucket, prefix: prefix}, nil
}

func (store *Store) Put(ctx context.Context, key string, data []byte) error {
	if err := content.ValidateKey(key); err != nil {
		return err
	}
	if len(data) > content.MaxBytes {
		return errors.New("snapshot exceeds size limit")
	}
	digest := sha256.Sum256(data)
	_, err := store.client.PutObject(ctx, &sdk.PutObjectInput{
		Bucket: new(store.bucket), Key: new(store.prefix + key), Body: bytes.NewReader(data),
		ContentLength: new(int64(len(data))), ContentType: new("application/octet-stream"),
		IfNoneMatch: new("*"), ChecksumSHA256: new(base64.StdEncoding.EncodeToString(digest[:])),
	})
	if err == nil {
		return nil
	}
	failure, ok := errors.AsType[smithy.APIError](err)
	if !ok || failure.ErrorCode() != "PreconditionFailed" {
		return fmt.Errorf("S3 snapshot upload: %w", err)
	}
	// A conditional collision is safe only if the existing object has these bytes.
	// This also detects a corrupted object or a misconfigured storage namespace.
	existing, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, data) {
		return content.ErrCorrupt
	}
	return nil
}

func (store *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := content.ValidateKey(key); err != nil {
		return nil, err
	}
	result, err := store.client.GetObject(ctx, &sdk.GetObjectInput{Bucket: new(store.bucket), Key: new(store.prefix + key)})
	if err != nil {
		if failure, ok := errors.AsType[smithy.APIError](err); ok && failure.ErrorCode() == "NoSuchKey" {
			return nil, content.ErrNotFound
		}
		return nil, fmt.Errorf("S3 snapshot read: %w", err)
	}
	if result == nil || result.Body == nil {
		return nil, errors.New("S3 returned no object body")
	}
	defer result.Body.Close()
	if aws.ToInt64(result.ContentLength) > content.MaxBytes {
		return nil, content.ErrCorrupt
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, content.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read S3 snapshot body: %w", err)
	}
	if len(data) > content.MaxBytes {
		return nil, content.ErrCorrupt
	}
	return data, nil
}

var _ content.Store = (*Store)(nil)
