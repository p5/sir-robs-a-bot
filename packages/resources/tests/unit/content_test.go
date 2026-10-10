package resourceunit

import (
	"context"
	"errors"
	"testing"

	"github.com/p5/sir-robs-a-bot/packages/resources/content"
	"github.com/p5/sir-robs-a-bot/packages/resources/content/memory"
)

type changedObject struct{ content.Store }

func (changedObject) Get(context.Context, string) ([]byte, error) { return []byte("tampered"), nil }

func TestContentIntegrityAndMissingObject(t *testing.T) {
	store := memory.New()
	repository, _ := content.New(store)
	ref, err := repository.Put(t.Context(), []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := repository.Get(t.Context(), ref)
	if err != nil || string(data) != "original" {
		t.Fatalf("round trip: %q %v", data, err)
	}
	corrupt, _ := content.New(changedObject{store})
	if _, err := corrupt.Get(t.Context(), ref); !errors.Is(err, content.ErrCorrupt) {
		t.Fatal("accepted corrupt bytes")
	}
	missing, _ := content.New(memory.New())
	if _, err := missing.Get(t.Context(), ref); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("missing content not reported")
	}
}

func FuzzReference(f *testing.F) {
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", int64(1))
	f.Add("../unsafe", int64(-1))
	f.Fuzz(func(t *testing.T, digest string, size int64) {
		ref := content.Reference{SHA256: digest, Size: size}
		if ref.Validate() == nil && content.ValidateKey(ref.Key()) != nil {
			t.Fatal("valid reference has invalid key")
		}
	})
}
