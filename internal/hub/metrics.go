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

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/realized"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
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
	leader      prometheus.Gauge
	transitions prometheus.Counter
	isLeader    atomic.Pointer[func() bool]
	snapshot    atomic.Pointer[[]*store.WorkloadView]
	dirty       atomic.Bool
	refreshMu   sync.Mutex
	lastRefresh atomic.Int64 // unix nanoseconds
	// realized exports the realized-savings totals (kerf_hub_realized_*), refreshed by the worker.
	realized *realized.Collector
}

// NewMetrics registers all metrics.
func NewMetrics() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	m.actions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_actions_total", Help: "Events stored by the hub, one increment per resource listed in the event.",
	}, []string{"cluster", "namespace", "workload_kind", "workload", "type", "strategy", "resource", "reason"})
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_hub_http_requests_total", Help: "HTTP requests by route and status code.",
	}, []string{"route", "code"})
	m.reqDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "kerf_hub_http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	m.runs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_recommender_runs_total", Help: "Recommender runs by status.",
	}, []string{"status"})
	m.runDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "kerf_recommender_run_duration_seconds", Help: "Duration of recommender runs.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
	})
	m.lastRun = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kerf_recommender_last_success_timestamp_seconds", Help: "Unix time of the last successful run per cluster.",
	}, []string{"cluster"})
	m.pricingFB = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kerf_recommender_pricing_fallback", Help: "1 when the last run used the default rate because no pricing series exist for the cluster.",
	}, []string{"cluster"})
	m.clusterRecs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kerf_recommender_recommendations", Help: "Recommendations produced by the last run per cluster.",
	}, []string{"cluster"})
	m.leader = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kerf_hub_leader", Help: "1 while this hub replica is the leader (runs the recommender, readiness checks and sweeps), 0 otherwise.",
	})
	m.transitions = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "kerf_hub_leader_transitions_total", Help: "Changes of kerf_hub_leader on this replica (leadership acquired or lost).",
	})
	// Per-process series every replica exports (sum them across replicas).
	m.Registry.MustRegister(m.actions, m.requests, m.reqDuration, m.runs, m.runDuration, m.leader, m.transitions,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	// State read from the database: only the leader exports it, so N replicas don't export N copies.
	m.realized = &realized.Collector{}
	m.LeaderOnly().MustRegister(m.lastRun, m.pricingFB, m.clusterRecs, &recCollector{m: m}, m.realized)
	return m
}

// SetLeaderFunc makes the database-state series leader-only (WO-18a): they are exported while f
// returns true. Without it (one hub, tests) they are always exported.
func (m *Metrics) SetLeaderFunc(f func() bool) { m.isLeader.Store(&f) }

func (m *Metrics) exportState() bool {
	if f := m.isLeader.Load(); f != nil {
		return (*f)()
	}
	return true
}

func (m *Metrics) setLeader(v bool) {
	if v {
		m.leader.Set(1)
	} else {
		m.leader.Set(0)
	}
	m.transitions.Inc()
}

// LeaderOnly returns a Registerer whose collectors are exported only while this replica leads.
func (m *Metrics) LeaderOnly() prometheus.Registerer { return leaderOnlyRegisterer{m: m} }

type leaderOnlyRegisterer struct{ m *Metrics }

func (r leaderOnlyRegisterer) Register(c prometheus.Collector) error {
	return r.m.Registry.Register(leaderOnlyCollector{Collector: c, m: r.m})
}

func (r leaderOnlyRegisterer) MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		if err := r.Register(c); err != nil {
			panic(err)
		}
	}
}

func (r leaderOnlyRegisterer) Unregister(c prometheus.Collector) bool {
	return r.m.Registry.Unregister(leaderOnlyCollector{Collector: c, m: r.m})
}

type leaderOnlyCollector struct {
	prometheus.Collector
	m *Metrics
}

func (c leaderOnlyCollector) Collect(ch chan<- prometheus.Metric) {
	if c.m.exportState() {
		c.Collector.Collect(ch)
	}
}

// seedRunGauges sets the kerf_recommender_* per-cluster gauges from the database (a new leader:
// another replica may have run the last runs).
func (m *Metrics) seedRunGauges(ctx context.Context, st *store.Store) error {
	runs, err := st.LastClusterRuns(ctx)
	if err != nil {
		return err
	}
	m.lastRun.Reset()
	m.pricingFB.Reset()
	m.clusterRecs.Reset()
	for _, r := range runs {
		fb := 0.0
		if r.PricingFallback {
			fb = 1
		}
		m.lastRun.WithLabelValues(r.Cluster).Set(float64(r.FinishedAt.Unix()))
		m.pricingFB.WithLabelValues(r.Cluster).Set(fb)
		m.clusterRecs.WithLabelValues(r.Cluster).Set(float64(r.Recommendations))
	}
	return nil
}

func (m *Metrics) observeRequest(route string, code int, d time.Duration) {
	m.requests.WithLabelValues(route, strconv.Itoa(code)).Inc()
	m.reqDuration.WithLabelValues(route).Observe(d.Seconds())
}

// countAction increments kerf_actions_total for a stored event. Like every hub series it carries
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

// Refresh reloads the snapshot behind the kerf_recommendation_* and kerf_workload_* series.
func (m *Metrics) Refresh(ctx context.Context, st *store.Store) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	views, err := st.LoadWorkloads(ctx, store.WorkloadFilter{})
	if err != nil {
		return err
	}
	m.snapshot.Store(&views)
	m.dirty.Store(false)
	m.lastRefresh.Store(time.Now().UnixNano())
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
			if m.dirty.Load() || time.Since(time.Unix(0, m.lastRefresh.Load())) > interval {
				if err := m.Refresh(ctx, st); err != nil {
					log.WarnContext(ctx, "metrics refresh", "err", err)
				}
			}
		}
	}
}

var (
	descCPU = prometheus.NewDesc("kerf_recommendation_cpu_cores", "CPU in cores (current or recommended request/limit).",
		[]string{"cluster", "namespace", "workload_kind", "workload", "container", "type", "source"}, nil)
	descMem = prometheus.NewDesc("kerf_recommendation_memory_bytes", "Memory in bytes (current or recommended request/limit).",
		[]string{"cluster", "namespace", "workload_kind", "workload", "container", "type", "source"}, nil)
	descSavings = prometheus.NewDesc("kerf_recommendation_monthly_savings_usd", "Estimated monthly savings; negative for increases.",
		[]string{"cluster", "namespace", "workload_kind", "workload", "container", "resource"}, nil)
	wlLabels  = []string{"cluster", "namespace", "workload_kind", "workload"}
	descConf  = prometheus.NewDesc("kerf_recommendation_confidence", "Recommendation confidence 0-1.", wlLabels, nil)
	descCov   = prometheus.NewDesc("kerf_recommendation_data_coverage_ratio", "Fraction of the window with data.", wlLabels, nil)
	descFlag  = prometheus.NewDesc("kerf_recommendation_risk_flag", "1 while the risk flag is set.", append(wlLabels[:4:4], "flag"), nil)
	descGen   = prometheus.NewDesc("kerf_recommendation_generated_timestamp_seconds", "Unix time the recommendation was generated.", wlLabels, nil)
	descReps  = prometheus.NewDesc("kerf_recommendation_average_replicas", "Replicas used for savings.", wlLabels, nil)
	descMode  = prometheus.NewDesc("kerf_workload_mode", "1 for the effective mode.", append(wlLabels[:4:4], "resource", "mode", "source"), nil)
	descPhase = prometheus.NewDesc("kerf_workload_phase", "1 for the current phase.", append(wlLabels[:4:4], "phase"), nil)
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
