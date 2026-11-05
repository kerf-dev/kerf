package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Integration tests run against Postgres when KERF_TEST_DATABASE_URL is set to a superuser URL,
// e.g. postgres://kerf:kerf@localhost:15432/postgres?sslmode=disable. Every test creates its own
// database kerf_hub_test_<random> and drops it at the end.

const testDBPrefix = "kerf_hub_test_"

// newTestStore creates a fresh migrated database and returns a store on it.
func newTestStore(t *testing.T, createOpts string) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, newTestDB(t, createOpts))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Migrate(ctx, os.DirFS("../../db/migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// newTestDB creates a fresh empty database, dropped at the end of the test, and returns its URL.
func newTestDB(t *testing.T, createOpts string) string {
	t.Helper()
	base := os.Getenv("KERF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KERF_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := testDBPrefix + hex.EncodeToString(b)
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect %s: %v", u.Redacted(), err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" "+createOpts); err != nil {
		admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		admin.Close(ctx)
	})
	u.Path = "/" + name
	return u.String()
}

// ---- fake recommender loader ----

type ctrSpec struct {
	name           string
	cpu, mem       float64 // mean usage (cores, bytes)
	curCPU, curMem float64 // current requests (cores, bytes)
	cpuLim         float64
	oom            bool
	noData         bool
}

type wlSpec struct {
	key        hubapi.WorkloadKey
	containers []ctrSpec
	replicas   float64
	days       float64 // days of samples before end
	hpa        *recommender.HPA
}

// fakeLoader returns synthetic cluster data. Current requests grow by 50 % per day before
// fakeLoader.now, so runs as of different times have different savings totals.
type fakeLoader struct {
	mu        sync.Mutex
	now       time.Time
	workloads map[string][]wlSpec
	hidden    map[string]bool // workload ids missing from the inventory
	empty     map[string]bool // clusters returning no workloads at all
	noReqs    map[string]bool // workload ids without request series (HasRequests false)
	gone      map[string]bool // workload ids with history but no pods now (Live false)
	// noPricing: no pricing series at the window end; pricingNow (if set) is what PricingAt returns.
	noPricing  bool
	pricingNow *recommender.Pricing
	pricingAt  []time.Time
	calls      int
}

func (l *fakeLoader) hide(id string, v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hidden == nil {
		l.hidden = map[string]bool{}
	}
	l.hidden[id] = v
}

// PricingAt implements the job's pricing fallback for backfills.
func (l *fakeLoader) PricingAt(_ context.Context, _ string, t time.Time) (recommender.Pricing, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pricingAt = append(l.pricingAt, t)
	if l.pricingNow == nil {
		return recommender.Pricing{}, false, nil
	}
	return *l.pricingNow, true, nil
}

func (l *fakeLoader) setEmpty(cluster string, v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.empty == nil {
		l.empty = map[string]bool{}
	}
	l.empty[cluster] = v
}

const fakeStep = 5 * time.Minute

func (l *fakeLoader) Load(_ context.Context, cluster string, end time.Time, window time.Duration) (*recommender.ClusterData, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	end = end.Truncate(fakeStep)
	d := &recommender.ClusterData{
		Cluster: cluster, End: end, Window: window, Workloads: map[hubapi.WorkloadKey]*recommender.RawWorkload{},
		Pricing: recommender.Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004, Source: recommender.PricingSourceMetrics, Nodes: 3},
	}
	if l.noPricing {
		d.Pricing = recommender.Pricing{CPUCoreHour: 0.02, MemoryGiBHour: 0.002, Source: recommender.PricingSourceDefault}
	}
	if l.empty[cluster] {
		return d, nil
	}
	factor := 1 + 0.5*math.Floor(l.now.Sub(end).Hours()/24)
	for _, w := range l.workloads[cluster] {
		if l.hidden[w.key.ID()] {
			continue
		}
		n := int(w.days * 24 * float64(time.Hour/fakeStep))
		rw := &recommender.RawWorkload{Key: w.key, Containers: map[string]*recommender.RawContainer{}, HasRequests: !l.noReqs[w.key.ID()], HPA: w.hpa,
			Live: !l.noReqs[w.key.ID()] && !l.gone[w.key.ID()]}
		for i := 0; i < n; i++ {
			rw.Pods = append(rw.Pods, recommender.Sample{T: end.Add(-time.Duration(n-1-i) * fakeStep), V: w.replicas})
		}
		for _, c := range w.containers {
			rc := &recommender.RawContainer{
				Name: c.name, OOMKilled: c.oom,
				Current:    recommender.Current{CPURequest: c.curCPU * factor, MemoryRequest: c.curMem * factor, CPULimit: c.cpuLim},
				Used24hCPU: c.cpu * w.replicas, Used24hMemory: c.mem * w.replicas,
			}
			if !c.noData {
				for i := 0; i < n; i++ {
					ts := end.Add(-time.Duration(n-1-i) * fakeStep)
					rc.CPU = append(rc.CPU, recommender.Sample{T: ts, V: c.cpu * (1 + 0.2*math.Sin(float64(i)/12))})
					rc.Memory = append(rc.Memory, recommender.Sample{T: ts, V: c.mem * (1 + 0.05*math.Sin(float64(i)/20))})
				}
			}
			rw.Containers[c.name] = rc
		}
		d.Workloads[w.key] = rw
	}
	return d, nil
}

func key(cluster, ns, kind, name string) hubapi.WorkloadKey {
	return hubapi.WorkloadKey{Cluster: cluster, Namespace: ns, Kind: kind, Name: name}
}

var (
	kCheckout = key("c1", "payments", hubapi.KindDeployment, "checkout")
	kLedger   = key("c1", "payments", hubapi.KindStatefulSet, "ledger")
	kFrontend = key("c1", "web", hubapi.KindDeployment, "frontend")
	kReport   = key("c1", "batch", hubapi.KindCronJob, "report")
	kLegacy   = key("c1", "web", hubapi.KindDeployment, "legacy")
	kAPI      = key("c2", "payments", hubapi.KindDeployment, "api")
)

const (
	mi = recommender.MiB
	gi = recommender.GiB
)

func defaultWorkloads() map[string][]wlSpec {
	return map[string][]wlSpec{
		"c1": {
			{key: kCheckout, replicas: 3, days: 8, containers: []ctrSpec{
				{name: "app", cpu: 0.2, mem: 200 * mi, curCPU: 1, curMem: 1 * gi, cpuLim: 2},
			}},
			{key: kLedger, replicas: 1, days: 8, containers: []ctrSpec{
				{name: "app", cpu: 0.4, mem: 400 * mi, curCPU: 0.5, curMem: 512 * mi, oom: true},
			}},
			{key: kFrontend, replicas: 2, days: 1, containers: []ctrSpec{
				{name: "nginx", cpu: 0.05, mem: 50 * mi, curCPU: 0.1, curMem: 128 * mi},
			}},
			{key: kReport, replicas: 1, days: 8, containers: []ctrSpec{
				{name: "job", noData: true, curCPU: 1, curMem: 1 * gi},
			}},
			{key: kLegacy, replicas: 1, days: 8, containers: []ctrSpec{
				{name: "app", cpu: 0.1, mem: 100 * mi, curCPU: 0.2, curMem: 256 * mi},
			}},
		},
		"c2": {
			{key: kAPI, replicas: 2, days: 8, containers: []ctrSpec{
				{name: "app", cpu: 0.1, mem: 100 * mi, curCPU: 0.25, curMem: 512 * mi},
			}},
		},
	}
}

// ---- fake PromQL querier for the usage endpoint ----

type fakeQuerier struct {
	mu      sync.Mutex
	queries []string
	err     error
	// initSeries makes init-container resource queries return series too.
	initSeries bool
}

func (q *fakeQuerier) Instant(context.Context, string, time.Time) ([]recommender.VectorSample, error) {
	return nil, nil
}

func (q *fakeQuerier) Range(_ context.Context, query string, start, end time.Time, step time.Duration) ([]recommender.Series, error) {
	q.mu.Lock()
	q.queries = append(q.queries, query)
	q.mu.Unlock()
	if q.err != nil {
		return nil, q.err
	}
	pts := func(v float64) []recommender.Sample {
		var out []recommender.Sample
		for t := start; !t.After(end) && len(out) < 5; t = t.Add(step) {
			out = append(out, recommender.Sample{T: t, V: v})
		}
		return out
	}
	lbl := func(wl, container, resource string) recommender.Labels {
		l := recommender.Labels{"cluster": "c1", "namespace": "payments", "workload_kind": "Deployment", "workload": wl, "container": container}
		if resource != "" {
			l["resource"] = resource
		}
		return l
	}
	isInit := strings.Contains(query, "kube_pod_init_container_resource_")
	switch {
	case isInit && !q.initSeries:
		return nil, nil
	case isInit:
		return []recommender.Series{{Labels: lbl("checkout", "init-proxy", "cpu"), Samples: pts(0.1)}}, nil
	case strings.Contains(query, "kube_pod_container_resource_requests"):
		return []recommender.Series{
			{Labels: lbl("checkout", "app", "cpu"), Samples: pts(1)},
			{Labels: lbl("checkout", "app", "memory"), Samples: pts(1 * gi)},
			{Labels: lbl("other", "app", "cpu"), Samples: pts(99)},
		}, nil
	case strings.Contains(query, "kube_pod_container_resource_limits"):
		return []recommender.Series{
			{Labels: lbl("checkout", "app", "cpu"), Samples: pts(2)},
			{Labels: lbl("other", "app", "cpu"), Samples: pts(99)},
		}, nil
	case strings.Contains(query, "avg by (cluster, namespace, workload_kind, workload, container)"):
		return []recommender.Series{
			{Labels: lbl("checkout", "app", ""), Samples: pts(0.15)},
			{Labels: lbl("other", "app", ""), Samples: pts(99)},
			{Labels: recommender.Labels{"cluster": "c1", "namespace": "payments", "workload_kind": "StatefulSet", "workload": "checkout", "container": "app"}, Samples: pts(98)},
		}, nil
	default:
		return []recommender.Series{
			{Labels: lbl("checkout", "app", ""), Samples: pts(0.3)},
			{Labels: lbl("other", "app", ""), Samples: pts(99)},
		}, nil
	}
}

// ---- environment ----

type testEnv struct {
	mu     sync.Mutex
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	srv    *Server
	job    *Job
	loader *fakeLoader
	prom   *fakeQuerier
	ts     *httptest.Server
	now    time.Time
	// backfillAsOf are the as-of times of the two backfill runs, oldest first.
	backfillAsOf []time.Time
}

const (
	tokC1    = "test-agent-c1"
	tokC2    = "test-agent-c2"
	tokUI    = "test-ui"
	tokAdmin = "test-admin"
)

// newEnv creates a database with clusters c1 and c2, tokens, two backfill runs (now-48h and
// now-24h) and one regular run as of now.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st := newTestStore(t, "")
	ctx := context.Background()
	// Not aligned to the 5 m step, so generatedAt (aligned) is strictly before now.
	now := time.Now().UTC().Truncate(time.Second)
	if now.Truncate(fakeStep).Equal(now) {
		now = now.Add(time.Second)
	}
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
	cfg := DefaultConfig()
	cfg.DatabaseURL = "unused"
	cfg.CORSOrigins = []string{"http://localhost:5173"}
	recCfg, err := cfg.RecommenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, ctx: ctx, st: st, now: now}
	clock := func() time.Time {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.now
	}
	metrics := NewMetrics()
	loader := &fakeLoader{now: now, workloads: defaultWorkloads()}
	prom := &fakeQuerier{}
	srv := &Server{Cfg: cfg, Store: st, Prom: prom, RecCfg: recCfg, Metrics: metrics, Log: discardLog(), Now: clock}
	job := &Job{
		Store: st, Metrics: metrics, Cfg: cfg, Log: discardLog(), Now: clock,
		Engine: &recommender.Engine{Loader: loader, Cfg: recCfg, Strategy: recommender.PercentileStrategy{}},
	}
	srv.Job = job
	e.srv, e.job, e.loader, e.prom = srv, job, loader, prom
	for i := 2; i >= 1; i-- {
		asOf := now.Add(-time.Duration(i) * 24 * time.Hour)
		e.run(asOf, true)
		e.backfillAsOf = append(e.backfillAsOf, asOf.Truncate(fakeStep))
	}
	e.run(now, false)
	// Recompute requests are queued in the database; the (single, leading) test hub runs them.
	rctx, rcancel := context.WithCancel(ctx)
	rdone := make(chan struct{})
	go func() { defer close(rdone); job.RecomputeLoop(rctx) }()
	t.Cleanup(func() { rcancel(); <-rdone })
	srv.SetReady()
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

// advance moves the clock (server, job and the loader's notion of now) forward by d.
func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
	e.loader.mu.Lock()
	e.loader.now = e.loader.now.Add(d)
	e.loader.mu.Unlock()
}

func (e *testEnv) run(asOf time.Time, backfill bool) {
	e.t.Helper()
	if err := e.job.RunOnce(e.ctx, asOf, backfill); err != nil {
		e.t.Fatalf("RunOnce(%s, backfill=%v): %v", asOf, backfill, err)
	}
}

type resp struct {
	code   int
	header http.Header
	body   []byte
}

func (e *testEnv) do(method, path, token string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		case []byte:
			rd = bytes.NewReader(b)
		default:
			j, err := json.Marshal(body)
			if err != nil {
				e.t.Fatal(err)
			}
			rd = bytes.NewReader(j)
		}
	}
	req, err := http.NewRequestWithContext(e.ctx, method, e.ts.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp{code: res.StatusCode, header: res.Header, body: b}
}

// getJSON GETs path, expects 200 and decodes into v.
func (e *testEnv) getJSON(path, token string, v any) resp {
	e.t.Helper()
	r := e.do(http.MethodGet, path, token, nil)
	if r.code != http.StatusOK {
		e.t.Fatalf("GET %s: %d %s", path, r.code, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		e.t.Errorf("GET %s: Content-Type %q", path, ct)
	}
	if err := json.Unmarshal(r.body, v); err != nil {
		e.t.Fatalf("GET %s: decode %s: %v", path, r.body, err)
	}
	return r
}

func (e *testEnv) postJSON(path, token string, body, v any) {
	e.t.Helper()
	r := e.do(http.MethodPost, path, token, body)
	if r.code != http.StatusOK {
		e.t.Fatalf("POST %s: %d %s", path, r.code, r.body)
	}
	if err := json.Unmarshal(r.body, v); err != nil {
		e.t.Fatalf("POST %s: decode %s: %v", path, r.body, err)
	}
}

func (e *testEnv) problem(r resp, status int, code string) hubapi.Problem {
	e.t.Helper()
	return checkProblem(e.t, r.code, r.header, r.body, status, code)
}

func (e *testEnv) metrics() metricSet {
	e.t.Helper()
	r := e.do(http.MethodGet, "/metrics", "", nil)
	if r.code != 200 {
		e.t.Fatalf("/metrics: %d", r.code)
	}
	return parseMetrics(string(r.body))
}

func wlPath(k hubapi.WorkloadKey, suffix string) string {
	return "/v1/workloads/" + k.EscapedID() + suffix
}

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func ptrStr(p *time.Time) string {
	if p == nil {
		return "<nil>"
	}
	return p.String()
}

func mustParseCPU(t *testing.T, s string) float64 {
	t.Helper()
	v, err := recommender.ParseCPU(s)
	if err != nil {
		t.Fatalf("ParseCPU(%q): %v", s, err)
	}
	return v
}

func newUUID(i int) string { return fmt.Sprintf("0193a1b2-c3d4-7e5f-8a9b-%012d", i) }
