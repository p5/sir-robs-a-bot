package memory

import (
	"strconv"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
)

func BenchmarkDispatchBacklog(b *testing.B) {
	for _, records := range []int{1, 1000, 10000} {
		b.Run(strconv.Itoa(records), func(b *testing.B) {
			store := New(Config{})
			for index := range records {
				key := datastore.Key{Kind: "bench", ID: strconv.Itoa(index)}
				if err := store.Enqueue(b.Context(), key); err != nil {
					b.Fatal(err)
				}
				claim, err := store.Claim(b.Context(), []string{key.Kind}, time.Minute)
				if err != nil {
					b.Fatal(err)
				}
				if err := store.Commit(b.Context(), claim, datastore.Completion{}); err != nil {
					b.Fatal(err)
				}
			}
			key := datastore.Key{Kind: "bench", ID: "hot"}
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Enqueue(b.Context(), key); err != nil {
					b.Fatal(err)
				}
				claim, err := store.Claim(b.Context(), []string{key.Kind}, time.Minute)
				if err != nil {
					b.Fatal(err)
				}
				if err := store.Commit(b.Context(), claim, datastore.Completion{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
