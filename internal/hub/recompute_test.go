package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
)

// runClusters returns the clusters a run recorded results for.
func (e *testEnv) runClusters(runID string) []string {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, `SELECT cluster FROM recommender_run_clusters WHERE run_id = $1 ORDER BY cluster`, runID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestRecompute(t *testing.T) {
	e := newEnv(t)
	before, err := e.st.LastRunPerCluster(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.advance(10 * time.Minute)

	// Auth and validation.
	e.problem(e.do("POST", "/v1/admin/recompute", tokUI, nil), 403, "role_forbidden")
	e.problem(e.do("POST", "/v1/admin/recompute", tokC1, nil), 403, "role_forbidden")
	e.problem(e.do("POST", "/v1/admin/recompute", "", nil), 401, "unauthorized")
	e.problem(e.do("POST", "/v1/admin/recompute?cluster=Bad_Name", tokAdmin, nil), 400, "invalid_cluster")
	e.problem(e.do("POST", "/v1/admin/recompute?cluster=zz", tokAdmin, nil), 404, "cluster_not_found")

	// One cluster: 202 with the run id; the run covers c1 only and moves its last run.
	r := e.do("POST", "/v1/admin/recompute?cluster=c1", tokAdmin, nil)
	if r.code != http.StatusAccepted {
		t.Fatalf("recompute: %d %s", r.code, r.body)
	}
	var acc hubapi.RecomputeAccepted
	decode(t, r.body, &acc)
	if _, err := uuid.Parse(acc.RunID); err != nil || acc.Cluster != "c1" || acc.Coalesced || !acc.RequestedAt.Equal(e.now) {
		t.Errorf("accepted: %+v", acc)
	}
	e.job.WaitTriggered()
	if got := e.runClusters(acc.RunID); !slices.Equal(got, []string{"c1"}) {
		t.Errorf("run clusters %v, want [c1]", got)
	}
	after, err := e.st.LastRunPerCluster(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after["c1"].After(before["c1"]) || !after["c2"].Equal(before["c2"]) {
		t.Errorf("last runs before %v after %v", before, after)
	}
	var list hubapi.RecommendationList
	e.getJSON("/v1/clusters/c1/recommendations", tokC1, &list)
	for _, it := range list.Items {
		if !it.GeneratedAt.Equal(e.now.Truncate(fakeStep)) {
			t.Errorf("%s generated %s, want the recompute's data end %s", it.Workload.Name, it.GeneratedAt, e.now.Truncate(fakeStep))
		}
	}

	// While a run is in progress, requests queue; the same scope coalesces until its run starts.
	e.advance(10 * time.Minute)
	e.job.mu.Lock()
	var all1, all2, c2 hubapi.RecomputeAccepted
	for _, x := range []struct {
		q   string
		dst *hubapi.RecomputeAccepted
	}{{"", &all1}, {"", &all2}, {"?cluster=c2", &c2}} {
		r := e.do("POST", "/v1/admin/recompute"+x.q, tokAdmin, nil)
		if r.code != http.StatusAccepted {
			t.Fatalf("recompute%s: %d %s", x.q, r.code, r.body)
		}
		decode(t, r.body, x.dst)
	}
	e.job.mu.Unlock()
	if all1.Coalesced || !all2.Coalesced || all2.RunID != all1.RunID || !all2.RequestedAt.Equal(all1.RequestedAt) || all1.Cluster != "" {
		t.Errorf("coalescing: %+v %+v", all1, all2)
	}
	if c2.Coalesced || c2.RunID == all1.RunID {
		t.Errorf("other scope coalesced: %+v", c2)
	}
	e.job.WaitTriggered()
	if got := e.runClusters(all1.RunID); !slices.Equal(got, []string{"c1", "c2"}) {
		t.Errorf("all-cluster run clusters %v", got)
	}
	if got := e.runClusters(c2.RunID); !slices.Equal(got, []string{"c2"}) {
		t.Errorf("c2 run clusters %v", got)
	}
	// Once started, a new request queues a new run.
	r = e.do("POST", "/v1/admin/recompute", tokAdmin, nil)
	decode(t, r.body, &acc)
	if acc.Coalesced || acc.RunID == all1.RunID {
		t.Errorf("after the run: %+v", acc)
	}
	e.job.WaitTriggered()

	// A cluster removed after the request: the run fails, nothing panics.
	id, _, _, err := e.job.Trigger(context.Background(), "gone", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.job.WaitTriggered()
	var status string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT status FROM recommender_runs WHERE id = $1`, id).Scan(&status); err != nil || status != "failed" {
		t.Errorf("run for an unknown cluster: %q %v", status, err)
	}
}

func TestBackfillPlan(t *testing.T) {
	e := newEnv(t) // two backfill runs as of now-48h and now-24h (off the daily grid), one regular run as of now
	day := 24 * time.Hour
	e.job.Cfg.Recommender.Backfill.Runs = 3
	e.job.Cfg.Recommender.Backfill.Every = Duration(day)
	old, err := e.st.BackfillRuns(e.ctx)
	if err != nil || len(old) != 2 {
		t.Fatalf("setup backfill runs %v %v", old, err)
	}
	plan, err := e.job.PlanBackfill(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	midnight := alignDown(e.now, day)
	want := []time.Time{midnight, midnight.Add(-day), midnight.Add(-2 * day)}
	if !slices.EqualFunc(plan.Missing, want, time.Time.Equal) || len(plan.Stale) != 2 {
		t.Fatalf("plan: missing %v (want %v), stale %v", plan.Missing, want, plan.Stale)
	}
	if err := e.job.RunBackfill(e.ctx); err != nil {
		t.Fatal(err)
	}
	runs, err := e.st.BackfillRuns(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []time.Time
	for _, r := range runs {
		got = append(got, r.AsOf)
		if r.Status != "succeeded" {
			t.Errorf("backfill run %+v", r)
		}
	}
	slices.Reverse(want)
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("backfill runs %v, want %v", got, want)
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM recommendations WHERE run_id = ANY($1)`,
		[]uuid.UUID{old[0].ID, old[1].ID}).Scan(&n); err != nil || n != 0 {
		t.Errorf("recommendations of stale runs: %d %v", n, err)
	}
	// The new history is in the workload's history and the savings trend.
	var h hubapi.WorkloadHistory
	e.getJSON(wlPath(kCheckout, "/history?from="+want[0].Add(-time.Hour).Format(time.RFC3339)), tokUI, &h)
	var times []time.Time
	for _, r := range h.Recommendations {
		times = append(times, r.GeneratedAt)
	}
	for _, w := range want {
		if !slices.ContainsFunc(times, w.Equal) {
			t.Errorf("history %v lacks the backfill as of %s", times, w)
		}
	}
	var tr hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?step=24h&from="+want[0].Format(time.RFC3339), tokUI, &tr)
	if len(tr.Series) != 1 || len(tr.Series[0].Points) == 0 || tr.Series[0].Points[0].Total <= 0 {
		t.Errorf("trend from the first backfill: %+v", tr)
	}

	// Idempotent: nothing left to do.
	plan, err = e.job.PlanBackfill(e.ctx)
	if err != nil || len(plan.Missing) != 0 || len(plan.Stale) != 0 {
		t.Errorf("second plan %+v %v", plan, err)
	}
	// A failed backfill run is redone and replaced.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE recommender_runs SET status = 'failed' WHERE id = $1`, runs[1].ID); err != nil {
		t.Fatal(err)
	}
	plan, _ = e.job.PlanBackfill(e.ctx)
	if !slices.EqualFunc(plan.Missing, []time.Time{runs[1].AsOf}, time.Time.Equal) || !slices.Equal(plan.Stale, []uuid.UUID{runs[1].ID}) {
		t.Errorf("plan after a failed run: %+v", plan)
	}
	// Only within the history retention, and nothing when disabled.
	e.job.Cfg.History.Retention = Duration(e.now.Sub(midnight) + day + time.Hour)
	plan, _ = e.job.PlanBackfill(e.ctx)
	if len(plan.Missing) != 1 {
		t.Errorf("plan within retention: %+v", plan)
	}
	e.job.Cfg.Recommender.Backfill.Runs = 0
	if plan, _ = e.job.PlanBackfill(e.ctx); len(plan.Missing)+len(plan.Stale) != 0 {
		t.Errorf("disabled backfill planned %+v", plan)
	}
}

// TestBackfillFreshDB: on an empty database the loop runs the regular run first, then fills
// the history before it, then waits for the interval.
func TestBackfillFreshDB(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.Pool.Exec(e.ctx, `TRUNCATE recommender_runs CASCADE; TRUNCATE recommendations`); err != nil {
		t.Fatal(err)
	}
	e.job.Cfg.Recommender.Backfill.Runs = 2
	e.job.Cfg.Recommender.Backfill.Every = Duration(24 * time.Hour)
	e.job.Cfg.Recommender.Interval = Duration(time.Hour)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.job.Loop(ctx); close(done) }()
	deadline := time.Now().Add(20 * time.Second)
	var runs []store.RunInfo
	for time.Now().Before(deadline) {
		runs, _ = e.st.BackfillRuns(e.ctx)
		if len(runs) == 2 && runs[0].Status == "succeeded" && runs[1].Status == "succeeded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if len(runs) != 2 {
		t.Fatalf("backfill runs %+v", runs)
	}
	var regularStart, firstBackfillStart time.Time
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT min(started_at) FILTER (WHERE NOT backfill), min(started_at) FILTER (WHERE backfill) FROM recommender_runs`).
		Scan(&regularStart, &firstBackfillStart); err != nil {
		t.Fatal(err)
	}
	if !regularStart.Before(firstBackfillStart) {
		t.Errorf("regular run started %s, backfill %s: the regular run must come first", regularStart, firstBackfillStart)
	}
}

func decode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}
