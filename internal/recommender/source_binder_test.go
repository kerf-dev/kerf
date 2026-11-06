package recommender

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestSourceBinder: a bound cluster's data queries go to its datasource with its matcher and are
// relabelled; pricing stays on the hub's own store (D-025).
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
