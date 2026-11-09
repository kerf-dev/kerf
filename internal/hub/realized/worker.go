package realized

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Worker materialises realized savings and the node cost trend, hour by hour, on the hub leader.
// Each cluster keeps its own progress (realized_savings_state): a first run backfills from the
// cluster's earliest stored change, later runs add the hours completed since, and events that
// arrive late (buffered by the agent) rewind the computation to the hour they happened in.
type Worker struct {
	DB *DB
	// Local is the hub's own metrics store, where the pricing service writes its series.
	Local recommender.Querier
	// MetricsFor returns the querier and binding of a cluster's metrics (kube-state-metrics).
	MetricsFor func(ctx context.Context, cluster string) (recommender.Querier, *datasource.Matcher, error)
	// DefaultRates price hours for which no rates were stored (the recommender's defaults).
	DefaultRates Rates
	Log          *slog.Logger
	Now          func() time.Time
	// Interval between runs (default 10m).
	Interval time.Duration
	// NodeCostBackfill is how far back the node cost trend starts on a first run (default 30 days).
	NodeCostBackfill time.Duration
	// Chunk is the span computed per query and transaction (default 3 days); MaxPerRun bounds the
	// work of one run per cluster (default 31 days), so a long backfill spreads over several runs.
	Chunk     time.Duration
	MaxPerRun time.Duration
	// StartDelay is the pause before the first run after taking the lead (default 30s).
	StartDelay time.Duration
	// Settle is how long after an hour ends it is computed (default 10m): metrics arrive late.
	Settle time.Duration
	// Collector, when set, exports the totals after each run.
	Collector *Collector
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func or[T comparable](v, d T) T {
	var zero T
	if v == zero {
		return d
	}
	return v
}

func (w *Worker) log() *slog.Logger { return or(w.Log, slog.Default()) }

// Run computes every Interval, and as soon as an hour becomes complete, until ctx ends. The first
// run waits StartDelay, so a new leader's takeover and first recommender run go first.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTimer(or(w.StartDelay, 30*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.log().WarnContext(ctx, "realized savings", "err", err)
		}
		t.Reset(w.nextDelay(w.now()))
	}
}

// nextDelay is the wait before the next run: Interval, but no later than the moment the next hour
// becomes complete (its end plus Settle). A fixed interval alone would let computedThrough trail
// the clock by up to an hour plus Settle plus Interval; aligned, it trails by at most an hour plus
// Settle (and the run's own time).
func (w *Worker) nextDelay(now time.Time) time.Duration {
	d := or(w.Interval, 10*time.Minute)
	settle := or(w.Settle, 10*time.Minute)
	complete := now.Add(-settle).Truncate(time.Hour).Add(time.Hour + settle)
	if until := complete.Sub(now); until < d {
		d = until
	}
	return d
}

// RunOnce brings every live cluster up to the last complete hour (within MaxPerRun) and refreshes
// the exported totals. A failing cluster does not stop the others.
func (w *Worker) RunOnce(ctx context.Context) error {
	clusters, err := w.DB.liveClusters(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, c := range clusters {
		if err := w.cluster(ctx, c); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.log().WarnContext(ctx, "realized savings: cluster", "cluster", c, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("cluster %s: %w", c, err)
			}
		}
	}
	if w.Collector != nil {
		if err := w.Collector.Refresh(ctx, w.DB); err != nil {
			return err
		}
	}
	return firstErr
}

func (w *Worker) cluster(ctx context.Context, cluster string) error {
	now := w.now()
	// Hours that ended at least Settle ago are complete (remote write and scrapes lag a little).
	end := now.Add(-or(w.Settle, 10*time.Minute)).Truncate(time.Hour)
	// Events received in the last two minutes may still be committing; the next run sees them.
	cutoff := now.Add(-2 * time.Minute)
	st, found, err := w.DB.state(ctx, cluster)
	if err != nil {
		return err
	}

	// Realized savings.
	start := st.ComputedThrough
	if start == nil {
		if start, err = w.DB.firstChange(ctx, cluster); err != nil {
			return err
		}
		if start != nil {
			t := start.UTC().Truncate(time.Hour)
			start = &t
		}
	} else if found {
		late, err := w.DB.lateChange(ctx, cluster, st.EventsThrough, cutoff)
		if err != nil {
			return err
		}
		if late != nil && late.Before(*start) {
			t := late.UTC().Truncate(time.Hour)
			w.log().InfoContext(ctx, "realized savings: late events, recomputing", "cluster", cluster, "from", t)
			start = &t
		}
	}
	st.EventsThrough = cutoff
	wrote := false
	if start != nil {
		var changes map[WorkloadKey][]Change
		budget := start.Add(or(w.MaxPerRun, 31*24*time.Hour))
		for from := *start; from.Before(end) && from.Before(budget); {
			to := minTime(from.Add(or(w.Chunk, 72*time.Hour)), end)
			if changes == nil {
				// All of the cluster's changes up to the end: the ledger needs the history before
				// the chunk too (releases are cumulative).
				if changes, err = w.DB.changes(ctx, cluster, end); err != nil {
					return err
				}
			}
			rows, err := w.computeRealized(ctx, cluster, changes, from, to)
			if err != nil {
				return err
			}
			t := to
			st.ComputedThrough = &t
			if err := w.DB.replaceHours(ctx, cluster, from, to, rows, st); err != nil {
				return err
			}
			wrote = true
			from = to
		}
		if st.ComputedThrough != nil && st.ComputedThrough.After(*start) {
			w.log().InfoContext(ctx, "realized savings computed", "cluster", cluster, "from", *start, "through", *st.ComputedThrough)
		}
	}

	// Node cost trend.
	nstart := end.Add(-or(w.NodeCostBackfill, 30*24*time.Hour))
	if st.NodeCostThrough != nil {
		nstart = *st.NodeCostThrough
	}
	budget := nstart.Add(or(w.MaxPerRun, 31*24*time.Hour))
	for from := nstart; from.Before(end) && from.Before(budget); {
		to := minTime(from.Add(or(w.Chunk, 72*time.Hour)), end)
		rows, err := w.computeNodeCost(ctx, cluster, from, to)
		if err != nil {
			return err
		}
		t := to
		st.NodeCostThrough = &t
		if err := w.DB.replaceNodeCost(ctx, cluster, from, to, rows, st); err != nil {
			return err
		}
		wrote = true
		from = to
	}
	if !wrote {
		// Nothing to compute: still record the event watermark.
		tx, err := w.DB.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := w.DB.saveState(ctx, tx, cluster, st); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// computeRealized prices the hours [from, to) of every workload with a change before `to`.
func (w *Worker) computeRealized(ctx context.Context, cluster string, changes map[WorkloadKey][]Change, from, to time.Time) ([]workloadHour, error) {
	ledgers := map[WorkloadKey][]Step{}
	for k, cs := range changes {
		steps, skipped := Ledger(cs)
		if skipped > 0 {
			w.log().DebugContext(ctx, "realized savings: container changes without both requests", "cluster", cluster,
				"workload", k.Namespace+"/"+k.Kind+"/"+k.Name, "skipped", skipped)
		}
		if len(steps) > 0 && steps[0].Time.Before(to) {
			ledgers[k] = steps
		}
	}
	if len(ledgers) == 0 {
		return nil, nil
	}
	replicas, covered, err := w.metricReplicas(ctx, cluster, from, to)
	if err != nil {
		return nil, err
	}
	snaps, err := w.DB.rateSnapshots(ctx, cluster, from, to)
	if err != nil {
		return nil, err
	}
	var recReps map[WorkloadKey][]replicaPoint
	needRec := false
	for t := from; t.Before(to); t = t.Add(time.Hour) {
		if !covered[t] {
			needRec = true
			break
		}
	}
	if needRec {
		if recReps, err = w.DB.recommendationReplicas(ctx, cluster, from, to); err != nil {
			return nil, err
		}
	}
	keys := make([]WorkloadKey, 0, len(ledgers))
	for k := range ledgers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	var out []workloadHour
	for _, k := range keys {
		for _, h := range Hours(ledgers[k], from, to, func(hour time.Time) (HourInput, bool) {
			in := HourInput{Rates: ratesAt(snaps, hour.Add(time.Hour), w.DefaultRates)}
			switch {
			case covered[hour]:
				// The cluster had data in that hour: a workload without pods ran none.
				in.Replicas, in.ReplicaSource = replicas[k][hour], ReplicasMetrics
			default:
				if r, ok := replicasAt(recReps[k], hour.Add(time.Hour)); ok {
					in.Replicas, in.ReplicaSource = r, ReplicasRecommendation
				} else {
					in.ReplicaSource = ReplicasNone
				}
			}
			return in, true
		}) {
			out = append(out, workloadHour{Key: k, Hour: h})
		}
	}
	return out, nil
}

func keyLess(a, b WorkloadKey) bool {
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Name < b.Name
}

// ratesAt returns the rates of the latest snapshot at or before t, else the earliest after it,
// else the defaults.
func ratesAt(snaps []rateSnapshot, t time.Time, def Rates) Rates {
	if len(snaps) == 0 {
		def.Source = "default"
		return def
	}
	r := snaps[0].Rates
	for _, s := range snaps {
		if s.AsOf.After(t) {
			break
		}
		r = s.Rates
	}
	return r
}

// replicasAt returns the replicas of the latest recommendation generated at or before t.
func replicasAt(ps []replicaPoint, t time.Time) (float64, bool) {
	var r float64
	ok := false
	for _, p := range ps {
		if p.At.After(t) {
			break
		}
		r, ok = p.Replicas, true
	}
	return r, ok
}

// hourlyReplicas is a datasource that is not queried with PromQL (Dynatrace) and reads the
// average replicas per workload and hour itself.
type hourlyReplicas interface {
	HourlyReplicas(ctx context.Context, registered, cluster string, from, to time.Time) (map[hubapi.WorkloadKey]map[time.Time]float64, map[time.Time]bool, error)
}

// ReplicasQuery is the average number of scheduled pods per workload over the hour ending at
// each evaluation time (kube-state-metrics requests with a node; one sample per 5 minutes, a
// missing sample counts as zero pods).
func ReplicasQuery(cluster string, binding *datasource.Matcher) string {
	m := datasource.DefaultMatcher(cluster)
	if binding != nil {
		m = *binding
	}
	q := recommender.Queries{Cluster: cluster, Binding: binding}
	pods := fmt.Sprintf("max by (cluster, namespace, pod) (%s)",
		m.Relabel("kube_pod_container_resource_requests"+m.Sel(`resource=~"cpu|memory"`, `node!=""`)))
	perWorkload := fmt.Sprintf("count by (%s) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		recommender.WorkloadLabels, pods, q.PodWorkload(nil, nil))
	return fmt.Sprintf("sum_over_time((%s)[1h:5m]) / scalar(count_over_time(vector(1)[1h:5m]))", perWorkload)
}

// NodeCostQueries are the hourly averages of the node cost trend. cost and nodes run on the hub's
// own metrics store (pricing series), cpu and memory on the cluster's datasource.
func NodeCostQueries(cluster string, binding *datasource.Matcher) (cost, nodes, cpu, memory string) {
	local := datasource.DefaultMatcher(cluster)
	perNode := fmt.Sprintf("max by (cluster, node) (kerf_node_hourly_cost_usd%s)", local.Sel())
	cost = fmt.Sprintf("avg_over_time((sum by (cluster) (%s))[1h:5m])", perNode)
	nodes = fmt.Sprintf("avg_over_time((count by (cluster) (%s))[1h:5m])", perNode)
	m := local
	if binding != nil {
		m = *binding
	}
	ratio := func(res string) string {
		return fmt.Sprintf("avg_over_time((\n  sum by (cluster) (%s)\n  / sum by (cluster) (%s)\n)[1h:5m])",
			m.Relabel("kube_pod_container_resource_requests"+m.Sel(fmt.Sprintf("resource=%q", res), `node!=""`)),
			m.Relabel("kube_node_status_allocatable"+m.Sel(fmt.Sprintf("resource=%q", res))))
	}
	return cost, nodes, ratio("cpu"), ratio("memory")
}

// metricReplicas returns the average replicas per workload and hour of [from, to), and the hours
// for which the cluster had any data at all.
func (w *Worker) metricReplicas(ctx context.Context, cluster string, from, to time.Time) (map[WorkloadKey]map[time.Time]float64, map[time.Time]bool, error) {
	prom, binding, err := w.MetricsFor(ctx, cluster)
	if err != nil {
		return nil, nil, err
	}
	if hr, ok := prom.(hourlyReplicas); ok {
		src := cluster
		if binding != nil && binding.Value != "" {
			src = binding.Value
		}
		byKey, covered, err := hr.HourlyReplicas(ctx, cluster, src, from, to)
		if err != nil {
			return nil, nil, err
		}
		out := make(map[WorkloadKey]map[time.Time]float64, len(byKey))
		for k, v := range byKey {
			out[WorkloadKey{Namespace: k.Namespace, Kind: k.Kind, Name: k.Name}] = v
		}
		return out, covered, nil
	}
	// The sample at T is the hour [T-1h, T).
	res, err := prom.Range(ctx, ReplicasQuery(cluster, binding), from.Add(time.Hour), to, time.Hour)
	if err != nil {
		return nil, nil, err
	}
	out := map[WorkloadKey]map[time.Time]float64{}
	covered := map[time.Time]bool{}
	for _, s := range res {
		k := WorkloadKey{Namespace: s.Labels["namespace"], Kind: s.Labels["workload_kind"], Name: s.Labels["workload"]}
		for _, p := range s.Samples {
			if math.IsNaN(p.V) {
				continue
			}
			h := p.T.UTC().Add(-time.Hour).Truncate(time.Hour)
			if out[k] == nil {
				out[k] = map[time.Time]float64{}
			}
			out[k][h] = p.V
			covered[h] = true
		}
	}
	return out, covered, nil
}

// computeNodeCost reads the node cost trend of [from, to).
func (w *Worker) computeNodeCost(ctx context.Context, cluster string, from, to time.Time) ([]NodeCostHour, error) {
	prom, binding, err := w.MetricsFor(ctx, cluster)
	if err != nil {
		return nil, err
	}
	if _, ok := prom.(hourlyReplicas); ok {
		// not a PromQL datasource (Dynatrace): the requested ratios are left out
		prom = nil
	}
	costQ, nodesQ, cpuQ, memQ := NodeCostQueries(cluster, binding)
	byHour := map[time.Time]*NodeCostHour{}
	read := func(q recommender.Querier, query string, set func(*NodeCostHour, float64)) error {
		if q == nil {
			return nil
		}
		res, err := q.Range(ctx, query, from.Add(time.Hour), to, time.Hour)
		if err != nil {
			return err
		}
		for _, s := range res {
			for _, p := range s.Samples {
				if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
					continue
				}
				h := p.T.UTC().Add(-time.Hour).Truncate(time.Hour)
				if byHour[h] == nil {
					byHour[h] = &NodeCostHour{Hour: h}
				}
				set(byHour[h], p.V)
			}
		}
		return nil
	}
	ptr := func(v float64) *float64 { return &v }
	for _, x := range []struct {
		q     recommender.Querier
		query string
		set   func(*NodeCostHour, float64)
	}{
		{w.Local, costQ, func(n *NodeCostHour, v float64) { n.NodeCostUSD = ptr(v) }},
		{w.Local, nodesQ, func(n *NodeCostHour, v float64) { n.Nodes = ptr(v) }},
		{prom, cpuQ, func(n *NodeCostHour, v float64) { n.CPURequestedRatio = ptr(v) }},
		{prom, memQ, func(n *NodeCostHour, v float64) { n.MemoryRequestedRatio = ptr(v) }},
	} {
		if err := read(x.q, x.query, x.set); err != nil {
			return nil, err
		}
	}
	out := make([]NodeCostHour, 0, len(byHour))
	for _, n := range byHour {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour.Before(out[j].Hour) })
	return out, nil
}
