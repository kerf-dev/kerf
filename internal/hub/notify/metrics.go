package notify

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the notification series (docs/contracts/metrics.md). A nil *Metrics is a no-op.
//
//	kerf_notification_deliveries_total{channel_type,result}        counter: success, retry, failed
//	kerf_notification_last_success_timestamp_seconds{channel}      gauge, from the database (leader)
type Metrics struct {
	deliveries *prometheus.CounterVec
	lastOK     atomic.Pointer[[]channelSuccess]
}

type channelSuccess struct {
	name string
	unix float64
}

// Result label values of kerf_notification_deliveries_total.
const (
	ResultSuccess = "success"
	ResultRetry   = "retry"
	ResultFailed  = "failed"
)

// NewMetrics creates the metrics. Register Deliveries on the registry of every replica and
// LastSuccess (a Collector) on the leader-only registerer.
func NewMetrics() *Metrics {
	m := &Metrics{deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_notification_deliveries_total",
		Help: "Notification delivery attempts by channel type and result: success, retry (will be tried again) or failed (given up).",
	}, []string{"channel_type", "result"})}
	for _, t := range []string{"slack", "teams", "webhook"} {
		for _, r := range []string{ResultSuccess, ResultRetry, ResultFailed} {
			m.deliveries.WithLabelValues(t, r)
		}
	}
	return m
}

// Deliveries returns the counter.
func (m *Metrics) Deliveries() prometheus.Collector { return m.deliveries }

// LastSuccess returns the collector of kerf_notification_last_success_timestamp_seconds.
func (m *Metrics) LastSuccess() prometheus.Collector { return (*lastSuccessCollector)(m) }

type lastSuccessCollector Metrics

var lastSuccessDesc = prometheus.NewDesc("kerf_notification_last_success_timestamp_seconds",
	"Unix time of the last successful notification delivery per channel (absent until the first success).", []string{"channel"}, nil)

func (c *lastSuccessCollector) Describe(ch chan<- *prometheus.Desc) { ch <- lastSuccessDesc }

func (c *lastSuccessCollector) Collect(ch chan<- prometheus.Metric) {
	snap := (*Metrics)(c).lastOK.Load()
	if snap == nil {
		return
	}
	for _, s := range *snap {
		ch <- prometheus.MustNewConstMetric(lastSuccessDesc, prometheus.GaugeValue, s.unix, s.name)
	}
}

func (m *Metrics) count(channelType string, ok, retry bool) {
	if m == nil {
		return
	}
	r := ResultFailed
	switch {
	case ok:
		r = ResultSuccess
	case retry:
		r = ResultRetry
	}
	m.deliveries.WithLabelValues(channelType, r).Inc()
}

func (m *Metrics) setLastSuccess(cs []Channel) {
	if m == nil {
		return
	}
	var out []channelSuccess
	for _, c := range cs {
		if c.LastSuccessAt != nil {
			out = append(out, channelSuccess{name: c.Name, unix: float64(c.LastSuccessAt.Unix())})
		}
	}
	m.lastOK.Store(&out)
}
