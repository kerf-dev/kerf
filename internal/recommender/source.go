package recommender

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// RawContainer holds the raw series of one container.
type RawContainer struct {
	Name string
	Init bool
	// CPU and Memory are per-step usage series (max across pods) over the longest window.
	CPU, Memory []Sample
	// Throttle is the per-step throttling ratio over the risk window.
	Throttle []Sample
	// OOMKilled is true when the container was OOM-killed within the risk window.
	OOMKilled bool
	// Restarts within the risk window, summed over pods.
	Restarts float64
	Current  Current
	// Used24hCPU / Used24hMemory: mean over the last 24 h of the usage summed over pods.
	Used24hCPU, Used24hMemory float64
	// Images are the container's images within the inventory lookback (JVM detection, WO-12).
	Images []string
}

// RawWorkload holds the raw data of one workload.
type RawWorkload struct {
	Key        hubapi.WorkloadKey
	Containers map[string]*RawContainer
	// Pods is the number of running pods per step over the longest window.
	Pods []Sample
	HPA  *HPA
	// HasRequests is true when the workload was found by the inventory (KSM) queries.
	HasRequests bool
	// Live is true when the workload has request/limit series at the window end (default lookback):
	// it has pods right now (WO-04e).
	Live bool
}

// ClusterData is the raw data of one cluster as of End.
type ClusterData struct {
	Cluster   string
	End       time.Time
	Window    time.Duration
	Workloads map[hubapi.WorkloadKey]*RawWorkload
	Pricing   Pricing
}

// SortedKeys returns the workload keys in a stable order.
func (d *ClusterData) SortedKeys() []hubapi.WorkloadKey {
	keys := make([]hubapi.WorkloadKey, 0, len(d.Workloads))
	for k := range d.Workloads {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID() < keys[j].ID() })
	return keys
}

// Source loads cluster data with PromQL.
type Source struct {
	Q   Querier
	Cfg Config
	// Concurrency bounds parallel queries per cluster.
	Concurrency int
	// Binder returns where a cluster's metrics are (D-025): its datasource and the matcher of its
	// series. nil, or a nil matcher, = Q with label cluster (the queries before D-025). Pricing
	// series are always read from Q (the hub's own store).
	Binder func(ctx context.Context, cluster string) (Querier, *datasource.Matcher, error)
}

// bind returns the querier and matcher of a cluster's metrics.
func (s *Source) bind(ctx context.Context, cluster string) (Querier, *datasource.Matcher, error) {
	if s.Binder == nil {
		return s.Q, nil, nil
	}
	q, m, err := s.Binder(ctx, cluster)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics binding of %s: %w", cluster, err)
	}
	if q == nil {
		q = s.Q
	}
	return q, m, nil
}

// Load reads everything needed to recommend for one cluster. window is the longest profile window
// in use for the cluster; End is aligned down to the step.
func (s *Source) Load(ctx context.Context, cluster string, end time.Time, window time.Duration) (*ClusterData, error) {
	cfg := s.Cfg
	step := cfg.Step
	end = end.Truncate(step)
	start := end.Add(-window)
	data, binding, err := s.bind(ctx, cluster)
	if err != nil {
		return nil, err
	}
	q := Queries{Cluster: cluster, RateWindow: cfg.RateWindow, Binding: binding}
	d := &ClusterData{Cluster: cluster, End: end, Window: window, Workloads: map[hubapi.WorkloadKey]*RawWorkload{}}

	var mu sync.Mutex
	workload := func(l Labels) *RawWorkload {
		k := hubapi.WorkloadKey{Cluster: cluster, Namespace: l["namespace"], Kind: l["workload_kind"], Name: l["workload"]}
		if k.Namespace == "" || k.Name == "" || !hubapi.ValidKind(k.Kind) {
			return nil
		}
		w, ok := d.Workloads[k]
		if !ok {
			w = &RawWorkload{Key: k, Containers: map[string]*RawContainer{}}
			d.Workloads[k] = w
		}
		return w
	}
	container := func(l Labels) *RawContainer {
		w := workload(l)
		if w == nil || l["container"] == "" {
			return nil
		}
		c, ok := w.Containers[l["container"]]
		if !ok {
			c = &RawContainer{Name: l["container"]}
			w.Containers[l["container"]] = c
		}
		return c
	}

	// Range queries return large matrices; keep few in flight to bound hub and backend memory.
	conc := s.Concurrency
	if conc <= 0 {
		conc = 2
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(conc)
	rangeQ := func(name, query string, from time.Time, st time.Duration, apply func(Series)) {
		g.Go(func() error {
			res, err := data.Range(gctx, query, from, end, st)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, r := range res {
				apply(r)
			}
			return nil
		})
	}
	instantOn := func(src Querier, name, query string, apply func(VectorSample)) {
		g.Go(func() error {
			res, err := src.Instant(gctx, query, end)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, r := range res {
				apply(r)
			}
			return nil
		})
	}
	instantQ := func(name, query string, apply func(VectorSample)) { instantOn(data, name, query, apply) }

	rangeQ("cpu", q.CPUUsage("max", nil), start, step, func(r Series) {
		if c := container(r.Labels); c != nil {
			c.CPU = r.Samples
		}
	})
	rangeQ("memory", q.MemoryUsage("max", nil, step), start, step, func(r Series) {
		if c := container(r.Labels); c != nil {
			c.Memory = r.Samples
		}
	})
	rangeQ("pods", q.RunningPods(step), start, step, func(r Series) {
		if w := workload(r.Labels); w != nil {
			w.Pods = r.Samples
		}
	})
	day := end.Add(-24 * time.Hour)
	rangeQ("cpu24h", q.CPUUsage("sum", nil), day, step, func(r Series) {
		if c := container(r.Labels); c != nil {
			c.Used24hCPU = mean(r.Samples)
		}
	})
	rangeQ("memory24h", q.MemoryUsage("sum", nil, step), day, step, func(r Series) {
		if c := container(r.Labels); c != nil {
			c.Used24hMemory = mean(r.Samples)
		}
	})
	risk := end.Add(-cfg.RiskWindow)
	rangeQ("throttle", q.ThrottleRatio(), risk, step, func(r Series) {
		if c := container(r.Labels); c != nil {
			c.Throttle = r.Samples
		}
	})
	instantQ("oom", q.OOMKills(cfg.RiskWindow), func(r VectorSample) {
		if c := container(r.Labels); c != nil && r.Sample.V > 0 {
			c.OOMKilled = true
		}
	})
	instantQ("restarts", q.Restarts(cfg.RiskWindow), func(r VectorSample) {
		if c := container(r.Labels); c != nil {
			c.Restarts = r.Sample.V
		}
	})

	// Current requests/limits: the live value (default lookback) wins over the value from
	// InventoryLookback, which keeps CronJobs and finished backfills in the inventory.
	type resKey struct {
		c        *RawContainer
		resource string
		limit    bool
	}
	live := map[resKey]bool{}
	setRes := func(r VectorSample, limit, init, isLive bool) {
		c := container(r.Labels)
		if c == nil {
			return
		}
		k := resKey{c, r.Labels["resource"], limit}
		if !isLive && live[k] {
			return
		}
		if isLive {
			live[k] = true
		}
		if init {
			c.Init = true
		}
		v := r.Sample.V
		switch {
		case k.resource == "cpu" && !limit:
			c.Current.CPURequest = v
		case k.resource == "cpu" && limit:
			c.Current.CPULimit = v
		case k.resource == "memory" && !limit:
			c.Current.MemoryRequest = v
		case k.resource == "memory" && limit:
			c.Current.MemoryLimit = v
		}
		w := workload(r.Labels)
		w.HasRequests = true
		w.Live = w.Live || isLive
	}
	var inv []func()
	for _, init := range []bool{false, true} {
		for _, metric := range []string{"requests", "limits"} {
			for _, lb := range []time.Duration{0, cfg.InventoryLookback} {
				var res []VectorSample
				query := q.Resources(metric, init, lb, nil)
				name := fmt.Sprintf("%s(init=%v,lookback=%s)", metric, init, lb)
				g.Go(func() error {
					r, err := data.Instant(gctx, query, end)
					if err != nil {
						return fmt.Errorf("%s: %w", name, err)
					}
					res = r
					return nil
				})
				limit, isLive, init := metric == "limits", lb == 0, init
				inv = append(inv, func() {
					for _, r := range res {
						setRes(r, limit, init, isLive)
					}
				})
			}
		}
	}
	initNames := map[hubapi.WorkloadKey]map[string]bool{}
	instantQ("init", q.InitContainers(cfg.InventoryLookback), func(r VectorSample) {
		// Look up only: an init container alone does not make a workload part of the inventory.
		k := hubapi.WorkloadKey{Cluster: cluster, Namespace: r.Labels["namespace"], Kind: r.Labels["workload_kind"], Name: r.Labels["workload"]}
		if initNames[k] == nil {
			initNames[k] = map[string]bool{}
		}
		initNames[k][r.Labels["container"]] = true
	})
	images := containerImages{}
	instantQ("images", q.ContainerImages(cfg.InventoryLookback), func(r VectorSample) { images.add(cluster, r.Labels) })
	type hpaKey struct{ ns, name string }
	hpaTargets := map[hpaKey]Labels{}
	hpaMetrics := map[hpaKey][]string{}
	hpaUtil := map[hpaKey]map[string]float64{}
	hpaMin, hpaMax, hpaPeak := map[hpaKey]float64{}, map[hpaKey]float64{}, map[hpaKey]float64{}
	instantQ("hpa", q.HPAInfo(cfg.InventoryLookback), func(r VectorSample) {
		hpaTargets[hpaKey{r.Labels["namespace"], r.Labels["horizontalpodautoscaler"]}] = r.Labels
	})
	instantQ("hpaMetrics", q.HPAMetrics(cfg.InventoryLookback), func(r VectorSample) {
		k := hpaKey{r.Labels["namespace"], r.Labels["horizontalpodautoscaler"]}
		name, typ := r.Labels["metric_name"], r.Labels["metric_target_type"]
		hpaMetrics[k] = append(hpaMetrics[k], name+"/"+typ)
		if typ == "utilization" {
			if hpaUtil[k] == nil {
				hpaUtil[k] = map[string]float64{}
			}
			hpaUtil[k][name] = r.Sample.V
		}
	})
	hpaNum := func(name, query string, dst map[hpaKey]float64) {
		instantQ(name, query, func(r VectorSample) {
			dst[hpaKey{r.Labels["namespace"], r.Labels["horizontalpodautoscaler"]}] = r.Sample.V
		})
	}
	hpaNum("hpaMin", q.HPAReplicaBounds("min", cfg.InventoryLookback), hpaMin)
	hpaNum("hpaMax", q.HPAReplicaBounds("max", cfg.InventoryLookback), hpaMax)
	hpaNum("hpaPeak", q.HPAPeakReplicas(window), hpaPeak)
	var cpuRate, memRate, nodes float64
	cpuAvg, cpuCount := q.Pricing("ro_cpu_core_hourly_cost_usd", time.Hour)
	memAvg, _ := q.Pricing("ro_memory_gib_hourly_cost_usd", time.Hour)
	// Pricing series are in the hub's own store.
	instantOn(s.Q, "pricingCPU", cpuAvg, func(r VectorSample) { cpuRate = r.Sample.V })
	instantOn(s.Q, "pricingMemory", memAvg, func(r VectorSample) { memRate = r.Sample.V })
	instantOn(s.Q, "pricingNodes", cpuCount, func(r VectorSample) { nodes = r.Sample.V })

	if err := g.Wait(); err != nil {
		return nil, err
	}
	// Live values first so they win.
	for _, f := range inv {
		f()
	}
	for k, names := range initNames {
		if w := d.Workloads[k]; w != nil {
			for n := range names {
				if c := w.Containers[n]; c != nil {
					c.Init = true
				}
			}
		}
	}
	images.apply(d)
	for k, l := range hpaTargets {
		wk := hubapi.WorkloadKey{Cluster: cluster, Namespace: k.ns, Kind: l["scaletargetref_kind"], Name: l["scaletargetref_name"]}
		if w := d.Workloads[wk]; w != nil {
			ms := hpaMetrics[k]
			sort.Strings(ms)
			w.HPA = &HPA{Name: k.name, Metrics: ms, Targets: hpaUtil[k], MinReplicas: hpaMin[k], MaxReplicas: hpaMax[k], PeakReplicas: hpaPeak[k]}
		}
	}
	d.Pricing = Pricing{CPUCoreHour: cfg.DefaultCPUCoreHourUSD, MemoryGiBHour: cfg.DefaultMemoryGiBHourUSD, Source: PricingSourceDefault}
	if cpuRate > 0 && memRate > 0 {
		d.Pricing = Pricing{CPUCoreHour: cpuRate, MemoryGiBHour: memRate, Source: PricingSourceMetrics, Nodes: int(nodes)}
	}
	return d, nil
}

// PricingAt returns the cluster's average pricing rates at t (lookback 1 h). ok is false when the
// cluster has no pricing series then.
func (s *Source) PricingAt(ctx context.Context, cluster string, t time.Time) (Pricing, bool, error) {
	q := Queries{Cluster: cluster, RateWindow: s.Cfg.RateWindow}
	cpuAvg, cpuCount := q.Pricing("ro_cpu_core_hourly_cost_usd", time.Hour)
	memAvg, _ := q.Pricing("ro_memory_gib_hourly_cost_usd", time.Hour)
	var v [3]float64
	for i, query := range []string{cpuAvg, memAvg, cpuCount} {
		res, err := s.Q.Instant(ctx, query, t)
		if err != nil {
			return Pricing{}, false, err
		}
		if len(res) > 0 {
			v[i] = res[0].Sample.V
		}
	}
	if v[0] <= 0 || v[1] <= 0 {
		return Pricing{}, false, nil
	}
	return Pricing{CPUCoreHour: v[0], MemoryGiBHour: v[1], Source: PricingSourceMetrics, Nodes: int(v[2])}, true, nil
}

// Data derives the recommender input for profile p from the raw series.
func (w *RawWorkload) Data(p Profile, end time.Time, cfg Config) WorkloadData {
	from := end.Add(-p.Window)
	peakFrom := end.Add(-p.PeakWindow())
	qs := Quantiles(p)
	out := WorkloadData{Key: w.Key, HPA: w.HPA}
	mainSamples := 0
	for _, c := range w.Containers {
		if !c.Init {
			mainSamples = max(mainSamples, len(after(c.CPU, from)))
		}
	}
	names := make([]string, 0, len(w.Containers))
	for n := range w.Containers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := w.Containers[n]
		cpu := after(c.CPU, from)
		mem := after(c.Memory, from)
		// Init containers count only when they run alongside the app (native sidecars); classic
		// init containers run for seconds and are not rightsized.
		if c.Init && float64(len(cpu)) < 0.5*float64(mainSamples) {
			continue
		}
		out.Containers = append(out.Containers, ContainerData{
			Name:          c.Name,
			Init:          c.Init,
			CPU:           StatsFromSamples(cpu, qs),
			Memory:        StatsFromSamples(mem, qs),
			MemoryPeak:    MaxSince(mem, peakFrom),
			ThrottleRatio: mean(c.Throttle),
			OOMKilled:     c.OOMKilled,
			Restarts:      c.Restarts,
			Current:       c.Current,
			Used24hCPU:    c.Used24hCPU,
			Used24hMemory: c.Used24hMemory,
		})
	}
	out.AverageReplicas = averageReplicas(after(w.Pods, from), end, cfg.Step)
	return out
}

// averageReplicas is the mean pod count from the first sample to end; missing steps count as 0.
func averageReplicas(pods []Sample, end time.Time, step time.Duration) float64 {
	if len(pods) == 0 {
		return 0
	}
	first := pods[0].T
	sum := 0.0
	for _, p := range pods {
		sum += p.V
		if p.T.Before(first) {
			first = p.T
		}
	}
	steps := float64(end.Sub(first)/step) + 1
	return sum / steps
}

func after(s []Sample, from time.Time) []Sample {
	i := sort.Search(len(s), func(i int) bool { return s[i].T.After(from) })
	return s[i:]
}

func mean(s []Sample) float64 {
	if len(s) == 0 {
		return 0
	}
	sum := 0.0
	for _, p := range s {
		sum += p.V
	}
	return sum / float64(len(s))
}
