package reconciletests

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/internal/teststore"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsDistinguishAcknowledgedAndUnknownCompletion(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		name := "acknowledged"
		if uncertain {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			metrics := reconcile.NewMetrics()
			registry := prometheus.NewPedanticRegistry()
			if err := registry.Register(metrics); err != nil {
				t.Fatal(err)
			}
			model := teststore.New()
			enqueueKey(t, model, "never-a-metric-label")
			var store datastore.Store = model
			failure := errors.New("response lost")
			if uncertain {
				store = uncertainStore{Store: model, failure: failure}
			}
			config := testConfig()
			config.Metrics = metrics
			engine, err := reconcile.New(store, map[string]reconcile.Reconciler{"fixture": reconcile.ReconcilerFunc(func(context.Context, datastore.Key) (reconcile.Result, error) { return reconcile.Result{}, nil })}, config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.ReconcileOne(t.Context())
			if uncertain != errors.Is(err, failure) {
				t.Fatalf("dispatch outcome: %v", err)
			}
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			seenDispatch := false
			for _, family := range families {
				for _, metric := range family.Metric {
					labels := map[string]string{}
					for _, label := range metric.Label {
						labels[label.GetName()] = label.GetValue()
					}
					for key := range labels {
						if key != "kind" && key != "operation" && key != "outcome" {
							t.Fatalf("unbounded label: %s", key)
						}
					}
					if family.GetName() == "factory_reconcile_active" && metric.GetGauge().GetValue() != 0 {
						t.Fatal("active dispatch leaked")
					}
					if family.GetName() == "factory_reconcile_operations_total" && labels["operation"] == "dispatch" {
						want := "completed"
						if uncertain {
							want = "commit_error"
						}
						if labels["outcome"] != want || metric.GetCounter().GetValue() != 1 {
							t.Fatalf("wrong completion metric: %v", labels)
						}
						seenDispatch = true
					}
					if histogram := metric.Histogram; histogram != nil && histogram.GetSampleCount() != 1 {
						t.Fatal("duration observation missing or duplicated")
					}
				}
			}
			if !seenDispatch {
				t.Fatal("dispatch metric missing")
			}
		})
	}
}

func TestMetricsTrackConcurrentDispatches(t *testing.T) {
	metrics := reconcile.NewMetrics()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(metrics)
	store := teststore.New()
	const calls = 16
	for index := range calls {
		enqueueKey(t, store, strconv.Itoa(index))
	}
	entered := make(chan struct{}, calls)
	finish := make(chan struct{})
	config := testConfig()
	config.Metrics = metrics
	engine, err := reconcile.New(store, map[string]reconcile.Reconciler{
		"fixture": reconcile.ReconcilerFunc(func(ctx context.Context, key datastore.Key) (reconcile.Result, error) {
			entered <- struct{}{}
			select {
			case <-finish:
				return reconcile.Result{}, nil
			case <-ctx.Done():
				return reconcile.Result{}, ctx.Err()
			}
		}),
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range calls {
		workers.Go(func() {
			if worked, err := engine.ReconcileOne(t.Context()); err != nil || !worked {
				t.Errorf("dispatch: worked=%v error=%v", worked, err)
			}
		})
	}
	// Always release callbacks, including when an assertion fails.
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(finish) }); workers.Wait() })
	readyCtx, cancelReady := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelReady()
	for range calls {
		select {
		case <-entered:
		case <-readyCtx.Done():
			t.Fatal("dispatch callbacks did not start")
		}
	}
	assertActive := func(want float64) {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() == "factory_reconcile_active" && len(family.Metric) == 1 && family.Metric[0].GetGauge().GetValue() == want {
				return
			}
		}
		t.Fatalf("active gauge did not equal %g", want)
	}
	assertActive(calls)
	release.Do(func() { close(finish) })
	workers.Wait()
	assertActive(0)
}
