package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

func (e *testEnv) clusterRun(cluster string) (status, msg string, total float64) {
	e.t.Helper()
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT rc.status, rc.error, rc.monthly_savings_cpu_usd + rc.monthly_savings_memory_usd
		FROM recommender_run_clusters rc JOIN recommender_runs r ON r.id = rc.run_id
		WHERE rc.cluster = $1 AND NOT r.backfill ORDER BY r.started_at DESC LIMIT 1`, cluster).Scan(&status, &msg, &total); err != nil {
		e.t.Fatal(err)
	}
	return
}

// TestIncompleteMetrics: a regular run whose inventory or request series suddenly halve
// is not stored for the grace period; the summary and the savings trend keep the previous values.
func TestIncompleteMetrics(t *testing.T) {
	defer func(v int) { minGuardedInventory = v }(minGuardedInventory)
	minGuardedInventory = 2
	e := newEnv(t) // c1: 5 workloads, accepted by the regular run as of now
	var before hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &before)
	c1Before := clusterSummary(t, before, "c1")

	// Three of five workloads vanish from the metrics.
	for _, k := range []hubapi.WorkloadKey{kCheckout, kLedger, kFrontend} {
		e.loader.hide(k.ID(), true)
	}
	e.advance(5 * time.Minute)
	err := e.job.RunOnce(e.ctx, e.now, false)
	if err == nil || !strings.Contains(err.Error(), "metrics look incomplete") {
		t.Fatalf("run with 2 of 5 workloads: %v", err)
	}
	if st, msg, _ := e.clusterRun("c1"); st != "failed" || !strings.Contains(msg, "2 workloads") {
		t.Errorf("c1 run %s %q", st, msg)
	}
	if st, _, _ := e.clusterRun("c2"); st != "succeeded" {
		t.Errorf("c2 run %s: other clusters are unaffected", st)
	}
	var during hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &during)
	if got := clusterSummary(t, during, "c1"); got.Workloads != c1Before.Workloads ||
		got.MonthlySavingsUSD != c1Before.MonthlySavingsUSD {
		t.Errorf("summary changed by a refused run: %+v -> %+v", c1Before, got)
	}
	var tr hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?cluster=c1&step=5m&from="+e.now.Add(-time.Hour).Format(time.RFC3339), tokUI, &tr)
	if pts := tr.Series[0].Points; pts[len(pts)-1].Total != c1Before.MonthlySavingsUSD.Total {
		t.Errorf("trend last point %v, summary %v", pts[len(pts)-1].Total, c1Before.MonthlySavingsUSD.Total)
	}

	// The metrics come back: accepted, nothing was lost.
	for _, k := range []hubapi.WorkloadKey{kCheckout, kLedger, kFrontend} {
		e.loader.hide(k.ID(), false)
	}
	e.advance(5 * time.Minute)
	e.run(e.now, false)

	// Request series vanish for 3 of 4 workloads that had them: refused as well.
	e.loader.mu.Lock()
	e.loader.noReqs = map[string]bool{kCheckout.ID(): true, kLedger.ID(): true, kFrontend.ID(): true, kLegacy.ID(): true}
	e.loader.mu.Unlock()
	e.advance(5 * time.Minute)
	if err := e.job.RunOnce(e.ctx, e.now, false); err == nil || !strings.Contains(err.Error(), "with requests") {
		t.Fatalf("run without request series: %v", err)
	}
	e.loader.mu.Lock()
	e.loader.noReqs = nil
	e.loader.mu.Unlock()

	// A drop that persists past the grace period is real: accepted, workloads deleted.
	e.loader.hide(kCheckout.ID(), true)
	e.loader.hide(kLedger.ID(), true)
	e.loader.hide(kFrontend.ID(), true)
	e.advance(5 * time.Minute)
	if err := e.job.RunOnce(e.ctx, e.now, false); err == nil {
		t.Fatal("drop accepted at once")
	}
	e.advance(31 * time.Minute)
	e.run(e.now, false)
	var after hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &after)
	if got := clusterSummary(t, after, "c1"); got.Workloads != 2 {
		t.Errorf("after the grace period: %d workloads, want 2", got.Workloads)
	}

	// Disabled with a negative grace.
	e.loader.hide(kLegacy.ID(), true)
	e.job.Cfg.Recommender.IncompleteMetricsGrace = Duration(-1)
	e.advance(5 * time.Minute)
	e.run(e.now, false)
}

func clusterSummary(t *testing.T, s hubapi.FleetSummary, cluster string) hubapi.ClusterSummary {
	t.Helper()
	for _, c := range s.Clusters {
		if c.Cluster == cluster {
			return c
		}
	}
	t.Fatalf("no cluster %s in summary", cluster)
	return hubapi.ClusterSummary{}
}

// TestPricingFallback: without pricing series at a run's time, a regular run keeps the
// rates of the cluster's last run with series, a backfill uses today's series; both stay
// "metrics". Backfills that used the default rates are recomputed by the backfill plan.
func TestPricingFallback(t *testing.T) {
	e := newEnv(t) // runs priced at 0.03 / 0.004 (metrics)
	src := func(cluster string) (string, float64) {
		var s string
		var cpu float64
		if err := e.st.Pool.QueryRow(e.ctx, `SELECT rc.pricing->>'source', (rc.pricing->>'cpuCoreHourUSD')::float8
			FROM recommender_run_clusters rc JOIN recommender_runs r ON r.id = rc.run_id
			WHERE rc.cluster = $1 ORDER BY r.started_at DESC LIMIT 1`, cluster).Scan(&s, &cpu); err != nil {
			t.Fatal(err)
		}
		return s, cpu
	}

	// A gap in the series: the last run's rates.
	e.loader.mu.Lock()
	e.loader.noPricing = true
	e.loader.mu.Unlock()
	e.advance(5 * time.Minute)
	e.run(e.now, false)
	if s, cpu := src("c1"); s != "metrics" || cpu != 0.03 {
		t.Errorf("gap: %s %v, want the last run's metrics rates", s, cpu)
	}
	var list hubapi.RecommendationList
	e.getJSON("/v1/clusters/c1/recommendations", tokC1, &list)
	if p := list.Items[0].Pricing; p == nil || p.Source != "metrics" || p.CPUCoreHourUSD != 0.03 {
		t.Errorf("recommendation pricing %+v", p)
	}

	// A backfill before the series existed: today's series.
	e.loader.mu.Lock()
	e.loader.pricingNow = &recommender.Pricing{CPUCoreHour: 0.05, MemoryGiBHour: 0.005, Source: recommender.PricingSourceMetrics}
	e.loader.mu.Unlock()
	e.run(e.now.Add(-72*time.Hour), true)
	if s, cpu := src("c1"); s != "metrics" || cpu != 0.05 {
		t.Errorf("backfill: %s %v, want today's rates", s, cpu)
	}
	if len(e.loader.pricingAt) == 0 || !e.loader.pricingAt[len(e.loader.pricingAt)-1].Equal(e.now) {
		t.Errorf("PricingAt calls %v, want now %s", e.loader.pricingAt, e.now)
	}

	// No series ever, no earlier run within a day: the default rates.
	e.loader.mu.Lock()
	e.loader.pricingNow = nil
	e.loader.mu.Unlock()
	e.advance(25 * time.Hour)
	e.run(e.now, false)
	if s, _ := src("c1"); s != "default" {
		t.Errorf("no rates anywhere: %s, want default", s)
	}

	// Backfill runs priced with the default rates while the cluster's latest regular run has
	// series are recomputed.
	e.loader.mu.Lock()
	e.loader.noPricing = false
	e.loader.mu.Unlock()
	e.advance(5 * time.Minute)
	e.run(e.now, false)
	e.job.Cfg.Recommender.Backfill.Runs = 2
	e.job.Cfg.Recommender.Backfill.Every = Duration(24 * time.Hour)
	if err := e.job.RunBackfill(e.ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.st.BackfillRuns(e.ctx)
	var onGrid []string
	for _, r := range runs {
		if r.AsOf.Unix()%86400 == 0 {
			onGrid = append(onGrid, r.ID.String())
		}
	}
	if len(onGrid) == 0 {
		t.Fatal("no backfill runs on the grid")
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE recommender_run_clusters SET pricing = '{"source": "default", "cpuCoreHourUSD": 0.02, "memoryGiBHourUSD": 0.002}'
		WHERE run_id = $1::uuid AND cluster = 'c1'`, onGrid[0]); err != nil {
		t.Fatal(err)
	}
	plan, err := e.job.PlanBackfill(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale := false
	for _, id := range plan.Stale {
		stale = stale || id.String() == onGrid[0]
	}
	if !stale || len(plan.Missing) != 1 {
		t.Errorf("default-priced backfill not replaced: %+v", plan)
	}
}
