package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// newTestStore creates a fresh migrated database ro_hub_test_<random> (dropped at the end) when
// RO_TEST_DATABASE_URL is set to a superuser URL; otherwise the test is skipped.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	base := os.Getenv("RO_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("RO_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "ro_hub_test_" + hex.EncodeToString(b)
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
	st, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	applied, err := st.Migrate(ctx, os.DirFS("../../../db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) < 2 {
		t.Fatalf("applied migrations %v", applied)
	}
	return st
}

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func wkey(cluster, name string) hubapi.WorkloadKey {
	return hubapi.WorkloadKey{Cluster: cluster, Namespace: "ns", Kind: hubapi.KindDeployment, Name: name}
}

func inventory(keys ...hubapi.WorkloadKey) []recommender.WorkloadInventory {
	var out []recommender.WorkloadInventory
	for _, k := range keys {
		out = append(out, recommender.WorkloadInventory{Key: k, Containers: []string{"app"}, AverageReplicas: 2, Used24hCPU: 0.5, Used24hMemory: 1e9})
	}
	return out
}

func result(k hubapi.WorkloadKey, at time.Time, cpu string, savings float64) recommender.Result {
	id, _ := uuid.NewV7()
	return recommender.Result{
		Recommendation: hubapi.WorkloadRecommendation{
			ID: id.String(), Workload: k.Ref(), GeneratedAt: at, StrategyName: "percentile",
			Settings:   hubapi.ValueSettings{Profile: hubapi.ProfileProduction},
			Containers: []hubapi.ContainerRecommendation{{Name: "app", Recommended: hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: cpu}}}},
			Confidence: 0.9, RiskFlags: []string{}, AverageReplicas: 2,
			MonthlySavingsUSD: hubapi.Savings{CPU: savings, Memory: 1, Total: savings + 1},
			DataCoverage:      hubapi.DataCoverage{Ratio: 1, Window: "336h0m0s"},
		},
		Pricing: recommender.Pricing{Source: recommender.PricingSourceDefault},
	}
}

// insert stores recommendations for keys at the given times (one run per call).
func insert(t *testing.T, st *Store, cluster string, ids map[hubapi.WorkloadKey]int64, results ...recommender.Result) {
	t.Helper()
	run, _ := uuid.NewV7()
	if err := st.InsertRecommendations(context.Background(), run, ids, cluster, results); err != nil {
		t.Fatal(err)
	}
}

func recTimes(t *testing.T, st *Store, k hubapi.WorkloadKey) []time.Time {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(), `SELECT r.generated_at FROM recommendations r JOIN workloads w ON w.id = r.workload_id
		WHERE w.cluster = $1 AND w.name = $2 ORDER BY r.generated_at`, k.Cluster, k.Name)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		t.Fatal(err)
	}
	for i := range ts {
		ts[i] = ts[i].UTC()
	}
	return ts
}

func TestMigrateIdempotent(t *testing.T) {
	st := newTestStore(t)
	applied, err := st.Migrate(context.Background(), os.DirFS("../../../db/migrations"))
	if err != nil || len(applied) != 0 {
		t.Errorf("second migrate: %v %v", applied, err)
	}
}

func TestPruneHistory(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.EnsureCluster(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	w1, w2, w3 := wkey("c1", "w1"), wkey("c1", "w2"), wkey("c1", "w3")
	ids, err := st.UpsertInventory(ctx, "c1", inventory(w1, w2, w3), now.Add(-30*24*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	day := func(d int, hour int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day()-d, hour, 0, 0, 0, time.UTC)
	}
	w1Times := []time.Time{
		now.Add(-12 * 24 * time.Hour), // older than retention, not newest: deleted
		day(5, 10),                    // older than full resolution, not newest of its day: deleted
		day(5, 14),                    // newest of its day: kept
		day(4, 11),                    // only one of its day: kept
		now.Add(-24 * time.Hour),      // full resolution: kept
		now.Add(-23 * time.Hour),      // full resolution, same day: kept
		now.Add(-time.Hour),           // newest: kept
	}
	for _, at := range w1Times {
		insert(t, st, "c1", ids, result(w1, at, "100m", 1))
	}
	insert(t, st, "c1", ids, result(w2, now.Add(-20*24*time.Hour), "100m", 1)) // only one, older than retention: kept
	insert(t, st, "c1", ids, result(w3, now.Add(-15*24*time.Hour), "100m", 1)) // deleted
	insert(t, st, "c1", ids, result(w3, now.Add(-11*24*time.Hour), "100m", 1)) // newest: kept

	oldRun, newRun := uuid.New(), uuid.New()
	if err := st.StartRun(ctx, oldRun, now.Add(-11*24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := st.StartRun(ctx, newRun, now.Add(-24*time.Hour), true); err != nil {
		t.Fatal(err)
	}

	n, err := st.PruneHistory(ctx, now, 2*24*time.Hour, 10*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pruned %d, want 3", n)
	}
	if got, want := recTimes(t, st, w1), []time.Time{w1Times[2], w1Times[3], w1Times[4], w1Times[5], w1Times[6]}; !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("w1 kept %v\nwant %v", got, want)
	}
	if got := recTimes(t, st, w2); len(got) != 1 {
		t.Errorf("w2 kept %v", got)
	}
	if got := recTimes(t, st, w3); len(got) != 1 || !got[0].Equal(now.Add(-11*24*time.Hour)) {
		t.Errorf("w3 kept %v", got)
	}
	var runs []uuid.UUID
	rows, err := st.Pool.Query(ctx, `SELECT id FROM recommender_runs`)
	if err != nil {
		t.Fatal(err)
	}
	if runs, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runs, []uuid.UUID{newRun}) {
		t.Errorf("runs left %v, want only %s", runs, newRun)
	}
	// Idempotent.
	if n, err := st.PruneHistory(ctx, now, 2*24*time.Hour, 10*24*time.Hour); err != nil || n != 0 {
		t.Errorf("second prune: %d %v", n, err)
	}
	// The latest recommendation per workload is what LoadWorkloads returns.
	views, err := st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Rec == nil {
			t.Errorf("%s lost its latest recommendation", v.Key.Name)
		}
	}
}

func TestTokensAndClusters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.EnsureCluster(ctx, "c1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureCluster(ctx, "c1", "second"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureCluster(ctx, "Bad_Name", ""); err == nil {
		t.Error("invalid cluster name accepted")
	}
	cs, err := st.Clusters(ctx)
	if err != nil || len(cs) != 1 || cs[0].Description != "first" || cs[0].LastSeenAt != nil || cs[0].AgentVersion != "" {
		t.Fatalf("clusters %+v %v", cs, err)
	}
	for _, c := range []struct {
		name string
		want bool
	}{{"c1", true}, {"c2", false}} {
		if ok, err := st.ClusterExists(ctx, c.name); err != nil || ok != c.want {
			t.Errorf("ClusterExists(%s) = %v %v", c.name, ok, err)
		}
	}

	for _, tk := range []struct{ tok, role, cluster string }{{"a", "agent", "c1"}, {"u", "ui", ""}, {"a", "ui", ""}} {
		if err := st.EnsureToken(ctx, tk.tok, tk.role, tk.cluster, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.EnsureToken(ctx, "x", "agent", "", ""); err == nil {
		t.Error("agent token without cluster accepted")
	}
	if err := st.EnsureToken(ctx, "y", "ui", "c1", ""); err == nil {
		t.Error("ui token with cluster accepted")
	}
	if tok, err := st.LookupToken(ctx, "a"); err != nil || tok.Role != "agent" || tok.Cluster != "c1" {
		t.Errorf("agent token %+v %v (EnsureToken must not overwrite)", tok, err)
	}
	if tok, err := st.LookupToken(ctx, "u"); err != nil || tok.Role != "ui" || tok.Cluster != "" {
		t.Errorf("ui token %+v %v", tok, err)
	}
	if _, err := st.LookupToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE token_sha256 = $1`, HashToken("u")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupToken(ctx, "u"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token: %v", err)
	}
	var stored []byte
	if err := st.Pool.QueryRow(ctx, `SELECT token_sha256 FROM api_tokens WHERE role = 'agent'`).Scan(&stored); err != nil || len(stored) != 32 {
		t.Errorf("stored hash %x %v", stored, err)
	}

	if err := st.Heartbeat(ctx, "c1", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat(ctx, "unknown", "1"); err != nil {
		t.Errorf("heartbeat for unknown cluster: %v", err)
	}
	cs, _ = st.Clusters(ctx)
	if cs[0].AgentVersion != "1.0.0" || cs[0].LastSeenAt == nil {
		t.Errorf("after heartbeat %+v", cs[0])
	}
	if !IsUniqueViolation(insertDuplicateCluster(ctx, st)) {
		t.Error("IsUniqueViolation false for a duplicate key")
	}
	if IsUniqueViolation(errors.New("x")) || IsUniqueViolation(nil) {
		t.Error("IsUniqueViolation true for a plain error")
	}
}

// insertDuplicateCluster inserts a duplicate cluster row (for IsUniqueViolation).
func insertDuplicateCluster(ctx context.Context, s *Store) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO clusters (name) VALUES ('c1')`)
	return err
}

func TestInventoryAndHistory(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, c := range []string{"c1", "c2"} {
		if err := st.EnsureCluster(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	a, b, other := wkey("c1", "a"), wkey("c1", "b"), wkey("c2", "a")
	t1, t2, t3 := now.Add(-48*time.Hour), now.Add(-24*time.Hour), now
	ids, err := st.UpsertInventory(ctx, "c1", inventory(a, b), t2, true)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids %v %v", ids, err)
	}
	if _, err := st.UpsertInventory(ctx, "c2", inventory(other), t2, true); err != nil {
		t.Fatal(err)
	}
	// A backfill as of an earlier time moves first_seen back but never last_seen or deleted_at.
	ids2, err := st.UpsertInventory(ctx, "c1", inventory(a), t1, false)
	if err != nil || ids2[a] != ids[a] {
		t.Fatalf("backfill ids %v %v", ids2, err)
	}
	views, err := st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	if err != nil || len(views) != 2 {
		t.Fatalf("views %v %v", views, err)
	}
	if va := views[0]; va.Key != a || !va.FirstSeenAt.Equal(t1) || !va.LastSeenAt.Equal(t2) || va.Attributes.AverageReplicas != 2 ||
		va.Attributes.Used24h["cpu"] != 0.5 || !slices.Equal(va.Attributes.Containers, []string{"app"}) || va.Rec != nil || va.Status != nil {
		t.Errorf("view a %+v", va)
	}
	if views[0].Modes() != hubapi.DefaultModes() || views[0].Phase() != hubapi.PhasePending {
		t.Errorf("defaults %+v %s", views[0].Modes(), views[0].Phase())
	}
	// A regular run without b deletes b, only in c1.
	if _, err := st.UpsertInventory(ctx, "c1", inventory(a), t3, true); err != nil {
		t.Fatal(err)
	}
	views, _ = st.LoadWorkloads(ctx, WorkloadFilter{})
	var keys []hubapi.WorkloadKey
	for _, v := range views {
		keys = append(keys, v.Key)
	}
	if !slices.Equal(keys, []hubapi.WorkloadKey{a, other}) {
		t.Errorf("live workloads %v", keys)
	}
	// Single-key lookups return deleted workloads too.
	if vb, err := st.LoadWorkloads(ctx, WorkloadFilter{Key: &b}); err != nil || len(vb) != 1 {
		t.Errorf("deleted b by key: %v %v", vb, err)
	}
	// b comes back.
	if _, err := st.UpsertInventory(ctx, "c1", inventory(a, b), t3, true); err != nil {
		t.Fatal(err)
	}
	if views, _ := st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"}); len(views) != 2 {
		t.Errorf("b not restored: %d", len(views))
	}
	// Recommendations: history, steps, latest.
	insert(t, st, "c1", ids, result(a, t1, "100m", 1))
	insert(t, st, "c1", ids, result(a, t2, "200m", 2), result(b, t2, "300m", 3))
	insert(t, st, "c1", ids, result(a, t3, "300m", 4))
	if err := st.InsertRecommendations(ctx, uuid.New(), ids, "c1", []recommender.Result{result(wkey("c1", "zz"), t3, "1", 1)}); err == nil {
		t.Error("recommendation for a workload missing from the inventory accepted")
	}
	h, err := st.RecommendationHistory(ctx, ids[a], a.Ref(), t1, t3, 10)
	if err != nil || len(h) != 2 || !h[0].GeneratedAt.Equal(t2) || !h[1].GeneratedAt.Equal(t1) {
		t.Fatalf("history [t1, t3): %+v %v", h, err)
	}
	if h[0].MonthlySavingsUSD != (hubapi.Savings{CPU: 2, Memory: 1, Total: 3}) || h[0].Workload != a.Ref() || h[0].Settings.Profile != "production" ||
		h[0].DataCoverage.Window != "336h0m0s" || h[0].RiskFlags == nil {
		t.Errorf("history item %+v", h[0])
	}
	if h, _ := st.RecommendationHistory(ctx, ids[a], a.Ref(), t1, t3.Add(time.Second), 1); len(h) != 1 || !h[0].GeneratedAt.Equal(t3) {
		t.Errorf("history limit 1: %+v", h)
	}
	steps, err := st.RecommendationSteps(ctx, ids[a], t2.Add(time.Hour), t3)
	if err != nil || len(steps) != 2 || !steps[0].At.Equal(t2) || !steps[1].At.Equal(t3) || steps[1].Containers[0].Recommended.Requests.CPU != "300m" {
		t.Errorf("steps %+v %v", steps, err)
	}
	if steps, _ := st.RecommendationSteps(ctx, ids[a], t1.Add(-time.Hour), t1.Add(-time.Minute)); len(steps) != 0 {
		t.Errorf("steps before any recommendation: %+v", steps)
	}
	views, _ = st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	if views[0].Rec == nil || !views[0].Rec.GeneratedAt.Equal(t3) || views[1].Rec == nil || !views[1].Rec.GeneratedAt.Equal(t2) {
		t.Errorf("latest recs %+v %+v", views[0].Rec, views[1].Rec)
	}

	// A workload the agent reported after the run's data end is not deleted by that run; one last
	// seen before the data end is.
	fresh := wkey("c1", "fresh")
	if err := st.UpsertStatuses(ctx, "c1", []hubapi.WorkloadStatus{{Workload: fresh.Ref(), Phase: "Pending",
		Modes: hubapi.DefaultModes()}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE workloads SET last_seen_at = $1 WHERE name = 'fresh'`, t3.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertInventoryAt(ctx, "c1", inventory(a), t3.Add(5*time.Minute), true, t3); err != nil {
		t.Fatal(err)
	}
	views, _ = st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	keys = nil
	for _, v := range views {
		keys = append(keys, v.Key)
	}
	if !slices.Equal(keys, []hubapi.WorkloadKey{a, fresh}) {
		t.Errorf("after a run with data end before fresh: live %v, want a and fresh (b deleted)", keys)
	}
}

func TestRunsAndSavings(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, c := range []string{"c1", "c2"} {
		if err := st.EnsureCluster(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := st.HasSucceededRun(ctx); err != nil || ok {
		t.Errorf("HasSucceededRun on empty db: %v %v", ok, err)
	}
	type run struct {
		asOf     time.Time
		backfill bool
		status   string
		perC     map[string]float64
	}
	runs := []run{
		{now.Add(-72 * time.Hour), true, "succeeded", map[string]float64{"c1": 10, "c2": 1}},
		{now.Add(-48 * time.Hour), true, "succeeded", map[string]float64{"c1": 20}},
		{now.Add(-24 * time.Hour), false, "failed", nil},
		{now, false, "succeeded", map[string]float64{"c1": 30, "c2": 3}},
	}
	for i, r := range runs {
		id := uuid.New()
		if err := st.StartRun(ctx, id, r.asOf, r.backfill); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if err := st.RecordRunCluster(ctx, RunCluster{RunID: id, Cluster: "c1", AsOf: r.asOf, Status: "failed", Error: "boom"}); err != nil {
				t.Fatal(err)
			}
		}
		for c, v := range r.perC {
			if err := st.RecordRunCluster(ctx, RunCluster{RunID: id, Cluster: c, AsOf: r.asOf, Status: "succeeded",
				Savings: hubapi.Savings{CPU: v, Memory: v / 10}, Pricing: recommender.Pricing{Source: "metrics"},
				FinishedAt: r.asOf.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.FinishRun(ctx, id, r.status, ""); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if ok, _ := st.HasSucceededRun(ctx); !ok {
				t.Error("HasSucceededRun false after a succeeded run")
			}
		}
	}
	last, err := st.LastRunPerCluster(ctx)
	if err != nil || len(last) != 2 {
		t.Fatalf("LastRunPerCluster %v %v", last, err)
	}
	var lastFinished time.Time
	if err := st.Pool.QueryRow(ctx, `SELECT max(finished_at) FROM recommender_run_clusters WHERE as_of = $1 AND cluster = 'c1'`, now).Scan(&lastFinished); err != nil {
		t.Fatal(err)
	}
	if !last["c1"].Equal(lastFinished.UTC()) {
		t.Errorf("c1 last run %s, want the regular run's %s", last["c1"], lastFinished)
	}

	samples, err := st.SavingsSamples(ctx, nil, now.Add(-50*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	type s struct {
		c   string
		at  time.Time
		cpu float64
	}
	var got []s
	for _, x := range samples {
		got = append(got, s{x.Cluster, x.At.UTC(), x.CPU})
	}
	want := []s{
		{"c1", now.Add(-72 * time.Hour), 10}, // last before from
		{"c2", now.Add(-72 * time.Hour), 1},  // last before from
		{"c1", now.Add(-48 * time.Hour), 20},
		{"c1", now.Add(time.Minute), 30}, // regular run: effective when it finished
		{"c2", now.Add(time.Minute), 3},
	}
	if !slices.Equal(got, want) {
		t.Errorf("samples\n got %v\nwant %v", got, want)
	}
	samples, _ = st.SavingsSamples(ctx, []string{"c2"}, now.Add(-time.Hour), now.Add(-time.Minute))
	if len(samples) != 1 || samples[0].Cluster != "c2" || samples[0].CPU != 1 || samples[0].Memory != 0.1 {
		t.Errorf("c2 samples before the latest run %+v", samples)
	}
}

func TestStatusesEventsAudit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.EnsureCluster(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	a := wkey("c1", "a")
	ids, err := st.UpsertInventory(ctx, "c1", inventory(a), now, true)
	if err != nil {
		t.Fatal(err)
	}
	observed := now.Add(-time.Minute)
	recID := uuid.NewString()
	items := []hubapi.WorkloadStatus{
		{Workload: a.Ref(), Modes: hubapi.EffectiveModes{CPU: "auto-patch", Memory: "off", CPUSource: "policy"}, Phase: "Applied",
			Settings: &hubapi.ValueSettings{Profile: "dev"}, RecommendationID: recID, ObservedAt: &observed, Strategy: "rollout"},
		// Unknown workload: created by the report.
		{Workload: hubapi.WorkloadRef{Namespace: "ns", Kind: "StatefulSet", Name: "new"}, Modes: hubapi.EffectiveModes{CPU: "off", Memory: "off"}, Phase: "Pending"},
	}
	if err := st.UpsertStatuses(ctx, "c1", items, now); err != nil {
		t.Fatal(err)
	}
	items[0].Phase = "Blocked"
	if err := st.UpsertStatuses(ctx, "c1", items[:1], now); err != nil {
		t.Fatal(err)
	}
	views, err := st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	if err != nil || len(views) != 2 {
		t.Fatalf("views %v %v", views, err)
	}
	v := views[0] // ns/Deployment/a sorts before ns/StatefulSet/new
	if v.Key != a || views[1].Key.Name != "new" || views[1].Status == nil || views[1].Status.Phase != "Pending" {
		t.Fatalf("views %+v %+v", views[0].Key, views[1].Key)
	}
	if v.Status == nil || v.Status.Phase != "Blocked" || v.Modes().CPU != "auto-patch" || v.Status.RecommendationID != recID ||
		!v.Status.ObservedAt.Equal(observed) || v.Status.Settings == nil || v.Status.Settings.Profile != "dev" || v.Status.Strategy != "rollout" {
		t.Errorf("status %+v", v.Status)
	}
	rs, err := st.ReportedSettings(ctx, "c1")
	if err != nil || len(rs) != 1 || rs[a] == nil || rs[a].Profile != "dev" {
		t.Errorf("reported settings %v %v", rs, err)
	}

	ev := func(id int, at time.Time, k hubapi.WorkloadKey) hubapi.Event {
		return hubapi.Event{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(id)}).String(), Type: "applied", Time: at, Workload: k.Ref(), Reason: "Applied"}
	}
	e1, e2, e3 := ev(1, now.Add(-3*time.Hour), a), ev(2, now.Add(-2*time.Hour), a), ev(3, now.Add(-time.Hour), wkey("c1", "ghost"))
	stored, dups, err := st.InsertEvents(ctx, "c1", []hubapi.Event{e1, e2, e3})
	if err != nil || dups != 0 || len(stored) != 3 {
		t.Fatalf("insert: %d %d %v", len(stored), dups, err)
	}
	stored, dups, err = st.InsertEvents(ctx, "c1", []hubapi.Event{e1, e2, e1})
	if err != nil || dups != 3 || len(stored) != 0 {
		t.Errorf("re-insert: %d %d %v", len(stored), dups, err)
	}
	var linked int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workload_id = $1`, ids[a]).Scan(&linked); err != nil || linked != 2 {
		t.Errorf("events linked to workload a: %d %v", linked, err)
	}
	evs, more, err := st.QueryAudit(ctx, AuditQuery{Limit: 2})
	if err != nil || !more || len(evs) != 2 || evs[0].ID != e1.ID {
		t.Fatalf("page 1: %v %v %v", evs, more, err)
	}
	evs, more, err = st.QueryAudit(ctx, AuditQuery{Limit: 2, AfterTime: &evs[1].Time, AfterID: evs[1].ID})
	if err != nil || more || len(evs) != 1 || evs[0].ID != e3.ID || evs[0].WorkloadID != "c1/ns/Deployment/ghost" {
		t.Errorf("page 2: %+v %v %v", evs, more, err)
	}
	evs, _, _ = st.QueryAudit(ctx, AuditQuery{Limit: 10, Desc: true, Types: []string{"applied"}, Workload: "a"})
	if len(evs) != 2 || evs[0].ID != e2.ID {
		t.Errorf("desc workload a: %+v", evs)
	}
	wevs, err := st.WorkloadEvents(ctx, a, now.Add(-3*time.Hour), now, 10)
	if err != nil || len(wevs) != 2 || wevs[0].ID != e2.ID {
		t.Errorf("workload events %+v %v", wevs, err)
	}
	if wevs, _ := st.WorkloadEvents(ctx, a, now.Add(-3*time.Hour+time.Second), now, 10); len(wevs) != 1 {
		t.Errorf("from is inclusive, events before excluded: %+v", wevs)
	}
}
