package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics collects process-local operations, durations, and active dispatches.
// Construct it with NewMetrics, then register it with a host-owned registry.
// Share one instance between engines when they should contribute to the same series.
// Labels contain registered kinds and fixed operation/outcome names, never
// resource IDs, error messages, or priority values.
type Metrics struct {
	operations *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	active     *prometheus.GaugeVec
}

var _ prometheus.Collector = (*Metrics)(nil)

// NewMetrics creates an unregistered collector. It uses no global registry and
// starts no listener. The host owns registry conflicts and endpoint authorization.
func NewMetrics() *Metrics {
	labels := []string{"kind", "operation", "outcome"}
	return &Metrics{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factory_reconcile_operations_total",
			Help: "Dispatch operations by kind, operation, and outcome.",
		}, labels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "factory_reconcile_operation_duration_seconds",
			Help:    "Dispatch operation duration in seconds.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 5, 30, 120},
		}, labels),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factory_reconcile_active",
			Help: "Dispatches active in this process, including queue completion.",
		}, []string{"kind"}),
	}
}

func (metrics *Metrics) Describe(channel chan<- *prometheus.Desc) {
	metrics.operations.Describe(channel)
	metrics.duration.Describe(channel)
	metrics.active.Describe(channel)
}

func (metrics *Metrics) Collect(channel chan<- prometheus.Metric) {
	metrics.operations.Collect(channel)
	metrics.duration.Collect(channel)
	metrics.active.Collect(channel)
}

func (metrics *Metrics) record(kind, operation, outcome string, duration time.Duration) {
	if metrics == nil {
		return
	}
	metrics.operations.WithLabelValues(kind, operation, outcome).Inc()
	metrics.duration.WithLabelValues(kind, operation, outcome).Observe(duration.Seconds())
}

func (metrics *Metrics) changeActive(kind string, delta int64) {
	if metrics != nil {
		metrics.active.WithLabelValues(kind).Add(float64(delta))
	}
}

func (engine *Engine) observe(kind, operation string, started time.Time, err error) {
	if engine.config.Metrics == nil {
		return
	}
	outcome := "success"
	switch {
	case errors.Is(err, datastore.ErrNoWork):
		outcome = "idle"
	case errors.Is(err, datastore.ErrLeaseLost):
		outcome = "lease_lost"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		outcome = "cancelled"
	case err != nil:
		outcome = "error"
	}
	engine.config.Metrics.record(kind, operation, outcome, time.Since(started))
}
