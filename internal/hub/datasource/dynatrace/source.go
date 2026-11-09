package dynatrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// ErrNoPromQL is returned by the PromQL methods: a Dynatrace datasource is read with DQL through
// its own methods (LoadWorkloads, the readiness and node inventory methods).
var ErrNoPromQL = errors.New("a dynatrace datasource is not queried with PromQL")

// maxBins is the most buckets one timeseries query asks for (the query API allows 1,500).
const maxBins = 1440

// DefaultConcurrency is the number of queries a Source runs at once.
const DefaultConcurrency = 2

// Source reads one Dynatrace environment. It implements recommender.Querier (PromQL is
// refused with ErrNoPromQL) and recommender.WorkloadSource.
type Source struct {
	Client *Client
	// Concurrency bounds parallel queries; 0 = DefaultConcurrency.
	Concurrency int
	Log         *slog.Logger

	once sync.Once
	sem  chan struct{}
}

// New returns the source of an environment URL (or the ActiveGate URL, when set) with an
// authenticated HTTP client.
func New(envURL, activeGateURL string, hc *http.Client) *Source {
	base := PlatformURL(envURL)
	if strings.TrimSpace(activeGateURL) != "" {
		base = strings.TrimRight(strings.TrimSpace(activeGateURL), "/")
	}
	return &Source{Client: &Client{Base: base, HTTP: hc}}
}

// Instant implements recommender.Querier: it always fails with ErrNoPromQL.
func (s *Source) Instant(context.Context, string, time.Time) ([]recommender.VectorSample, error) {
	return nil, ErrNoPromQL
}

// Range implements recommender.Querier: it always fails with ErrNoPromQL.
func (s *Source) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]recommender.Series, error) {
	return nil, ErrNoPromQL
}

func (s *Source) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// run executes one query, at most Concurrency at a time.
func (s *Source) run(ctx context.Context, dql string) (Result, error) {
	s.once.Do(func() {
		n := s.Concurrency
		if n <= 0 {
			n = DefaultConcurrency
		}
		s.sem = make(chan struct{}, n)
	})
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	defer func() { <-s.sem }()
	res, err := s.Client.Query(ctx, dql)
	if err != nil {
		return res, err
	}
	for _, n := range res.Notes {
		s.log().WarnContext(ctx, "dynatrace query notification", "note", n)
	}
	return res, nil
}

// ---- DQL building ----

// dqlString quotes s as a DQL string literal.
func dqlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// dqlTime renders an absolute timestamp.
func dqlTime(t time.Time) string { return dqlString(t.UTC().Format(time.RFC3339)) }

// dqlDuration renders a duration literal (whole seconds at least).
func dqlDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", max(1, d/time.Second))
}

func dims(d []string) string { return "{" + strings.Join(d, ", ") + "}" }

// filter is the timeseries filter of a cluster; "" = the whole environment.
func filter(cluster string) string {
	if cluster == "" {
		return ""
	}
	return ", filter: " + DimCluster + " == " + dqlString(cluster)
}

// window is the timeframe of a query.
func window(from, to time.Time) string { return ", from: " + dqlTime(from) + ", to: " + dqlTime(to) }

// binInterval is the smallest multiple of step that covers d in at most maxBins buckets.
func binInterval(d, step time.Duration) time.Duration {
	if step <= 0 {
		step = time.Minute
	}
	n := (d + step*maxBins - 1) / (step * maxBins)
	return max(1, n) * step
}

// chunk splits [start, end) into parts of at most maxBins steps.
func chunk(start, end time.Time, step time.Duration) [][2]time.Time {
	var out [][2]time.Time
	span := step * maxBins
	for from := start; from.Before(end); from = from.Add(span) {
		to := from.Add(span)
		if to.After(end) {
			to = end
		}
		out = append(out, [2]time.Time{from, to})
	}
	return out
}

// UsageQuery is the per-step usage of every workload container: agg (avg for CPU, max for
// memory) per pod first, then the max and the sum across pods. Per pod first keeps the sum
// exact when a bucket holds several samples.
func UsageQuery(key, agg, cluster string, from, to time.Time, step time.Duration) string {
	return fmt.Sprintf("timeseries v = %s(%s), by: %s%s%s, interval: %s\n| summarize {vmax = max(v[]), vsum = sum(v[])}, by: %s",
		agg, key, dims(containerDims), filter(cluster), window(from, to), dqlDuration(step), dims(append([]string{"timeframe", "interval"}, workloadContainerDims...)))
}

// PodsQuery is the number of pods per workload that had a memory sample in each step.
func PodsQuery(cluster string, from, to time.Time, step time.Duration) string {
	return fmt.Sprintf("timeseries v = max(%s), by: %s%s%s, interval: %s\n| fieldsAdd v = if(isNotNull(v[]), 1.0)\n| summarize pods = sum(v[]), by: %s",
		KeyMemoryWorkingSet, dims([]string{DimNamespace, DimKind, DimWorkload, DimPod}), filter(cluster), window(from, to), dqlDuration(step),
		dims([]string{"timeframe", "interval", DimNamespace, DimKind, DimWorkload}))
}

// MaxAcrossPodsQuery is the max across pods of a per-pod average (the throttling ratio).
func MaxAcrossPodsQuery(key, cluster string, from, to time.Time, step time.Duration) string {
	return fmt.Sprintf("timeseries v = avg(%s), by: %s%s%s, interval: %s\n| summarize vmax = max(v[]), by: %s",
		key, dims(containerDims), filter(cluster), window(from, to), dqlDuration(step), dims(append([]string{"timeframe", "interval"}, workloadContainerDims...)))
}

// TotalQuery sums a count metric over the window per workload container (restarts, OOM kills).
func TotalQuery(key, cluster string, from, to time.Time) string {
	return fmt.Sprintf("timeseries v = sum(%s, scalar: true), by: %s%s%s\n| summarize v = sum(v), by: %s",
		key, dims(containerDims), filter(cluster), window(from, to), dims(workloadContainerDims))
}

// ResourceQuery is a request or limit per workload container and step (max across pods).
func ResourceQuery(key, cluster string, from, to time.Time, step time.Duration) string {
	return fmt.Sprintf("timeseries v = max(%s), by: %s%s%s, interval: %s", key, dims(workloadContainerDims), filter(cluster), window(from, to), dqlDuration(step))
}

// ---- workloads (recommender) ----

// kerfKind maps a Dynatrace k8s.workload.kind to a Kerf workload kind ("" = not rightsized).
func kerfKind(k string) string {
	switch strings.ToLower(k) {
	case "deployment":
		return hubapi.KindDeployment
	case "statefulset":
		return hubapi.KindStatefulSet
	case "daemonset":
		return hubapi.KindDaemonSet
	case "cronjob":
		return hubapi.KindCronJob
	}
	return ""
}

// workloadKey returns the Kerf workload of a record; ok is false for kinds Kerf does not rightsize.
func workloadKey(cluster string, rec map[string]json.RawMessage) (hubapi.WorkloadKey, bool) {
	k := hubapi.WorkloadKey{Cluster: cluster, Namespace: str(rec, DimNamespace), Kind: kerfKind(str(rec, DimKind)), Name: str(rec, DimWorkload)}
	return k, k.Namespace != "" && k.Name != "" && k.Kind != ""
}

// unitScale returns the conversion factor of key in res, logging a unit it does not know.
func (s *Source) unitScale(ctx context.Context, res Result, key string, kind unitKind) float64 {
	unit := res.Units[key]
	f, known := scale(kind, unit)
	if !known && unit != "" {
		s.log().WarnContext(ctx, "dynatrace metric has an unknown unit; using the default", "metric", key, "unit", unit)
	}
	return f
}

// LoadWorkloads implements recommender.WorkloadSource: the usage, pods, throttling, restarts,
// OOM kills and current requests/limits of every workload container of a cluster, in at most
// a few dozen queries (one batch per recompute).
func (s *Source) LoadWorkloads(ctx context.Context, req recommender.WorkloadRequest) (map[hubapi.WorkloadKey]*recommender.RawWorkload, error) {
	cluster := req.SourceCluster()
	step := req.Step
	if step <= 0 {
		step = 5 * time.Minute
	}
	out := map[hubapi.WorkloadKey]*recommender.RawWorkload{}
	var mu sync.Mutex
	workload := func(rec map[string]json.RawMessage) *recommender.RawWorkload {
		k, ok := workloadKey(req.Cluster, rec)
		if !ok {
			return nil
		}
		w := out[k]
		if w == nil {
			w = &recommender.RawWorkload{Key: k, Containers: map[string]*recommender.RawContainer{}, ThrottleDerived: true, HPAUnknown: true}
			out[k] = w
		}
		return w
	}
	container := func(rec map[string]json.RawMessage) *recommender.RawContainer {
		w := workload(rec)
		name := str(rec, DimContainer)
		if w == nil || name == "" {
			return nil
		}
		c := w.Containers[name]
		if c == nil {
			c = &recommender.RawContainer{Name: name}
			w.Containers[name] = c
		}
		return c
	}
	g, gctx := errgroup.WithContext(ctx)
	// each runs one query and applies its records under mu
	each := func(name, dql string, apply func(res Result, rec map[string]json.RawMessage) error) {
		g.Go(func() error {
			res, err := s.run(gctx, dql)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, rec := range res.Records {
				if err := apply(res, rec); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			return nil
		})
	}
	day := req.End.Add(-24 * time.Hour)
	type usageAcc struct {
		max, sum []recommender.Sample
	}
	cpu, mem := map[*recommender.RawContainer]*usageAcc{}, map[*recommender.RawContainer]*usageAcc{}
	usage := func(name, key, agg string, kind unitKind, acc map[*recommender.RawContainer]*usageAcc) {
		for _, c := range chunk(req.Start, req.End, step) {
			each(name, UsageQuery(key, agg, cluster, c[0], c[1], step), func(res Result, rec map[string]json.RawMessage) error {
				ct := container(rec)
				if ct == nil {
					return nil
				}
				f := s.unitScale(gctx, res, key, kind)
				mx, err := series(rec, "vmax", f)
				if err != nil {
					return err
				}
				sm, err := series(rec, "vsum", f)
				if err != nil {
					return err
				}
				a := acc[ct]
				if a == nil {
					a = &usageAcc{}
					acc[ct] = a
				}
				a.max, a.sum = append(a.max, mx...), append(a.sum, sm...)
				return nil
			})
		}
	}
	usage("cpu", KeyCPUUsage, "avg", unitCPU, cpu)
	usage("memory", KeyMemoryWorkingSet, "max", unitBytes, mem)
	pods := map[*recommender.RawWorkload][]recommender.Sample{}
	for _, c := range chunk(req.Start, req.End, step) {
		each("pods", PodsQuery(cluster, c[0], c[1], step), func(_ Result, rec map[string]json.RawMessage) error {
			w := workload(rec)
			if w == nil {
				return nil
			}
			p, err := series(rec, "pods", 1)
			pods[w] = append(pods[w], p...)
			return err
		})
	}
	risk := req.End.Add(-req.RiskWindow)
	if req.RiskWindow > 0 {
		iv := binInterval(req.RiskWindow, time.Hour)
		each("throttle", MaxAcrossPodsQuery(KeyThrottlingRatio, cluster, risk, req.End, iv), func(res Result, rec map[string]json.RawMessage) error {
			c := container(rec)
			if c == nil {
				return nil
			}
			sm, err := series(rec, "vmax", s.unitScale(gctx, res, KeyThrottlingRatio, unitRatio))
			c.Throttle = append(c.Throttle, sm...)
			return err
		})
		each("oom", TotalQuery(KeyOOMKills, cluster, risk, req.End), func(_ Result, rec map[string]json.RawMessage) error {
			if c := container(rec); c != nil {
				if v, ok := num(rec, "v"); ok && v > 0 {
					c.OOMKilled = true
				}
			}
			return nil
		})
		each("restarts", TotalQuery(KeyRestarts, cluster, risk, req.End), func(_ Result, rec map[string]json.RawMessage) error {
			if c := container(rec); c != nil {
				if v, ok := num(rec, "v"); ok {
					c.Restarts += v
				}
			}
			return nil
		})
	}
	lookback := max(req.InventoryLookback, step)
	invStep := binInterval(lookback, step)
	for _, r := range []struct {
		key  string
		kind unitKind
		set  func(c *recommender.Current, v float64)
	}{
		{KeyRequestsCPU, unitCPU, func(c *recommender.Current, v float64) { c.CPURequest = v }},
		{KeyRequestsMemory, unitBytes, func(c *recommender.Current, v float64) { c.MemoryRequest = v }},
		{KeyLimitsCPU, unitCPU, func(c *recommender.Current, v float64) { c.CPULimit = v }},
		{KeyLimitsMemory, unitBytes, func(c *recommender.Current, v float64) { c.MemoryLimit = v }},
	} {
		each(r.key, ResourceQuery(r.key, cluster, req.End.Add(-lookback), req.End, invStep), func(res Result, rec map[string]json.RawMessage) error {
			c := container(rec)
			if c == nil {
				return nil
			}
			sm, err := series(rec, "v", s.unitScale(gctx, res, r.key, r.kind))
			if err != nil || len(sm) == 0 {
				return err
			}
			last := sm[len(sm)-1]
			r.set(&c.Current, last.V)
			w := workload(rec)
			w.HasRequests = true
			// live: a value in the last bucket (the workload has pods now)
			w.Live = w.Live || !last.T.Before(req.End)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for c, a := range cpu {
		c.CPU = mergeMax(a.max)
		c.Used24hCPU = meanSince(mergeMax(a.sum), day)
	}
	for c, a := range mem {
		c.Memory = mergeMax(a.max)
		c.Used24hMemory = meanSince(mergeMax(a.sum), day)
	}
	for w, p := range pods {
		w.Pods = mergeMax(p)
	}
	for _, w := range out {
		for _, c := range w.Containers {
			c.Throttle = mergeMax(c.Throttle)
		}
	}
	return out, nil
}

// mergeMax sorts samples by time and keeps the highest value per timestamp (chunks never
// overlap, but a workload kind can appear under two spellings).
func mergeMax(s []recommender.Sample) []recommender.Sample {
	if len(s) == 0 {
		return nil
	}
	sort.SliceStable(s, func(i, j int) bool { return s[i].T.Before(s[j].T) })
	out := s[:1]
	for _, p := range s[1:] {
		if last := &out[len(out)-1]; p.T.Equal(last.T) {
			last.V = max(last.V, p.V)
			continue
		}
		out = append(out, p)
	}
	return out
}

func meanSince(s []recommender.Sample, from time.Time) float64 {
	sum, n := 0.0, 0
	for _, p := range s {
		if p.T.After(from) {
			sum += p.V
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
