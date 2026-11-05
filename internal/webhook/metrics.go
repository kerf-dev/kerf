package webhook

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the webhook's operational series (prefix kerf_webhook_, docs/contracts/metrics.md).
type Metrics struct {
	Admissions *prometheus.CounterVec
	Duration   *prometheus.HistogramVec
	CacheReady prometheus.Gauge
}

// NewMetrics creates the series and registers them on reg (if not nil).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Admissions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kerf_webhook_admissions_total",
			Help: "Pod admissions handled, by result (mutated, skipped, error) and reason (empty for mutated).",
		}, []string{"result", "reason"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kerf_webhook_admission_duration_seconds",
			Help:    "Time spent handling one pod admission, by result.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2},
		}, []string{"result"}),
		CacheReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_webhook_cache_synced",
			Help: "1 once the informer caches (RightsizingRecommendations, ReplicaSets, Jobs) have synced.",
		}),
	}
	// Pre-create the result series so rates exist from the start.
	for _, r := range []string{ResultMutated, ResultSkipped, ResultError} {
		m.Duration.WithLabelValues(r)
	}
	m.Admissions.WithLabelValues(ResultMutated, "")
	if reg != nil {
		reg.MustRegister(m.Admissions, m.Duration, m.CacheReady)
	}
	return m
}

// Observe records one admission.
func (m *Metrics) Observe(result, reason string, d time.Duration) {
	if m == nil {
		return
	}
	m.Admissions.WithLabelValues(result, reason).Inc()
	m.Duration.WithLabelValues(result).Observe(d.Seconds())
}
