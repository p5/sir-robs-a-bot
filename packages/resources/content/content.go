// Package content stores and verifies immutable, content-addressed snapshots.
package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// MaxBytes bounds a snapshot in memory. Large streaming artifacts are a separate
// capability; callers must not split a snapshot and pretend it is atomic.
const MaxBytes = 16 << 20

var (
	ErrNotFound = errors.New("content not found")
	ErrCorrupt  = errors.New("content integrity check failed")
)

// Reference names immutable bytes within the owner's configured content store.
// It contains no bucket, credential, URL, or mutable object alias.
type Reference struct {
	SHA256 string
	Size   int64
}

func (ref Reference) Validate() error {
	digest, err := hex.DecodeString(ref.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != ref.SHA256 || ref.Size < 0 || ref.Size > MaxBytes {
		return errors.New("invalid content reference")
	}
	return nil
}

func (ref Reference) Key() string { return "sha256/" + ref.SHA256 }

// Store implements immutable put and bounded get. Put must never replace an
// existing object with different bytes. A successful Put establishes durability
// according to the configured backend. Implementations return ErrCorrupt for
// confirmed corruption, and ordinary errors for temporary unavailability.
type Store interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
}

// Repository hides addressing and integrity checks from resource consumers.
type Repository struct{ store Store }

func New(store Store) (*Repository, error) {
	if store == nil {
		return nil, errors.New("content store is required")
	}
	return &Repository{store: store}, nil
}

func (repository *Repository) Put(ctx context.Context, data []byte) (Reference, error) {
	if len(data) > MaxBytes {
		return Reference{}, errors.New("content exceeds snapshot limit")
	}
	digest := sha256.Sum256(data)
	ref := Reference{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))}
	if err := repository.store.Put(ctx, ref.Key(), data); err != nil {
		return Reference{}, fmt.Errorf("store content: %w", err)
	}
	return ref, nil
}

func (repository *Repository) Get(ctx context.Context, ref Reference) ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	data, err := repository.store.Get(ctx, ref.Key())
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != ref.Size || hex.EncodeToString(digest[:]) != ref.SHA256 {
		return nil, ErrCorrupt
	}
	return data, nil
}

// ValidateKey prevents adapters from accepting paths outside the content layout.
func ValidateKey(key string) error {
	const prefix = "sha256/"
	if len(key) != len(prefix)+64 || key[:len(prefix)] != prefix {
		return errors.New("invalid content key")
	}
	return (Reference{SHA256: key[len(prefix):]}).Validate()
}
