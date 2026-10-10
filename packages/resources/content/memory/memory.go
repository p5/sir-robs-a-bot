// Package memory provides explicitly ephemeral content storage for tests and demos.
package memory

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/p5/sir-robs-a-bot/packages/resources/content"
)

type Store struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func New() *Store { return &Store{objects: make(map[string][]byte)} }

func (store *Store) Put(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := content.ValidateKey(key); err != nil {
		return err
	}
	if len(data) > content.MaxBytes {
		return errors.New("snapshot exceeds size limit")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.objects[key]; ok {
		if !bytes.Equal(existing, data) {
			return content.ErrCorrupt
		}
		return nil
	}
	store.objects[key] = bytes.Clone(data)
	return nil
}

func (store *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := content.ValidateKey(key); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	data, ok := store.objects[key]
	if !ok {
		return nil, content.ErrNotFound
	}
	return bytes.Clone(data), nil
}
