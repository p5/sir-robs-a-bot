package reconciletests

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/memory"
)

func BenchmarkEngineDispatch(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "metrics-disabled"
		if enabled {
			name = "metrics-enabled"
		}
		b.Run(name, func(b *testing.B) {
			store := memory.New(memory.Config{})
			config := testConfig()
			config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
			if enabled {
				config.Metrics = reconcile.NewMetrics()
			}
			engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
				"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) { return reconcile.Result{}, nil }),
			}, config)
			if err != nil {
				b.Fatal(err)
			}
			key := datastore.Key{Kind: "fixture", ID: "hot"}
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Enqueue(b.Context(), key); err != nil {
					b.Fatal(err)
				}
				if worked, err := engine.ReconcileOne(b.Context()); err != nil || !worked {
					b.Fatalf("dispatch: worked=%v error=%v", worked, err)
				}
			}
		})
	}
}
