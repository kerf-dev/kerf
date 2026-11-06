package realized

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// newTestDB creates a fresh migrated database (dropped at the end) when KERF_TEST_DATABASE_URL is
// set to a superuser URL; otherwise the test is skipped.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	base := os.Getenv("KERF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KERF_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kerf_realized_test_" + hex.EncodeToString(b)
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
		admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Migrate(ctx, os.DirFS("../../../db/migrations")); err != nil {
		t.Fatal(err)
	}
	return &DB{Pool: st.Pool}
}

// fakeProm answers the worker's queries: replicas per workload (1 each, except during gap) and
// constant node cost series.
type fakeProm struct {
	mu        sync.Mutex
	workloads []WorkloadKey
	gapFrom   time.Time
	gapTo     time.Time
	queries   []string
}

func (f *fakeProm) Instant(context.Context, string, time.Time) ([]recommender.VectorSample, error) {
	return nil, nil
}

func (f *fakeProm) Range(_ context.Context, q string, start, end time.Time, step time.Duration) ([]recommender.Series, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	samples := func(v float64) []recommender.Sample {
		var out []recommender.Sample
		for t := start; !t.After(end); t = t.Add(step) {
			hour := t.Add(-time.Hour)
			if !hour.Before(f.gapFrom) && hour.Before(f.gapTo) {
				continue
			}
			out = append(out, recommender.Sample{T: t, V: v})
		}
		return out
	}
	switch {
	case strings.Contains(q, "count by (cluster, namespace, workload_kind, workload)"):
		var out []recommender.Series
		for _, k := range f.workloads {
			out = append(out, recommender.Series{
				Labels:  recommender.Labels{"cluster": "c1", "namespace": k.Namespace, "workload_kind": k.Kind, "workload": k.Name},
				Samples: samples(1),
			})
		}
		return out, nil
	case strings.Contains(q, "kube_node_status_allocatable"):
		return []recommender.Series{{Labels: recommender.Labels{"cluster": "c1"}, Samples: samples(0.6)}}, nil
	case strings.Contains(q, "count by (cluster) (max by (cluster, node) (kerf_node_hourly_cost_usd"):
		return []recommender.Series{{Labels: recommender.Labels{"cluster": "c1"}, Samples: samples(3)}}, nil
	case strings.Contains(q, "kerf_node_hourly_cost_usd"):
		return []recommender.Series{{Labels: recommender.Labels{"cluster": "c1"}, Samples: samples(0.5)}}, nil
	}
	return nil, nil
}

func exec(t *testing.T, d *DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func addEvent(t *testing.T, d *DB, k WorkloadKey, typ string, at, received time.Time, containers ...hubapi.ContainerChange) {
	t.Helper()
	b, _ := json.Marshal(containers)
	exec(t, d, `INSERT INTO audit_events (id, cluster, namespace, workload_kind, workload_name, type, reason, time, received_at, containers)
		VALUES ($1, 'c1', $2, $3, $4, $5, 'RecommendationApplied', $6, $7, $8)`,
		uuid.New(), k.Namespace, k.Kind, k.Name, typ, at, received, b)
}

func TestWorkerMaterialises(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	a := WorkloadKey{Namespace: "shop", Kind: hubapi.KindDeployment, Name: "a"}
	b := WorkloadKey{Namespace: "shop", Kind: hubapi.KindDeployment, Name: "b"}
	exec(t, d, `INSERT INTO clusters (name) VALUES ('c1')`)
	// Rates stored by a recommender run before the changes.
	run := uuid.New()
	exec(t, d, `INSERT INTO recommender_runs (id, started_at, as_of, status) VALUES ($1, $2, $2, 'succeeded')`, run, day(-1))
	exec(t, d, `INSERT INTO recommender_run_clusters (run_id, cluster, as_of, finished_at, status, pricing)
		VALUES ($1, 'c1', $2, $2, 'succeeded', '{"cpuCoreHourUSD": 0.04, "memoryGiBHourUSD": 0.005, "source": "metrics"}')`, run, day(-1))
	// Workload a has a stored recommendation with 1 replica (used during the metrics gap); b has none.
	var wid int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO workloads (cluster, namespace, kind, name) VALUES ('c1', 'shop', 'Deployment', 'a') RETURNING id`).Scan(&wid); err != nil {
		t.Fatal(err)
	}
	exec(t, d, `INSERT INTO recommendations (id, workload_id, run_id, generated_at, strategy_name, settings, containers, confidence, data_coverage, average_replicas)
		VALUES ($1, $2, $3, $4, 'percentile', '{}', '[]', 0.9, '{}', 1)`, uuid.New(), wid, run, day(-1))

	// a: 1 core released, rolled back after 10 days. b: 1 core released, raised by 0.5 core after 20 days.
	// The events were stored before the upgrade (received long ago): the backfill counts them.
	addEvent(t, d, a, TypeApplied, day(0), day(0), ct("app", req("2", ""), req("1", "")))
	addEvent(t, d, a, TypeRolledBack, day(10), day(10), ct("app", req("1", ""), req("2", "")))
	addEvent(t, d, b, TypeApplied, day(0), day(0), ct("app", req("2", "2Gi"), req("1", "1Gi")))
	addEvent(t, d, b, TypeApplied, day(20), day(20), ct("app", req("1", ""), req("1500m", "")))

	now := day(30).Add(15 * time.Minute)
	prom := &fakeProm{workloads: []WorkloadKey{a, b}, gapFrom: day(5), gapTo: day(6)}
	coll := &Collector{}
	w := &Worker{
		DB: d, Local: prom, Now: func() time.Time { return now },
		MetricsFor: func(context.Context, string) (recommender.Querier, *datasource.Matcher, error) {
			return prom, nil, nil
		},
		DefaultRates: Rates{CPUCoreHour: 1, MemoryGiBHour: 1}, Collector: coll,
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	read := func(sc Scope, from, to time.Time, bucket string) *Summary {
		t.Helper()
		s, err := d.Summary(ctx, sc, from, to, bucket)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	one := func(k WorkloadKey) Scope { return Scope{Clusters: []string{"c1"}, Workload: &k} }
	// a: 240 h x 1 core x $0.04 (the gap day uses the recommendation's 1 replica).
	if got := read(one(a), day(0), day(30), BucketDay); round2(got.Total.CPU) != 9.60 || got.Total.Memory != 0 {
		t.Errorf("a total %+v, want cpu 9.60", got.Total)
	}
	// b: (480 h x 1 + 240 h x 0.5 - 24 h without replicas) x $0.04 = 23.04; memory 696 h x 1 GiB x $0.005.
	gotB := read(one(b), day(0), day(30), BucketDay)
	if round2(gotB.Total.CPU) != 23.04 || round2(gotB.Total.Memory) != 3.48 {
		t.Errorf("b total %+v, want cpu 23.04 memory 3.48", gotB.Total)
	}
	if gotB.Coverage.HoursWithoutReplicas != 24 || gotB.Coverage.Hours != 720 {
		t.Errorf("b coverage %+v", gotB.Coverage)
	}
	if gotB.Since == nil || !gotB.Since.Equal(day(0)) {
		t.Errorf("b since %v", gotB.Since)
	}
	if gotB.ReleasedNowCPUCores != 0.5 || gotB.ReleasedNowMemoryBytes != GiB {
		t.Errorf("b released now %v cores %v bytes", gotB.ReleasedNowCPUCores, gotB.ReleasedNowMemoryBytes)
	}
	if len(gotB.Points) != 30 || round2(gotB.Points[0].USD.CPU) != 0.96 || gotB.Points[25].CPUCores != 0.5 {
		t.Errorf("b points %d, first %+v, day 25 %+v", len(gotB.Points), gotB.Points[0], gotB.Points[25])
	}
	gotA := read(one(a), day(0), day(30), BucketDay)
	if gotA.Coverage.HoursReplicasFromRecs != 24 {
		t.Errorf("a coverage %+v", gotA.Coverage)
	}
	fleet := read(Scope{}, day(0), day(30), BucketMonth)
	if round2(fleet.Total.CPU) != 32.64 || fleet.ComputedThrough == nil || !fleet.ComputedThrough.Equal(day(30)) {
		t.Errorf("fleet %+v through %v", fleet.Total, fleet.ComputedThrough)
	}
	if len(fleet.Points) != 1 || round2(fleet.Points[0].USD.CPU) != 32.64 {
		t.Errorf("fleet months %+v", fleet.Points)
	}
	if v := testutil.ToFloat64(gaugeOf(t, coll, "kerf_hub_realized_savings_usd", "b", "cpu")); round2(v) != 23.04 {
		t.Errorf("kerf_hub_realized_savings_usd b cpu %v", v)
	}

	// Node cost trend: the last 30 days, hourly.
	pts, through, err := d.NodeCost(ctx, "c1", day(29), day(30), BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 24 || pts[0].NodeCostUSDPerHour == nil || *pts[0].NodeCostUSDPerHour != 0.5 ||
		*pts[0].Nodes != 3 || *pts[0].CPURequestedRatio != 0.6 || pts[0].ReleasedCPUCores != 0.5 || through == nil {
		t.Fatalf("node cost %d points, first %+v, through %v", len(pts), pts[0], through)
	}

	// Five minutes later: an event the agent buffered arrives (b rolled back on day 25). The next run
	// recomputes from that hour.
	addEvent(t, d, b, TypeRolledBack, day(25), now.Add(time.Minute), ct("app", req("1500m", "1Gi"), req("2", "2Gi")))
	now = now.Add(5 * time.Minute)
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	gotB = read(one(b), day(0), day(30), BucketDay)
	// cpu: (480 h x 1 + 120 h x 0.5 - 24 h) x $0.04 = 20.64; memory: (600 - 24) h x 1 GiB x $0.005 = 2.88.
	if round2(gotB.Total.CPU) != 20.64 || round2(gotB.Total.Memory) != 2.88 || gotB.ReleasedNowCPUCores != 0 {
		t.Errorf("b after the late rollback %+v, released now %v", gotB.Total, gotB.ReleasedNowCPUCores)
	}

	// A run with nothing new changes nothing.
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(Scope{}, day(0), day(30), BucketMonth); math.Abs(got.Total.CPU-(9.60+20.64)) > 0.005 {
		t.Errorf("idempotent rerun %+v", got.Total)
	}
}

func gaugeOf(t *testing.T, c *Collector, name, workload, resource string) prometheus.Collector {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["workload"] == workload && labels["resource"] == resource {
				return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name}, func() float64 { return m.GetGauge().GetValue() })
			}
		}
	}
	t.Fatalf("no %s{workload=%q, resource=%q}", name, workload, resource)
	return nil
}
