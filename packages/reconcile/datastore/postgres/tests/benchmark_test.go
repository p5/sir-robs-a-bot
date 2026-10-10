package postgrestests

import (
	"crypto/rand"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
)

func BenchmarkDispatchBacklog(b *testing.B) {
	for _, records := range []int{0, 256} {
		b.Run(strconv.Itoa(records), func(b *testing.B) {
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { db.Close() })
			store, err := postgres.New(db, "bench-"+rand.Text())
			if err != nil {
				b.Fatal(err)
			}
			for index := range records {
				key := datastore.Key{Kind: "bench", ID: "a-" + strconv.Itoa(index)}
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
			key := datastore.Key{Kind: "bench", ID: "z-hot"}
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
