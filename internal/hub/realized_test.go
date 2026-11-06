package hub

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// replicaQuerier answers the realized-savings replica query with one pod per workload, and
// nothing else.
type replicaQuerier struct{ workloads []hubapi.WorkloadKey }

func (q replicaQuerier) Instant(context.Context, string, time.Time) ([]recommender.VectorSample, error) {
	return nil, nil
}

func (q replicaQuerier) Range(_ context.Context, query string, start, end time.Time, step time.Duration) ([]recommender.Series, error) {
	if !strings.Contains(query, "count by (cluster, namespace, workload_kind, workload)") {
		return nil, nil
	}
	var out []recommender.Series
	for _, k := range q.workloads {
		s := recommender.Series{Labels: recommender.Labels{"cluster": k.Cluster, "namespace": k.Namespace, "workload_kind": k.Kind, "workload": k.Name}}
		for t := start; !t.After(end); t = t.Add(step) {
			s.Samples = append(s.Samples, recommender.Sample{T: t, V: 1})
		}
		out = append(out, s)
	}
	return out, nil
}

func TestRealizedSavingsAPI(t *testing.T) {
	e := newEnv(t)
	now := e.srv.now()
	gone := key("c1", "payments", hubapi.KindDeployment, "retired") // deleted since: only in the audit log
	// Known rates, stored before the changes (the env's own runs are only in the last two days).
	run := uuid.New()
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO recommender_runs (id, started_at, as_of, status) VALUES ($1, $2, $2, 'succeeded')`,
		run, now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO recommender_run_clusters (run_id, cluster, as_of, finished_at, status, pricing)
		VALUES ($1, 'c1', $2, $2, 'succeeded', '{"cpuCoreHourUSD": 0.04, "memoryGiBHourUSD": 0.005, "source": "metrics"}')`,
		run, now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ev := func(k hubapi.WorkloadKey, typ string, ago time.Duration, from, to string) hubapi.Event {
		return hubapi.Event{
			ID: uuid.NewString(), Type: typ, Time: now.Add(-ago), Reason: map[string]string{"applied": "RecommendationApplied", "rolled_back": "Restarts"}[typ],
			Workload: hubapi.WorkloadRef{Namespace: k.Namespace, Kind: k.Kind, Name: k.Name}, Strategy: "in-place", Resources: []string{"cpu"},
			Containers: []hubapi.ContainerChange{{Name: "app",
				From: &hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: from}}, To: &hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: to}}}},
		}
	}
	day := 24 * time.Hour
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{
		// checkout: 1 core released 30 days ago, rolled back 10 days later.
		ev(kCheckout, "applied", 30*day, "2", "1"),
		ev(kCheckout, "rolled_back", 20*day, "1", "2"),
		// retired: 500m released 30 days ago, still released.
		ev(gone, "applied", 30*day, "1", "500m"),
	}}, &res)
	if res.Accepted != 3 {
		t.Fatalf("accepted %d", res.Accepted)
	}

	w := e.srv.RealizedWorker()
	rq := replicaQuerier{workloads: []hubapi.WorkloadKey{kCheckout, gone}}
	w.Local, w.NodeCostBackfill = rq, 3*day
	w.MetricsFor = func(context.Context, string) (recommender.Querier, *datasource.Matcher, error) {
		return rq, nil, nil
	}
	if err := w.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	through := now.Add(-10 * time.Minute).Truncate(time.Hour)

	// checkout: 240 h x 1 core x $0.04.
	var wl hubapi.RealizedSavings
	e.getJSON(wlPath(kCheckout, "/savings/realized")+"?from="+now.Add(-31*day).Format(time.RFC3339), tokUI, &wl)
	if round2(wl.Total.CPU) != 9.60 || round2(wl.InRange.CPU) != 9.60 || wl.Total.Total != wl.Total.CPU+wl.Total.Memory || wl.Step != "day" {
		t.Errorf("checkout %+v", wl)
	}
	if wl.Since == nil || wl.ComputedThrough == nil || !wl.ComputedThrough.Equal(through) || wl.ReleasedNow.CPUCores != 0 {
		t.Errorf("checkout since %v through %v released now %+v", ptrStr(wl.Since), ptrStr(wl.ComputedThrough), wl.ReleasedNow)
	}
	if len(wl.Points) != 32 { // 31 days from a non-midnight start: 32 day buckets
		t.Errorf("checkout points %d", len(wl.Points))
	}

	// retired: still answers after deletion; 0.5 core from 30 days ago. Read up to 3 days ago: the
	// env's own recommender runs of the last two days stored other rates, which apply from then on.
	upTo := now.Add(-3 * day).Truncate(time.Hour)
	hours := upTo.Sub(now.Add(-30 * day)).Hours()
	var gw hubapi.RealizedSavings
	e.getJSON(wlPath(gone, "/savings/realized")+"?from="+now.Add(-30*day).Format(time.RFC3339)+"&to="+upTo.Format(time.RFC3339), tokUI, &gw)
	if !approx(gw.InRange.CPU, 0.5*hours*0.04, 0.005) || gw.ReleasedNow.CPUCores != 0.5 || gw.Total.CPU <= gw.InRange.CPU {
		t.Errorf("retired in range %+v total %+v released now %+v, want %.4f", gw.InRange, gw.Total, gw.ReleasedNow, 0.5*hours*0.04)
	}
	var sumPoints float64
	for _, p := range gw.Points {
		sumPoints += p.Total
	}
	if !approx(sumPoints, gw.InRange.Total, 1e-9) {
		t.Errorf("retired points add up to %v, in range %v", sumPoints, gw.InRange.Total)
	}

	// Fleet, by month, and filtered.
	var fleet hubapi.RealizedSavings
	e.getJSON("/v1/savings/realized", tokUI, &fleet)
	if !approx(fleet.Total.CPU, wl.Total.CPU+gw.Total.CPU, 1e-6) || fleet.Step != "month" || len(fleet.Points) != 13 {
		t.Errorf("fleet %+v (%d points)", fleet.Total, len(fleet.Points))
	}
	var none hubapi.RealizedSavings
	e.getJSON("/v1/savings/realized?cluster=c2", tokUI, &none)
	if none.Since != nil || none.Total.Total != 0 {
		t.Errorf("c2 %+v", none)
	}
	var ns hubapi.RealizedSavings
	e.getJSON("/v1/savings/realized?namespace=nothing-here&step=day", tokUI, &ns)
	if ns.Total.Total != 0 || len(ns.Points) != 31 {
		t.Errorf("namespace filter %+v (%d points)", ns.Total, len(ns.Points))
	}

	// Realized savings never change the estimate.
	var sum hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &sum)
	before := sum.Fleet.MonthlySavingsUSD

	// Node cost trend: the worker found no pricing series; points carry the released requests.
	var nc hubapi.NodeCostTrend
	e.getJSON("/v1/savings/realized/node-cost?cluster=c1&from="+now.Add(-2*day).Format(time.RFC3339), tokUI, &nc)
	wantPts := int(through.Sub(now.Add(-2 * day).Truncate(time.Hour)).Hours())
	if nc.Relation != "correlated" || nc.Step != "hour" || len(nc.Points) != wantPts || nc.Points[0].Released.CPUCores != 0.5 ||
		nc.Points[0].NodeCostUSDPerHour != nil {
		t.Errorf("node cost %+v", nc)
	}

	// Errors.
	e.problem(e.do(http.MethodGet, "/v1/savings/realized?step=week", tokUI, nil), 400, "invalid_step")
	e.problem(e.do(http.MethodGet, "/v1/savings/realized?cluster=Bad_Name", tokUI, nil), 400, "invalid_cluster")
	e.problem(e.do(http.MethodGet, "/v1/savings/realized?from=2026-11-01T00:00:00Z&to=2026-10-01T00:00:00Z", tokUI, nil), 400, "invalid_time_range")
	e.problem(e.do(http.MethodGet, wlPath(key("c1", "x", hubapi.KindDeployment, "nope"), "/savings/realized"), tokUI, nil), 404, "workload_not_found")
	e.problem(e.do(http.MethodGet, "/v1/savings/realized/node-cost", tokUI, nil), 400, "invalid_cluster")
	e.problem(e.do(http.MethodGet, "/v1/savings/realized/node-cost?cluster=zz", tokUI, nil), 404, "cluster_not_found")
	if r := e.do(http.MethodGet, "/v1/savings/realized", "", nil); r.code != 401 {
		t.Errorf("unauthenticated: %d", r.code)
	}

	// The hub exports the totals for Grafana.
	m := e.metrics()
	v, ok := m.find("kerf_hub_realized_savings_usd", map[string]string{"workload": "checkout", "resource": "cpu"})
	if f, _ := strconv.ParseFloat(v, 64); !ok || round2(f) != 9.60 {
		t.Errorf("kerf_hub_realized_savings_usd checkout cpu = %q", v)
	}
	if _, ok := m.find("kerf_hub_realized_computed_through_timestamp_seconds", map[string]string{"cluster": "c1"}); !ok {
		t.Error("no kerf_hub_realized_computed_through_timestamp_seconds")
	}
	e.getJSON("/v1/summary", tokUI, &sum)
	if sum.Fleet.MonthlySavingsUSD != before {
		t.Errorf("estimated savings changed: %+v -> %+v", before, sum.Fleet.MonthlySavingsUSD)
	}
}
