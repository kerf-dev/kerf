package recommender

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestSourceBinder: a bound cluster's data queries go to its datasource with its matcher and are
// relabelled; pricing stays on the hub's own store.
func TestSourceBinder(t *testing.T) {
	cfg := DefaultConfig()
	local := newFakeQuerier()
	bound := newFakeQuerier()
	m, _ := datasource.NewMatcher("c1", &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod"})
	src := &Source{Q: local, Cfg: cfg, Binder: func(_ context.Context, cluster string) (Querier, *datasource.Matcher, error) {
		if cluster != "c1" {
			t.Errorf("binder asked for %s", cluster)
		}
		return bound, &m, nil
	}}
	if _, err := src.Load(context.Background(), "c1", testEnd, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(bound.called) == 0 {
		t.Fatal("no query on the bound datasource")
	}
	for q := range bound.called {
		if strings.Contains(q, "kerf_cpu_core_hourly_cost_usd") || strings.Contains(q, "kerf_memory_gib_hourly_cost_usd") {
			t.Errorf("pricing query on the bound datasource: %.80s", q)
		}
		if !strings.Contains(q, `k8s_cluster="prod"`) || !strings.Contains(q, `"cluster", "c1", "", ""`) {
			t.Errorf("bound query without matcher or relabel: %.120s", q)
		}
	}
	pricing := 0
	for q := range local.called {
		if !strings.Contains(q, "hourly_cost_usd") && !strings.Contains(q, "kerf_pricing_cur_") {
			t.Errorf("data query on the local store: %.80s", q)
		}
		pricing++
	}
	// rates, node count, rate sources, the cur period and the GPU price
	if pricing != 7 {
		t.Errorf("%d pricing queries on the local store, want 7", pricing)
	}

	// a binder error fails the load; a nil matcher is the default
	src.Binder = func(context.Context, string) (Querier, *datasource.Matcher, error) {
		return nil, nil, context.DeadlineExceeded
	}
	if _, err := src.Load(context.Background(), "c1", testEnd, time.Hour); err == nil {
		t.Error("binder error ignored")
	}
	local2 := newFakeQuerier()
	src = &Source{Q: local2, Cfg: cfg, Binder: func(context.Context, string) (Querier, *datasource.Matcher, error) { return nil, nil, nil }}
	if _, err := src.Load(context.Background(), "c1", testEnd, time.Hour); err != nil {
		t.Fatal(err)
	}
	for q := range local2.called {
		if strings.Contains(q, `"cluster", "c1", "", ""`) {
			t.Errorf("default binder relabels: %.80s", q)
		}
	}
}

// fakeWorkloadSource is a datasource that is not queried with PromQL.
type fakeWorkloadSource struct {
	req WorkloadRequest
	out map[hubapi.WorkloadKey]*RawWorkload
}

func (f *fakeWorkloadSource) Instant(context.Context, string, time.Time) ([]VectorSample, error) {
	return nil, errors.New("PromQL sent to a workload source")
}

func (f *fakeWorkloadSource) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]Series, error) {
	return nil, errors.New("PromQL sent to a workload source")
}

func (f *fakeWorkloadSource) LoadWorkloads(_ context.Context, req WorkloadRequest) (map[hubapi.WorkloadKey]*RawWorkload, error) {
	f.req = req
	return f.out, nil
}

// TestSourceWorkloadSource: a bound datasource that loads workloads itself replaces the PromQL
// queries; pricing stays on the hub's own store; its recommendations say the throttling input
// is derived and HPA coupling is unknown.
func TestSourceWorkloadSource(t *testing.T) {
	cfg := DefaultConfig()
	local := newFakeQuerier()
	key := hubapi.WorkloadKey{Cluster: "c1", Namespace: "shop", Kind: hubapi.KindDeployment, Name: "web"}
	var cpu, mem, pods []Sample
	for i := 0; i < 2016; i++ {
		ts := testEnd.Add(-time.Duration(2016-i) * 5 * time.Minute)
		cpu = append(cpu, Sample{T: ts, V: 0.2})
		mem = append(mem, Sample{T: ts, V: 200 * MiB})
		pods = append(pods, Sample{T: ts, V: 2})
	}
	ws := &fakeWorkloadSource{out: map[hubapi.WorkloadKey]*RawWorkload{key: {
		Key: key, Pods: pods, HasRequests: true, Live: true, ThrottleDerived: true, HPAUnknown: true,
		Containers: map[string]*RawContainer{"app": {Name: "app", CPU: cpu, Memory: mem, Throttle: []Sample{{T: testEnd, V: 0.01}},
			Current: Current{CPURequest: 1, MemoryRequest: 1 << 30}}},
	}}}
	m := datasource.Matcher{Cluster: "c1", Label: "cluster", Value: "prod-eu1"}
	src := &Source{Q: local, Cfg: cfg, Binder: func(context.Context, string) (Querier, *datasource.Matcher, error) { return ws, &m, nil }}
	d, err := src.Load(context.Background(), "c1", testEnd, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ws.req.Cluster != "c1" || ws.req.SourceCluster() != "prod-eu1" || ws.req.Step != cfg.Step || ws.req.RiskWindow != cfg.RiskWindow ||
		!ws.req.End.Equal(cfg.Align(testEnd)) || !ws.req.Start.Equal(ws.req.End.Add(-7*24*time.Hour)) {
		t.Errorf("request = %+v", ws.req)
	}
	for q := range local.called {
		if !strings.Contains(q, "hourly_cost_usd") && !strings.Contains(q, "kerf_pricing_cur_") {
			t.Errorf("data query on the local store: %.80s", q)
		}
	}
	if len(local.called) != 6 {
		t.Errorf("%d pricing queries, want 6 (no GPU prices)", len(local.called))
	}
	w := d.Workloads[key]
	if w == nil {
		t.Fatal("workload missing")
	}
	p := cfg.Profiles[hubapi.ProfileDev]
	res, ok := Recommend(Request{Workload: w.Data(p, d.End, cfg), Settings: cfg.EffectiveSettings(nil), Profile: p, Pricing: d.Pricing, End: d.End}, PercentileStrategy{}, cfg)
	if !ok {
		t.Fatal("no recommendation")
	}
	notes := strings.Join(res.Recommendation.Containers[0].Explanation.CPU.Notes, "; ")
	if !strings.Contains(notes, NoteThrottleDerived) || !strings.Contains(notes, NoteHPAUnknown) {
		t.Errorf("cpu notes = %q", notes)
	}
}
