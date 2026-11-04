package hub

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// Hub HA tests (WO-18a): several hub replicas, each with its own pool, elector and HTTP server,
// on one database. They use the real clock.

type haHub struct {
	name    string
	st      *store.Store
	srv     *Server
	job     *Job
	loader  *fakeLoader
	elector *Elector
	ts      *httptest.Server
	stop    func()
}

type haOpts struct {
	interval  time.Duration
	retry     time.Duration
	check     time.Duration
	reconnect time.Duration
}

// newHADB returns the URL of a migrated database with clusters c1, c2 and the test tokens.
func newHADB(t *testing.T) string {
	t.Helper()
	st := newTestStore(t, "")
	ctx := context.Background()
	for _, c := range []string{"c1", "c2"} {
		if err := st.EnsureCluster(ctx, c, "test cluster "+c); err != nil {
			t.Fatal(err)
		}
	}
	for _, tk := range []struct{ tok, role, cluster string }{
		{tokC1, hubapi.RoleAgent, "c1"}, {tokC2, hubapi.RoleAgent, "c2"}, {tokUI, hubapi.RoleUI, ""}, {tokAdmin, hubapi.RoleAdmin, ""},
	} {
		if err := st.EnsureToken(ctx, tk.tok, tk.role, tk.cluster, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return st.Pool.Config().ConnString()
}

// startHAHub starts one hub replica (store, server, job, elector, HTTP) like cmd/hub does.
func startHAHub(t *testing.T, url, name string, o haOpts) *haHub {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.DatabaseURL = url
	cfg.Recommender.Interval = Duration(o.interval)
	cfg.HA.LeaderRetry, cfg.HA.LeaderCheck = Duration(o.retry), Duration(o.check)
	recCfg, err := cfg.RecommenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	h := &haHub{name: name, st: st, loader: &fakeLoader{now: time.Now(), workloads: defaultWorkloads()}}
	metrics := NewMetrics()
	h.srv = &Server{Cfg: cfg, Store: st, Prom: &fakeQuerier{}, RecCfg: recCfg, Metrics: metrics, Log: discardLog()}
	h.job = &Job{Store: st, Metrics: metrics, Cfg: cfg, Log: discardLog(),
		Engine: &recommender.Engine{Loader: h.loader, Cfg: recCfg, Strategy: recommender.PercentileStrategy{}}}
	h.srv.Job = h.job
	if h.elector, err = h.srv.NewElector(name); err != nil {
		t.Fatal(err)
	}
	h.elector.reconnect = o.reconnect
	h.srv.SetReady()
	h.ts = httptest.NewServer(h.srv.Handler())
	ectx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.elector.Run(ectx) }()
	var once sync.Once
	h.stop = func() {
		once.Do(func() {
			cancel()
			<-done
			h.ts.Close()
			st.Close()
		})
	}
	t.Cleanup(h.stop)
	return h
}

func (h *haHub) calls() int {
	h.loader.mu.Lock()
	defer h.loader.mu.Unlock()
	return h.loader.calls
}

func (h *haHub) post(t *testing.T, path, token string) resp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doReq(t, req)
}

func (h *haHub) get(t *testing.T, path, token string) resp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doReq(t, req)
}

func doReq(t *testing.T, req *http.Request) resp {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp{code: res.StatusCode, header: res.Header, body: []byte(b.String())}
}

// waitFor polls cond every 20 ms until it holds or timeout passes; it returns the time it took.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < timeout {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
	return 0
}

// leaderOf returns the one leading hub (nil unless exactly one leads).
func leaderOf(hubs ...*haHub) (leader, follower *haHub) {
	var ls, fs []*haHub
	for _, h := range hubs {
		if h.elector.IsLeader() {
			ls = append(ls, h)
		} else {
			fs = append(fs, h)
		}
	}
	if len(ls) != 1 {
		return nil, nil
	}
	if len(fs) > 0 {
		follower = fs[0]
	}
	return ls[0], follower
}

func regularRuns(t *testing.T, st *store.Store, since time.Time) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM recommender_runs WHERE NOT backfill AND started_at >= $1`, since).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestLeaderTwoHubs: two hubs on one database for 3 recommender intervals. Exactly one leads and
// runs the recommender: one run at start plus one per interval, not twice that; every workload
// gets one recommendation id per run, as with one hub. Every replica serves the API; the
// database-state series come from the leader only.
func TestLeaderTwoHubs(t *testing.T) {
	url := newHADB(t)
	const interval = 2 * time.Second
	o := haOpts{interval: interval, retry: 500 * time.Millisecond, check: 500 * time.Millisecond}
	start := time.Now()
	a := startHAHub(t, url, "hub-a", o)
	b := startHAHub(t, url, "hub-b", o)
	waitFor(t, 10*time.Second, "a leader", func() bool { l, _ := leaderOf(a, b); return l != nil })
	time.Sleep(3*interval + interval/2)
	leader, follower := leaderOf(a, b)
	if leader == nil {
		t.Fatalf("leaders: a=%v b=%v", a.elector.IsLeader(), b.elector.IsLeader())
	}
	runs := regularRuns(t, a.st, start)
	// One at start plus 3 intervals (the third tick may race the end of the window).
	if runs < 3 || runs > 4 {
		t.Errorf("%d recommender runs in 3.5 intervals, want 3-4 (one hub's worth)", runs)
	}
	if follower.calls() != 0 {
		t.Errorf("the follower ran the recommender (%d loads)", follower.calls())
	}
	// Recommendation ids: per workload exactly one per run, so no more churn than one hub.
	var maxPer, distinctRuns int
	if err := a.st.Pool.QueryRow(context.Background(), `SELECT coalesce(max(n), 0), (SELECT count(DISTINCT run_id) FROM recommendations)
		FROM (SELECT workload_id, count(DISTINCT id) AS n FROM recommendations GROUP BY workload_id) x`).Scan(&maxPer, &distinctRuns); err != nil {
		t.Fatal(err)
	}
	if maxPer != runs || distinctRuns != runs {
		t.Errorf("recommendation ids per workload %d, runs with recommendations %d; want %d each", maxPer, distinctRuns, runs)
	}
	// Leadership metrics.
	if g := testutil.ToFloat64(leader.srv.Metrics.leader) + testutil.ToFloat64(follower.srv.Metrics.leader); g != 1 {
		t.Errorf("sum of ro_hub_leader = %v", g)
	}
	if n := testutil.ToFloat64(leader.srv.Metrics.transitions); n != 1 {
		t.Errorf("leader transitions %v, want 1", n)
	}
	if n := testutil.ToFloat64(follower.srv.Metrics.transitions); n != 0 {
		t.Errorf("follower transitions %v, want 0", n)
	}
	// Both serve the whole API; state series only on the leader.
	for _, h := range []*haHub{leader, follower} {
		var list hubapi.RecommendationList
		r := h.get(t, "/v1/clusters/c1/recommendations", tokC1)
		decode(t, r.body, &list)
		if r.code != 200 || len(list.Items) == 0 {
			t.Errorf("%s: recommendations %d (%d items)", h.name, r.code, len(list.Items))
		}
		if r := h.get(t, "/readyz", ""); r.code != 200 {
			t.Errorf("%s: /readyz %d", h.name, r.code)
		}
	}
	lm := string(leader.get(t, "/metrics", "").body)
	fm := string(follower.get(t, "/metrics", "").body)
	for _, name := range []string{"ro_recommendation_cpu_cores{", "ro_recommender_last_success_timestamp_seconds{"} {
		if !strings.Contains(lm, name) || strings.Contains(fm, name) {
			t.Errorf("%s: on leader %v, on follower %v; want leader only", name, strings.Contains(lm, name), strings.Contains(fm, name))
		}
	}
	for _, m := range []string{lm, fm} {
		if !strings.Contains(m, "ro_hub_leader ") || !strings.Contains(m, "ro_hub_leader_transitions_total ") {
			t.Error("ro_hub_leader / ro_hub_leader_transitions_total missing")
		}
	}
}

// TestLeaderFailover: the leader's lock connection is terminated (as when its pod dies). Its
// work stops, the other hub takes over within 15 s with the production timings and runs the next
// interval; the old leader comes back as a follower.
func TestLeaderFailover(t *testing.T) {
	url := newHADB(t)
	const interval = 2 * time.Second
	// Production retry/check; the old leader reconnects only after 10 s so the takeover can be
	// timed against the other hub's retry.
	o := haOpts{interval: interval, retry: 5 * time.Second, check: 2 * time.Second, reconnect: 10 * time.Second}
	a := startHAHub(t, url, "hub-a", o)
	b := startHAHub(t, url, "hub-b", o)
	waitFor(t, 10*time.Second, "a leader", func() bool { l, _ := leaderOf(a, b); return l != nil })
	old, next := leaderOf(a, b)
	waitFor(t, 10*time.Second, "the first run", func() bool { return old.calls() > 0 })

	ctx := context.Background()
	var pid int
	if err := a.st.Pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1`,
		ElectorApplicationName(old.name)).Scan(&pid); err != nil {
		t.Fatalf("leader's elector connection: %v", err)
	}
	killed := time.Now()
	if _, err := a.st.Pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the old leader to stop leading", func() bool { return !old.elector.IsLeader() })
	took := waitFor(t, 15*time.Second, "the other hub to take over", next.elector.IsLeader)
	t.Logf("failover took %s after the lock connection was terminated", took.Round(10*time.Millisecond))
	oldCalls, nextCalls := old.calls(), next.calls()
	waitFor(t, 3*interval, "the new leader's next run", func() bool { return next.calls() > nextCalls })
	if regularRuns(t, a.st, killed) == 0 {
		t.Error("no run after the failover")
	}
	if old.calls() != oldCalls {
		t.Errorf("the old leader kept running the recommender after losing the lock")
	}
	if got := testutil.ToFloat64(old.srv.Metrics.leader); got != 0 {
		t.Errorf("old leader ro_hub_leader = %v", got)
	}
	if got := testutil.ToFloat64(old.srv.Metrics.transitions); got != 2 {
		t.Errorf("old leader transitions %v, want 2 (acquired, lost)", got)
	}
	// The old leader reconnects as a follower.
	waitFor(t, 15*time.Second, "the old leader to reconnect", old.elector.Listening)
	time.Sleep(o.retry + time.Second)
	if old.elector.IsLeader() || !next.elector.IsLeader() {
		t.Errorf("after reconnecting: old leads %v, new leads %v", old.elector.IsLeader(), next.elector.IsLeader())
	}
	// A graceful stop hands over at once (unlock + notification), without waiting for the retry.
	stopped := time.Now()
	next.stop()
	waitFor(t, 4*time.Second, "takeover after a graceful stop", old.elector.IsLeader)
	t.Logf("handover after a graceful stop took %s", time.Since(stopped).Round(10*time.Millisecond))
}

// TestMigrateConcurrent: two Migrate calls at once on an empty database both succeed and apply
// every migration exactly once between them.
func TestMigrateConcurrent(t *testing.T) {
	url := newTestDB(t, "")
	ctx := context.Background()
	files, err := fs.Glob(os.DirFS("../../db/migrations"), "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v %v", files, err)
	}
	var wg sync.WaitGroup
	applied := make([][]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		st, err := store.Open(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied[i], errs[i] = st.Migrate(ctx, os.DirFS("../../db/migrations"))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Migrate %d: %v", i, err)
		}
	}
	all := append(slices.Clone(applied[0]), applied[1]...)
	slices.Sort(all)
	if len(all) != len(files) || len(slices.Compact(slices.Clone(all))) != len(files) {
		t.Errorf("applied %v and %v; want each of the %d migrations once", applied[0], applied[1], len(files))
	}
	if len(applied[0]) != 0 && len(applied[1]) != 0 {
		t.Errorf("both applied migrations (%d, %d): the lock did not serialise them", len(applied[0]), len(applied[1]))
	}
	st, _ := store.Open(ctx, url)
	defer st.Close()
	var rows, versions int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT version_id) FROM goose_db_version WHERE version_id > 0`).Scan(&rows, &versions); err != nil {
		t.Fatal(err)
	}
	if rows != len(files) || versions != len(files) {
		t.Errorf("goose_db_version: %d rows, %d versions; want %d", rows, versions, len(files))
	}
}

// TestRecomputeViaFollower: a recompute posted to the follower is stored, picked up by the leader
// within one poll and run there; the response is the usual 202.
func TestRecomputeViaFollower(t *testing.T) {
	url := newHADB(t)
	o := haOpts{interval: time.Hour, retry: 500 * time.Millisecond, check: 500 * time.Millisecond}
	a := startHAHub(t, url, "hub-a", o)
	b := startHAHub(t, url, "hub-b", o)
	waitFor(t, 10*time.Second, "a leader", func() bool { l, _ := leaderOf(a, b); return l != nil })
	leader, follower := leaderOf(a, b)
	waitFor(t, 10*time.Second, "the start run to finish", func() bool {
		var done bool
		err := a.st.Pool.QueryRow(context.Background(), `SELECT count(*) > 0 AND bool_and(status <> 'running') FROM recommender_runs WHERE NOT backfill`).Scan(&done)
		return err == nil && done
	})
	before := leader.calls()

	r := follower.post(t, "/v1/admin/recompute?cluster=c1", tokAdmin)
	if r.code != http.StatusAccepted {
		t.Fatalf("recompute via follower: %d %s", r.code, r.body)
	}
	var acc hubapi.RecomputeAccepted
	decode(t, r.body, &acc)
	if acc.Cluster != "c1" || acc.Coalesced || acc.RunID == "" || acc.RequestedAt.IsZero() {
		t.Errorf("accepted: %+v", acc)
	}
	var status string
	took := waitFor(t, 5*time.Second, "the leader to run the request", func() bool {
		err := a.st.Pool.QueryRow(context.Background(), `SELECT status FROM recommender_runs WHERE id = $1`, acc.RunID).Scan(&status)
		return err == nil && status != "running"
	})
	t.Logf("leader ran the follower's request %s after it was posted", took.Round(10*time.Millisecond))
	if status != "succeeded" {
		t.Errorf("run status %q", status)
	}
	follower.job.WaitTriggered()
	var errMsg string
	var finished *time.Time
	if err := a.st.Pool.QueryRow(context.Background(), `SELECT finished_at, error FROM recompute_requests WHERE id = $1`, acc.RunID).Scan(&finished, &errMsg); err != nil || finished == nil || errMsg != "" {
		t.Errorf("request row: finished %v error %q %v", finished, errMsg, err)
	}
	if leader.calls() != before+1 || follower.calls() != 0 {
		t.Errorf("loads: leader %d -> %d, follower %d; want the leader to run c1 once", before, leader.calls(), follower.calls())
	}
	e := &testEnv{t: t, ctx: context.Background(), st: a.st}
	if got := e.runClusters(acc.RunID); !slices.Equal(got, []string{"c1"}) {
		t.Errorf("run clusters %v", got)
	}
}

// TestLeaderInterruptedRecompute: a recompute the old leader was running when it lost the lock is
// marked failed and queued again; the new leader runs it.
func TestLeaderInterruptedRecompute(t *testing.T) {
	url := newHADB(t)
	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	if _, err := st.Pool.Exec(ctx, `INSERT INTO recommender_runs (id, started_at, as_of, status) VALUES ('0192b2b4-0000-7000-8000-000000000001', now(), now(), 'running');
		INSERT INTO recompute_requests (id, cluster, requested_at, requested_by, started_at) VALUES ('0192b2b4-0000-7000-8000-000000000001', 'c2', '`+at.Format(time.RFC3339)+`', 'admin', now())`); err != nil {
		t.Fatal(err)
	}
	h := startHAHub(t, url, "hub-a", haOpts{interval: time.Hour, retry: 500 * time.Millisecond, check: 500 * time.Millisecond})
	waitFor(t, 10*time.Second, "leadership", h.elector.IsLeader)
	h.job.WaitTriggered()
	var oldStatus string
	if err := st.Pool.QueryRow(ctx, `SELECT status FROM recommender_runs WHERE id = '0192b2b4-0000-7000-8000-000000000001'`).Scan(&oldStatus); err != nil || oldStatus != "failed" {
		t.Errorf("interrupted run: %q %v", oldStatus, err)
	}
	var n int
	var ok bool
	if err := st.Pool.QueryRow(ctx, `SELECT count(*), bool_and(finished_at IS NOT NULL) FROM recompute_requests WHERE cluster = 'c2' AND requested_at = $1`, at).Scan(&n, &ok); err != nil || n != 2 || !ok {
		t.Errorf("requests for c2: %d (all finished %v) %v; want the interrupted one and its re-run", n, ok, err)
	}
}

// TestLeaderFollowerTokenRevocation: a token revoked through one replica stops working on the
// other at once, although that one cached it.
func TestLeaderFollowerTokenRevocation(t *testing.T) {
	url := newHADB(t)
	o := haOpts{interval: time.Hour, retry: 500 * time.Millisecond, check: 500 * time.Millisecond}
	a := startHAHub(t, url, "hub-a", o)
	b := startHAHub(t, url, "hub-b", o)
	waitFor(t, 10*time.Second, "listening", func() bool { return a.elector.Listening() && b.elector.Listening() })
	if r := b.get(t, "/v1/clusters/c2/recommendations", tokC2); r.code != 200 { // cached on b
		t.Fatalf("before: %d", r.code)
	}
	var id int64
	if err := a.st.Pool.QueryRow(context.Background(), `SELECT id FROM api_tokens WHERE token_sha256 = $1`, store.HashToken(tokC2)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodDelete, a.ts.URL+"/v1/admin/tokens/"+strconv.FormatInt(id, 10), strings.NewReader(`{"reason":"test"}`))
	req.Header.Set("Authorization", "Bearer "+tokAdmin)
	req.Header.Set("Content-Type", "application/json")
	if r := doReq(t, req); r.code != 200 {
		t.Fatalf("revoke: %d %s", r.code, r.body)
	}
	waitFor(t, 2*time.Second, "the revocation on the other replica", func() bool {
		return b.get(t, "/v1/clusters/c2/recommendations", tokC2).code == http.StatusUnauthorized
	})
}
