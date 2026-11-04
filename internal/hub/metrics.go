package hub

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

type storeStep = store.RecommendedPoint

// Metrics holds the hub's Prometheus metrics (docs/contracts/metrics.md, section 3).
type Metrics struct {
	Registry *prometheus.Registry

	actions     *prometheus.CounterVec
	requests    *prometheus.CounterVec
	reqDuration *prometheus.HistogramVec
	runs        *prometheus.CounterVec
	runDuration prometheus.Histogram
	lastRun     *prometheus.GaugeVec
	pricingFB   *prometheus.GaugeVec
	clusterRecs *prometheus.GaugeVec
	snapshot    atomic.Pointer[[]*store.WorkloadView]
	dirty       atomic.Bool
	refreshMu   sync.Mutex
	lastRefresh time.Time
}

// NewMetrics registers all metrics.
func NewMetrics() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	m.actions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ro_actions_total", Help: "Events stored by the hub, one increment per resource listed in the event.",
	}, []string{"cluster", "namespace", "workload_kind", "workload", "type", "strategy", "resource", "reason"})
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ro_hub_http_requests_total", Help: "HTTP requests by route and status code.",
	}, []string{"route", "code"})
	m.reqDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ro_hub_http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	m.runs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ro_recommender_runs_total", Help: "Recommender runs by status.",
	}, []string{"status"})
	m.runDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "ro_recommender_run_duration_seconds", Help: "Duration of recommender runs.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
	})
	m.lastRun = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ro_recommender_last_success_timestamp_seconds", Help: "Unix time of the last successful run per cluster.",
	}, []string{"cluster"})
	m.pricingFB = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ro_recommender_pricing_fallback", Help: "1 when the last run used the default rate because no pricing series exist for the cluster.",
	}, []string{"cluster"})
	m.clusterRecs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ro_recommender_recommendations", Help: "Recommendations produced by the last run per cluster.",
	}, []string{"cluster"})
	m.Registry.MustRegister(m.actions, m.requests, m.reqDuration, m.runs, m.runDuration, m.lastRun, m.pricingFB, m.clusterRecs,
		&recCollector{m: m}, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) observeRequest(route string, code int, d time.Duration) {
	m.requests.WithLabelValues(route, strconv.Itoa(code)).Inc()
	m.reqDuration.WithLabelValues(route).Observe(d.Seconds())
}

// countAction increments ro_actions_total for a stored event. Like every hub series it carries
// the workload labels cluster, namespace, workload_kind and workload (docs/contracts/metrics.md §3).
func (m *Metrics) countAction(cluster string, e hubapi.Event) {
	res := e.Resources
	if len(res) == 0 {
		res = []string{""}
	}
	for _, r := range res {
		m.actions.WithLabelValues(cluster, e.Workload.Namespace, e.Workload.Kind, e.Workload.Name, e.Type, e.Strategy, r, e.Reason).Inc()
	}
}

func (m *Metrics) markDirty() { m.dirty.Store(true) }

// Refresh reloads the snapshot behind the ro_recommendation_* and ro_workload_* series.
func (m *Metrics) Refresh(ctx context.Context, st *store.Store) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	views, err := st.LoadWorkloads(ctx, store.WorkloadFilter{})
	if err != nil {
		return err
	}
	m.snapshot.Store(&views)
	m.dirty.Store(false)
	m.lastRefresh = time.Now()
	return nil
}

// RefreshLoop refreshes the snapshot every interval, or sooner after agent reports.
func (m *Metrics) RefreshLoop(ctx context.Context, st *store.Store, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if m.dirty.Load() || time.Since(m.lastRefresh) > interval {
				if err := m.Refresh(ctx, st); err != nil {
					log.WarnContext(ctx, "metrics refresh", "err", err)
				}
			}
		}
	}
}

var (
	descCPU = prometheus.NewDesc("ro_recommendation_cpu_cores", "CPU in cores (current or recommended request/limit).",
		[]string{"cluster", "namespace", "workload_kind", "workload", "container", "type", "source"}, nil)
	descMem = prometheus.NewDesc("ro_recommendation_memory_bytes", "Memory in bytes (current or recommended request/limit).",
		[]string{"cluster", "namespace", "workload_kind", "workload", "container", "type", "source"}, nil)
	descSavings = prometheus.NewDesc("ro_recommendation_monthly_savings_usd", "Estimated monthly savings; negative for increases.",
		[]string{"cluster", "namespace", "workload_kind", "workload", "container", "resource"}, nil)
	wlLabels  = []string{"cluster", "namespace", "workload_kind", "workload"}
	descConf  = prometheus.NewDesc("ro_recommendation_confidence", "Recommendation confidence 0-1.", wlLabels, nil)
	descCov   = prometheus.NewDesc("ro_recommendation_data_coverage_ratio", "Fraction of the window with data.", wlLabels, nil)
	descFlag  = prometheus.NewDesc("ro_recommendation_risk_flag", "1 while the risk flag is set.", append(wlLabels[:4:4], "flag"), nil)
	descGen   = prometheus.NewDesc("ro_recommendation_generated_timestamp_seconds", "Unix time the recommendation was generated.", wlLabels, nil)
	descReps  = prometheus.NewDesc("ro_recommendation_average_replicas", "Replicas used for savings.", wlLabels, nil)
	descMode  = prometheus.NewDesc("ro_workload_mode", "1 for the effective mode.", append(wlLabels[:4:4], "resource", "mode", "source"), nil)
	descPhase = prometheus.NewDesc("ro_workload_phase", "1 for the current phase.", append(wlLabels[:4:4], "phase"), nil)
)

type recCollector struct{ m *Metrics }

func (c *recCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descCPU, descMem, descSavings, descConf, descCov, descFlag, descGen, descReps, descMode, descPhase} {
		ch <- d
	}
}

func (c *recCollector) Collect(ch chan<- prometheus.Metric) {
	p := c.m.snapshot.Load()
	if p == nil {
		return
	}
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	for _, v := range *p {
		k := v.Key
		wl := []string{k.Cluster, k.Namespace, k.Kind, k.Name}
		with := func(extra ...string) []string { return append(append([]string{}, wl...), extra...) }
		m := v.Modes()
		srcOr := func(s string) string {
			if s == "" {
				return hubapi.SourceGlobalDefault
			}
			return s
		}
		gauge(descMode, 1, with("cpu", m.CPU, srcOr(m.CPUSource))...)
		gauge(descMode, 1, with("memory", m.Memory, srcOr(m.MemorySource))...)
		gauge(descPhase, 1, with(v.Phase())...)
		r := v.Rec
		if r == nil {
			continue
		}
		gauge(descConf, r.Confidence, wl...)
		gauge(descCov, r.DataCoverage.Ratio, wl...)
		gauge(descGen, float64(r.GeneratedAt.Unix()), wl...)
		gauge(descReps, r.AverageReplicas, wl...)
		for _, f := range r.RiskFlags {
			gauge(descFlag, 1, with(f)...)
		}
		for _, ct := range r.Containers {
			for _, src := range []struct {
				name string
				res  hubapi.Resources
			}{{"current", ct.Current}, {"recommended", ct.Recommended}} {
				for _, t := range []struct {
					name string
					rv   *hubapi.ResourceValues
				}{{"request", src.res.Requests}, {"limit", src.res.Limits}} {
					if t.rv == nil {
						continue
					}
					if t.rv.CPU != "" {
						if x, err := recommender.ParseCPU(t.rv.CPU); err == nil {
							gauge(descCPU, x, with(ct.Name, t.name, src.name)...)
						}
					}
					if t.rv.Memory != "" {
						if x, err := recommender.ParseMemory(t.rv.Memory); err == nil {
							gauge(descMem, x, with(ct.Name, t.name, src.name)...)
						}
					}
				}
			}
			if ct.MonthlySavingsUSD != nil {
				gauge(descSavings, ct.MonthlySavingsUSD.CPU, with(ct.Name, "cpu")...)
				gauge(descSavings, ct.MonthlySavingsUSD.Memory, with(ct.Name, "memory")...)
			}
		}
	}
}
