package recommender

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// fakeQuerier routes by query text. A query present in a map (even with a nil result) is known;
// any other query returns an empty result and is recorded in unknown.
type fakeQuerier struct {
	mu      sync.Mutex
	instant map[string][]VectorSample
	rng     map[string][]Series
	fail    map[string]error
	unknown []string
	// called maps every issued query to the start of its range (instant: the evaluation time).
	called map[string]time.Time
	// ends / steps record the end time and step of range calls.
	ends  map[string]time.Time
	steps map[string]time.Duration
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{
		instant: map[string][]VectorSample{}, rng: map[string][]Series{}, fail: map[string]error{},
		called: map[string]time.Time{}, ends: map[string]time.Time{}, steps: map[string]time.Duration{},
	}
}

func (f *fakeQuerier) Instant(_ context.Context, query string, t time.Time) ([]VectorSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called[query] = t
	if err := f.fail[query]; err != nil {
		return nil, err
	}
	r, ok := f.instant[query]
	if !ok {
		f.unknown = append(f.unknown, query)
	}
	return r, nil
}

func (f *fakeQuerier) Range(_ context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called[query] = start
	f.ends[query] = end
	f.steps[query] = step
	if err := f.fail[query]; err != nil {
		return nil, err
	}
	r, ok := f.rng[query]
	if !ok {
		f.unknown = append(f.unknown, query)
	}
	return r, nil
}

// wl returns the labels of a workload container; extra are key/value pairs.
func wl(ns, kind, name, container string, extra ...string) Labels {
	l := Labels{"cluster": "c1", "namespace": ns, "workload_kind": kind, "workload": name}
	if container != "" {
		l["container"] = container
	}
	for i := 0; i+1 < len(extra); i += 2 {
		l[extra[i]] = extra[i+1]
	}
	return l
}

func vs(l Labels, v float64) VectorSample {
	return VectorSample{Labels: l, Sample: Sample{T: testEnd, V: v}}
}

var (
	keyWeb    = hubapi.WorkloadKey{Cluster: "c1", Namespace: "ns1", Kind: hubapi.KindDeployment, Name: "web"}
	keyDB     = hubapi.WorkloadKey{Cluster: "c1", Namespace: "ns1", Kind: hubapi.KindStatefulSet, Name: "db"}
	keyAgent  = hubapi.WorkloadKey{Cluster: "c1", Namespace: "kube-system", Kind: hubapi.KindDaemonSet, Name: "agent"}
	keyReport = hubapi.WorkloadKey{Cluster: "c1", Namespace: "ns2", Kind: hubapi.KindCronJob, Name: "report"}
)

// sourceQueries are the query strings Source.Load must issue for cluster c1 with cfg.
type sourceQueries struct {
	cpu, mem, pods, cpu24h, mem24h, throttle string
	oom, restarts, init, hpa, hpaMetrics     string
	images                                   string
	hpaMin, hpaMax, hpaPeak                  string
	priceCPU, priceMem, priceNodes           string
	priceSources, curStart, curEnd           string
	// res[metric][init][lookback>0]
	res map[string]map[bool]map[bool]string
}

func buildSourceQueries(cfg Config) sourceQueries {
	q := Queries{Cluster: "c1", RateWindow: cfg.RateWindow}
	sq := sourceQueries{
		cpu:        q.CPUUsage("max", nil),
		mem:        q.MemoryUsage("max", nil, cfg.Step),
		pods:       q.RunningPods(cfg.Step),
		cpu24h:     q.CPUUsage("sum", nil),
		mem24h:     q.MemoryUsage("sum", nil, cfg.Step),
		throttle:   q.ThrottleRatio(),
		oom:        q.OOMKills(cfg.RiskWindow),
		restarts:   q.Restarts(cfg.RiskWindow),
		init:       q.InitContainers(cfg.InventoryLookback),
		hpa:        q.HPAInfo(cfg.InventoryLookback),
		hpaMetrics: q.HPAMetrics(cfg.InventoryLookback),
		images:     q.ContainerImages(cfg.InventoryLookback),
		hpaMin:     q.HPAReplicaBounds("min", cfg.InventoryLookback),
		hpaMax:     q.HPAReplicaBounds("max", cfg.InventoryLookback),
		hpaPeak:    q.HPAPeakReplicas(14 * 24 * time.Hour),
		res:        map[string]map[bool]map[bool]string{},
	}
	sq.priceCPU, sq.priceNodes = q.Pricing("kerf_cpu_core_hourly_cost_usd", time.Hour)
	sq.priceSources, sq.curStart, sq.curEnd = q.PricingSources(), PricingCURPeriod("start"), PricingCURPeriod("end")
	sq.priceMem, _ = q.Pricing("kerf_memory_gib_hourly_cost_usd", time.Hour)
	for _, m := range []string{"requests", "limits"} {
		sq.res[m] = map[bool]map[bool]string{}
		for _, init := range []bool{false, true} {
			sq.res[m][init] = map[bool]string{
				false: q.Resources(m, init, 0, nil),
				true:  q.Resources(m, init, cfg.InventoryLookback, nil),
			}
		}
	}
	return sq
}

// sourceFixture builds a fake with four workloads:
//   - ns1/Deployment/web: main container "app", native sidecar "istio-proxy" (full coverage),
//     classic init container "setup" (10 samples), HPA on cpu utilization, OOM-killed and restarting;
//   - ns1/StatefulSet/db: "postgres", OOM query returns 0;
//   - kube-system/DaemonSet/agent: "agent", memory request only in the lookback query;
//   - ns2/CronJob/report: "job", found only by the lookback inventory query, no usage.
//
// It also returns series that must be ignored (invalid kind, missing labels).
func sourceFixture(cfg Config) (*fakeQuerier, sourceQueries) {
	sq := buildSourceQueries(cfg)
	f := newFakeQuerier()
	app := wl("ns1", hubapi.KindDeployment, "web", "app")
	istio := wl("ns1", hubapi.KindDeployment, "web", "istio-proxy")
	setup := wl("ns1", hubapi.KindDeployment, "web", "setup")
	pg := wl("ns1", hubapi.KindStatefulSet, "db", "postgres")
	agent := wl("kube-system", hubapi.KindDaemonSet, "agent", "agent")
	job := wl("ns2", hubapi.KindCronJob, "report", "job")
	bogus := []Series{
		{Labels: wl("ns1", "ReplicaSet", "web-abc", "app"), Samples: flat(14, 1, 0, 90)},
		{Labels: wl("", hubapi.KindDeployment, "nons", "app"), Samples: flat(14, 1, 0, 91)},
		{Labels: wl("ns1", hubapi.KindDeployment, "", "app"), Samples: flat(14, 1, 0, 92)},
		{Labels: wl("ns1", hubapi.KindDeployment, "web", ""), Samples: flat(14, 1, 0, 93)},
	}
	with := func(l Labels, k, v string) Labels {
		out := Labels{k: v}
		for a, b := range l {
			out[a] = b
		}
		return out
	}
	cpu, mem := func(l Labels) Labels { return with(l, "resource", "cpu") }, func(l Labels) Labels { return with(l, "resource", "memory") }

	f.rng[sq.cpu] = append([]Series{
		{Labels: app, Samples: flat(14, 0.5, 0.1, 1)},
		{Labels: istio, Samples: flat(14, 0.05, 0.1, 2)},
		{Labels: setup, Samples: flat(14, 0.2, 0, 3)[:10]},
		{Labels: pg, Samples: flat(14, 1, 0.1, 4)},
		{Labels: agent, Samples: flat(14, 0.1, 0.1, 5)},
	}, bogus...)
	f.rng[sq.mem] = append([]Series{
		{Labels: app, Samples: flat(14, 200*mi, 0.1, 11)},
		{Labels: istio, Samples: flat(14, 40*mi, 0.1, 12)},
		{Labels: setup, Samples: flat(14, 10*mi, 0, 13)[:10]},
		{Labels: pg, Samples: flat(14, 2048*mi, 0.05, 14)},
		{Labels: agent, Samples: flat(14, 50*mi, 0.1, 15)},
	}, bogus...)
	f.rng[sq.pods] = []Series{
		{Labels: wl("ns1", hubapi.KindDeployment, "web", ""), Samples: podsConst(14, 3)},
		{Labels: wl("ns1", hubapi.KindStatefulSet, "db", ""), Samples: podsConst(14, 2)},
		{Labels: wl("kube-system", hubapi.KindDaemonSet, "agent", ""), Samples: podsConst(14, 4)},
		{Labels: wl("ns1", "Job", "x", ""), Samples: podsConst(14, 9)},
	}
	f.rng[sq.cpu24h] = []Series{{Labels: app, Samples: flat(1, 1.5, 0, 6)}, {Labels: istio, Samples: flat(1, 0.15, 0, 7)}}
	f.rng[sq.mem24h] = []Series{{Labels: app, Samples: flat(1, 600*mi, 0, 8)}}
	f.rng[sq.throttle] = []Series{{Labels: app, Samples: []Sample{{T: testEnd.Add(-time.Hour), V: 0.1}, {T: testEnd, V: 0.3}}}}

	f.instant[sq.oom] = []VectorSample{vs(app, 1), vs(pg, 0)}
	f.instant[sq.restarts] = []VectorSample{vs(app, 4), vs(pg, 0)}
	f.instant[sq.res["requests"][false][false]] = []VectorSample{
		vs(cpu(app), 1), vs(mem(app), 512*mi),
		vs(cpu(pg), 2), vs(mem(pg), 4096*mi),
		vs(cpu(agent), 0.1),
	}
	f.instant[sq.res["requests"][false][true]] = []VectorSample{
		vs(cpu(app), 2), vs(mem(app), 1024*mi), // stale: the live value wins
		vs(cpu(job), 0.25), vs(mem(job), 128*mi), // CronJob between runs
		vs(mem(agent), 64*mi), // no live memory request
		vs(cpu(pg), 3),
	}
	f.instant[sq.res["limits"][false][false]] = []VectorSample{vs(mem(app), 512*mi), vs(cpu(agent), 0.2)}
	f.instant[sq.res["limits"][false][true]] = []VectorSample{vs(mem(app), 2048*mi), vs(mem(job), 256*mi)}
	f.instant[sq.res["requests"][true][false]] = []VectorSample{vs(cpu(istio), 0.1)}
	f.instant[sq.res["requests"][true][true]] = nil
	f.instant[sq.res["limits"][true][false]] = nil
	f.instant[sq.res["limits"][true][true]] = nil
	f.instant[sq.init] = []VectorSample{vs(istio, 1), vs(setup, 1)}
	img := func(l Labels, image string) VectorSample {
		c := Labels{"image": image}
		for k, v := range l {
			c[k] = v
		}
		return vs(c, 1)
	}
	f.instant[sq.images] = []VectorSample{
		img(app, "registry/web:1.2.0-temurin21-jre"), img(app, "registry/web:1.1.0-temurin21-jre"), img(app, "registry/web:1.2.0-temurin21-jre"),
		img(pg, "postgres:17"),
		img(wl("ns1", hubapi.KindDeployment, "gone", "app"), "openjdk:17"), // not in the inventory
		img(wl("ns1", hubapi.KindDeployment, "web", ""), "x"),              // no container label
	}
	hpaL := func(name, kind, target string, extra ...string) Labels {
		l := Labels{"cluster": "c1", "namespace": "ns1", "horizontalpodautoscaler": name, "scaletargetref_kind": kind, "scaletargetref_name": target}
		for i := 0; i+1 < len(extra); i += 2 {
			l[extra[i]] = extra[i+1]
		}
		return l
	}
	f.instant[sq.hpa] = []VectorSample{vs(hpaL("web-hpa", "Deployment", "web"), 1), vs(hpaL("orphan", "Deployment", "gone"), 1)}
	f.instant[sq.hpaMetrics] = []VectorSample{
		vs(hpaL("web-hpa", "", "", "metric_name", "http_requests", "metric_target_type", "averagevalue"), 1),
		vs(hpaL("web-hpa", "", "", "metric_name", "cpu", "metric_target_type", "utilization"), 60),
		vs(hpaL("orphan", "", "", "metric_name", "memory", "metric_target_type", "utilization"), 70),
	}
	f.instant[sq.hpaMin] = []VectorSample{vs(hpaL("web-hpa", "", ""), 2), vs(hpaL("orphan", "", ""), 1)}
	f.instant[sq.hpaMax] = []VectorSample{vs(hpaL("web-hpa", "", ""), 6), vs(hpaL("orphan", "", ""), 3)}
	f.instant[sq.hpaPeak] = []VectorSample{vs(hpaL("web-hpa", "", ""), 4)}
	f.instant[sq.priceCPU] = []VectorSample{vs(Labels{"cluster": "c1"}, 0.03)}
	f.instant[sq.priceMem] = []VectorSample{vs(Labels{"cluster": "c1"}, 0.004)}
	f.instant[sq.priceNodes] = []VectorSample{vs(Labels{"cluster": "c1"}, 5)}
	f.instant[sq.priceSources] = []VectorSample{vs(Labels{"source": "list"}, 1), vs(Labels{"source": "cur"}, 4)}
	f.instant[sq.curStart] = []VectorSample{vs(Labels{}, float64(curPeriodStart.Unix()))}
	f.instant[sq.curEnd] = []VectorSample{vs(Labels{}, float64(curPeriodEnd.Unix()))}
	return f, sq
}

var (
	curPeriodStart = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	curPeriodEnd   = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
)

func TestSourceLoad(t *testing.T) {
	cfg := DefaultConfig()
	window := 14 * 24 * time.Hour
	boom := errors.New("boom")
	cases := []struct {
		name    string
		mutate  func(f *fakeQuerier, sq sourceQueries)
		wantErr string
		check   func(t *testing.T, d *ClusterData)
	}{
		{
			name: "workloads of every kind",
			check: func(t *testing.T, d *ClusterData) {
				want := []hubapi.WorkloadKey{keyAgent, keyDB, keyWeb, keyReport}
				sort.Slice(want, func(i, j int) bool { return want[i].ID() < want[j].ID() })
				if got := d.SortedKeys(); !reflect.DeepEqual(got, want) {
					t.Fatalf("workloads = %v, want %v", got, want)
				}
				containers := map[hubapi.WorkloadKey][]string{
					keyWeb: {"app", "istio-proxy", "setup"}, keyDB: {"postgres"}, keyAgent: {"agent"}, keyReport: {"job"},
				}
				for k, names := range containers {
					w := d.Workloads[k]
					var got []string
					for n := range w.Containers {
						got = append(got, n)
					}
					sort.Strings(got)
					if !reflect.DeepEqual(got, names) {
						t.Errorf("%s containers = %v, want %v", k.ID(), got, names)
					}
					if !w.HasRequests {
						t.Errorf("%s HasRequests = false", k.ID())
					}
					// Live: request/limit series at the window end; the CronJob between runs has
					// only the lookback ones.
					if want := k != keyReport; w.Live != want {
						t.Errorf("%s Live = %v, want %v", k.ID(), w.Live, want)
					}
				}
				web := d.Workloads[keyWeb]
				if app := web.Containers["app"]; len(app.CPU) != 14*288 || len(app.Memory) != 14*288 {
					t.Errorf("app samples cpu=%d mem=%d, want %d", len(app.CPU), len(app.Memory), 14*288)
				}
				if len(web.Pods) != 14*288 || len(d.Workloads[keyAgent].Pods) != 14*288 {
					t.Errorf("pods series missing")
				}
				if got := web.Containers["app"].Used24hCPU; math.Abs(got-1.5) > 1e-9 {
					t.Errorf("app Used24hCPU = %v, want 1.5", got)
				}
				if got := web.Containers["app"].Used24hMemory; math.Abs(got-600*mi) > 1 {
					t.Errorf("app Used24hMemory = %v, want 600Mi", got)
				}
				if got := web.Containers["app"].Throttle; len(got) != 2 {
					t.Errorf("app throttle = %v", got)
				}
				if got := d.Workloads[keyReport].Containers["job"]; len(got.CPU) != 0 || len(got.Memory) != 0 {
					t.Errorf("cronjob has usage: %+v", got)
				}
				if d.Cluster != "c1" || !d.End.Equal(testEnd) || d.Window != window {
					t.Errorf("cluster/end/window = %s %s %s", d.Cluster, d.End, d.Window)
				}
			},
		},
		{
			name: "current requests and limits: live wins over lookback",
			check: func(t *testing.T, d *ClusterData) {
				want := map[string]Current{
					"web/app":     {CPURequest: 1, MemoryRequest: 512 * mi, MemoryLimit: 512 * mi},
					"web/istio":   {CPURequest: 0.1},
					"db/postgres": {CPURequest: 2, MemoryRequest: 4096 * mi},
					"agent/agent": {CPURequest: 0.1, CPULimit: 0.2, MemoryRequest: 64 * mi},
					"report/job":  {CPURequest: 0.25, MemoryRequest: 128 * mi, MemoryLimit: 256 * mi},
					"web/setup":   {},
				}
				got := map[string]Current{
					"web/app":     d.Workloads[keyWeb].Containers["app"].Current,
					"web/istio":   d.Workloads[keyWeb].Containers["istio-proxy"].Current,
					"db/postgres": d.Workloads[keyDB].Containers["postgres"].Current,
					"agent/agent": d.Workloads[keyAgent].Containers["agent"].Current,
					"report/job":  d.Workloads[keyReport].Containers["job"].Current,
					"web/setup":   d.Workloads[keyWeb].Containers["setup"].Current,
				}
				for k, w := range want {
					if got[k] != w {
						t.Errorf("%s current = %+v, want %+v", k, got[k], w)
					}
				}
			},
		},
		{
			name: "container images (JVM detection)",
			check: func(t *testing.T, d *ClusterData) {
				if got, want := d.Workloads[keyWeb].Containers["app"].Images, []string{"registry/web:1.1.0-temurin21-jre", "registry/web:1.2.0-temurin21-jre"}; !reflect.DeepEqual(got, want) {
					t.Errorf("web/app images = %v, want %v", got, want)
				}
				if got := d.Workloads[keyDB].Containers["postgres"].Images; !reflect.DeepEqual(got, []string{"postgres:17"}) {
					t.Errorf("db/postgres images = %v", got)
				}
				if _, ok := d.Workloads[hubapi.WorkloadKey{Cluster: "c1", Namespace: "ns1", Kind: hubapi.KindDeployment, Name: "gone"}]; ok {
					t.Error("an image alone must not add a workload to the inventory")
				}
			},
		},
		{
			name: "init containers flagged and filtered by coverage",
			check: func(t *testing.T, d *ClusterData) {
				web := d.Workloads[keyWeb]
				for n, want := range map[string]bool{"app": false, "istio-proxy": true, "setup": true} {
					if got := web.Containers[n].Init; got != want {
						t.Errorf("%s Init = %v, want %v", n, got, want)
					}
				}
				data := web.Data(cfg.Profiles[hubapi.ProfileProduction], d.End, cfg)
				if got := data.ContainerNames(); !reflect.DeepEqual(got, []string{"app", "istio-proxy"}) {
					t.Fatalf("Data containers = %v, want [app istio-proxy] (setup has <50%% coverage)", got)
				}
				for _, c := range data.Containers {
					if c.Init != (c.Name == "istio-proxy") {
						t.Errorf("%s Init = %v", c.Name, c.Init)
					}
					if c.CPU.Samples != 14*288 {
						t.Errorf("%s CPU samples = %d", c.Name, c.CPU.Samples)
					}
				}
				if math.Abs(data.AverageReplicas-3) > 1e-9 {
					t.Errorf("AverageReplicas = %v, want 3", data.AverageReplicas)
				}
				app := data.Containers[0]
				if math.Abs(app.ThrottleRatio-0.2) > 1e-9 || !app.OOMKilled || app.Restarts != 4 || app.Used24hCPU != 1.5 {
					t.Errorf("app data = %+v", app)
				}
			},
		},
		{
			name: "hpa attached to its target",
			check: func(t *testing.T, d *ClusterData) {
				want := &HPA{Name: "web-hpa", Metrics: []string{"cpu/utilization", "http_requests/averagevalue"},
					Targets: map[string]float64{"cpu": 60}, MinReplicas: 2, MaxReplicas: 6, PeakReplicas: 4}
				h := d.Workloads[keyWeb].HPA
				if !reflect.DeepEqual(h, want) {
					t.Fatalf("web HPA = %+v, want %+v", h, want)
				}
				if !h.Coupled() {
					t.Error("web HPA not coupled")
				}
				for _, k := range []hubapi.WorkloadKey{keyDB, keyAgent, keyReport} {
					if d.Workloads[k].HPA != nil {
						t.Errorf("%s HPA = %+v, want nil", k.ID(), d.Workloads[k].HPA)
					}
				}
			},
		},
		{
			name: "hpa on custom metrics only is not coupled",
			mutate: func(f *fakeQuerier, sq sourceQueries) {
				f.instant[sq.hpaMetrics] = f.instant[sq.hpaMetrics][:1]
			},
			check: func(t *testing.T, d *ClusterData) {
				h := d.Workloads[keyWeb].HPA
				if h == nil || h.Name != "web-hpa" || !reflect.DeepEqual(h.Metrics, []string{"http_requests/averagevalue"}) {
					t.Fatalf("web HPA = %+v", h)
				}
				if h.Coupled() {
					t.Error("custom-metric HPA reported coupled")
				}
			},
		},
		{
			name: "oom and restarts",
			check: func(t *testing.T, d *ClusterData) {
				app, pg := d.Workloads[keyWeb].Containers["app"], d.Workloads[keyDB].Containers["postgres"]
				if !app.OOMKilled || app.Restarts != 4 {
					t.Errorf("app oom=%v restarts=%v, want true 4", app.OOMKilled, app.Restarts)
				}
				if pg.OOMKilled || pg.Restarts != 0 {
					t.Errorf("postgres oom=%v restarts=%v, want false 0", pg.OOMKilled, pg.Restarts)
				}
			},
		},
		{
			name: "pricing from metrics",
			check: func(t *testing.T, d *ClusterData) {
				want := Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004, Source: PricingSourceMetrics, Nodes: 5,
					Sources:   []hubapi.PricingRateSource{{Source: "cur", Nodes: 4}, {Source: "list", Nodes: 1}},
					CURPeriod: &hubapi.PricingPeriod{Start: curPeriodStart, End: curPeriodEnd}}
				if !reflect.DeepEqual(d.Pricing, want) || d.Pricing.Fallback() {
					t.Errorf("pricing = %+v, want %+v", d.Pricing, want)
				}
			},
		},
		{
			name: "pricing without a cur node has no period",
			mutate: func(f *fakeQuerier, sq sourceQueries) {
				f.instant[sq.priceSources] = []VectorSample{vs(Labels{"source": "list"}, 5)}
			},
			check: func(t *testing.T, d *ClusterData) {
				if d.Pricing.CURPeriod != nil || len(d.Pricing.Sources) != 1 {
					t.Errorf("pricing = %+v, want list only and no period", d.Pricing)
				}
			},
		},
		{
			name: "a failed source query leaves the sources out",
			mutate: func(f *fakeQuerier, sq sourceQueries) {
				f.fail[sq.priceSources] = errors.New("timeout")
			},
			check: func(t *testing.T, d *ClusterData) {
				if d.Pricing.Source != PricingSourceMetrics || d.Pricing.Sources != nil || d.Pricing.CURPeriod != nil {
					t.Errorf("pricing = %+v, want metrics without sources", d.Pricing)
				}
			},
		},
		{
			name: "pricing default when series missing",
			mutate: func(f *fakeQuerier, sq sourceQueries) {
				f.instant[sq.priceCPU], f.instant[sq.priceMem], f.instant[sq.priceNodes] = nil, nil, nil
			},
			check: func(t *testing.T, d *ClusterData) {
				want := Pricing{CPUCoreHour: cfg.DefaultCPUCoreHourUSD, MemoryGiBHour: cfg.DefaultMemoryGiBHourUSD, Source: PricingSourceDefault}
				if !reflect.DeepEqual(d.Pricing, want) || !d.Pricing.Fallback() {
					t.Errorf("pricing = %+v, want %+v", d.Pricing, want)
				}
			},
		},
		{
			name: "pricing default when memory rate missing",
			mutate: func(f *fakeQuerier, sq sourceQueries) {
				f.instant[sq.priceMem] = nil
			},
			check: func(t *testing.T, d *ClusterData) {
				if d.Pricing.Source != PricingSourceDefault || d.Pricing.CPUCoreHour != cfg.DefaultCPUCoreHourUSD || d.Pricing.Nodes != 0 {
					t.Errorf("pricing = %+v, want default", d.Pricing)
				}
			},
		},
		{
			name:    "range querier error",
			mutate:  func(f *fakeQuerier, sq sourceQueries) { f.fail[sq.throttle] = boom },
			wantErr: "throttle: boom",
		},
		{
			name:    "instant querier error",
			mutate:  func(f *fakeQuerier, sq sourceQueries) { f.fail[sq.hpa] = boom },
			wantErr: "hpa: boom",
		},
		{
			name:    "inventory querier error",
			mutate:  func(f *fakeQuerier, sq sourceQueries) { f.fail[sq.res["limits"][true][true]] = boom },
			wantErr: "limits(init=true,lookback=25h0m0s): boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, sq := sourceFixture(cfg)
			if tc.mutate != nil {
				tc.mutate(f, sq)
			}
			src := &Source{Q: f, Cfg: cfg, Concurrency: 3}
			// end is not aligned to the step: Load truncates it.
			d, err := src.Load(context.Background(), "c1", testEnd.Add(2*time.Minute+7*time.Second), window)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, boom) {
					t.Fatalf("err = %v, want %q wrapping boom", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.unknown) > 0 {
				t.Errorf("unrecognised queries:\n%s", strings.Join(f.unknown, "\n---\n"))
			}
			tc.check(t, d)
		})
	}
}

// TestSourceLoadQueries checks that Load issues every expected query once with the right times.
func TestSourceLoadQueries(t *testing.T) {
	cfg := DefaultConfig()
	f, sq := sourceFixture(cfg)
	window := 28 * 24 * time.Hour
	// The peak replicas cover the loaded window.
	q := Queries{Cluster: "c1", RateWindow: cfg.RateWindow}
	f.instant[q.HPAPeakReplicas(window)] = f.instant[sq.hpaPeak]
	delete(f.instant, sq.hpaPeak)
	src := &Source{Q: f, Cfg: cfg} // default concurrency
	if _, err := src.Load(context.Background(), "c1", testEnd.Add(time.Minute), window); err != nil {
		t.Fatal(err)
	}
	if len(f.unknown) > 0 {
		t.Errorf("unrecognised queries: %d", len(f.unknown))
	}
	wantRange := map[string]time.Time{
		sq.cpu: testEnd.Add(-window), sq.mem: testEnd.Add(-window), sq.pods: testEnd.Add(-window),
		sq.cpu24h: testEnd.Add(-24 * time.Hour), sq.mem24h: testEnd.Add(-24 * time.Hour),
		sq.throttle: testEnd.Add(-cfg.RiskWindow),
	}
	for q, from := range wantRange {
		if got, ok := f.called[q]; !ok || !got.Equal(from) {
			t.Errorf("range query start = %v (called %v), want %v:\n%.80s", got, ok, from, q)
		}
		if !f.ends[q].Equal(testEnd) || f.steps[q] != cfg.Step {
			t.Errorf("range query end/step = %v/%v, want %v/%v", f.ends[q], f.steps[q], testEnd, cfg.Step)
		}
	}
	var instants []string
	for q := range f.instant {
		instants = append(instants, q)
	}
	if len(instants) != 23 {
		t.Errorf("fixture has %d instant queries, want 23", len(instants))
	}
	for _, q := range instants {
		if got, ok := f.called[q]; !ok || !got.Equal(testEnd) {
			t.Errorf("instant query time = %v (called %v), want %v:\n%.80s", got, ok, testEnd, q)
		}
	}
	if want := len(wantRange) + len(instants); len(f.called) != want {
		t.Errorf("distinct queries = %d, want %d", len(f.called), want)
	}
}

func TestRawWorkloadData(t *testing.T) {
	cfg := DefaultConfig()
	prod := cfg.Profiles[hubapi.ProfileProduction]
	dev := cfg.Profiles[hubapi.ProfileDev]
	cases := []struct {
		name      string
		profile   Profile
		w         *RawWorkload
		wantNames []string
		wantInit  map[string]bool
		wantCPU   map[string]int // CPU samples per container
		wantReps  float64
	}{
		{
			name:    "init with full coverage kept, short init dropped",
			profile: prod,
			w: &RawWorkload{Key: keyWeb, Pods: podsConst(14, 2), Containers: map[string]*RawContainer{
				"app":   {Name: "app", CPU: flat(14, 1, 0, 1), Memory: flat(14, mi, 0, 2)},
				"proxy": {Name: "proxy", Init: true, CPU: flat(14, 0.1, 0, 3)},
				"setup": {Name: "setup", Init: true, CPU: flat(14, 0.1, 0, 4)[:100]},
			}},
			wantNames: []string{"app", "proxy"},
			wantInit:  map[string]bool{"app": false, "proxy": true},
			wantCPU:   map[string]int{"app": 14 * 288, "proxy": 14 * 288},
			wantReps:  2,
		},
		{
			name:    "init at exactly half coverage kept",
			profile: prod,
			w: &RawWorkload{Key: keyWeb, Pods: podsConst(14, 1), Containers: map[string]*RawContainer{
				"app":  {Name: "app", CPU: flat(14, 1, 0, 1)},
				"half": {Name: "half", Init: true, CPU: flat(7, 0.1, 0, 3)},
			}},
			wantNames: []string{"app", "half"},
			wantInit:  map[string]bool{"app": false, "half": true},
			wantCPU:   map[string]int{"app": 14 * 288, "half": 7 * 288},
			wantReps:  1,
		},
		{
			name:    "window trims to the profile",
			profile: dev,
			w: &RawWorkload{Key: keyDB, Pods: podsConst(14, 4), Containers: map[string]*RawContainer{
				"pg": {Name: "pg", CPU: flat(14, 1, 0, 1), Memory: flat(14, mi, 0, 2)},
			}},
			wantNames: []string{"pg"},
			wantInit:  map[string]bool{"pg": false},
			wantCPU:   map[string]int{"pg": 7 * 288},
			wantReps:  4,
		},
		{
			name:    "pods that started mid-window average to the steps since the first sample",
			profile: prod,
			w: &RawWorkload{Key: keyDB, Pods: podsConst(2, 6), Containers: map[string]*RawContainer{
				"pg": {Name: "pg", CPU: flat(2, 1, 0, 1)},
			}},
			wantNames: []string{"pg"},
			wantInit:  map[string]bool{"pg": false},
			wantCPU:   map[string]int{"pg": 2 * 288},
			wantReps:  6,
		},
		{
			name:    "no usage (cronjob between runs)",
			profile: prod,
			w: &RawWorkload{Key: keyReport, Containers: map[string]*RawContainer{
				"job": {Name: "job", Current: Current{CPURequest: 0.25}},
			}},
			wantNames: []string{"job"},
			wantInit:  map[string]bool{"job": false},
			wantCPU:   map[string]int{"job": 0},
			wantReps:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.w.Data(tc.profile, testEnd, cfg)
			if d.Key != tc.w.Key {
				t.Errorf("key = %v", d.Key)
			}
			if got := d.ContainerNames(); !reflect.DeepEqual(got, tc.wantNames) {
				t.Fatalf("containers = %v, want %v", got, tc.wantNames)
			}
			for _, c := range d.Containers {
				if c.Init != tc.wantInit[c.Name] {
					t.Errorf("%s Init = %v", c.Name, c.Init)
				}
				if c.CPU.Samples != tc.wantCPU[c.Name] {
					t.Errorf("%s CPU samples = %d, want %d", c.Name, c.CPU.Samples, tc.wantCPU[c.Name])
				}
				if c.Current != tc.w.Containers[c.Name].Current {
					t.Errorf("%s current = %+v", c.Name, c.Current)
				}
			}
			if math.Abs(d.AverageReplicas-tc.wantReps) > 1e-9 {
				t.Errorf("AverageReplicas = %v, want %v", d.AverageReplicas, tc.wantReps)
			}
		})
	}
}

// fakeLoader returns fixed cluster data and records the requested window.
type fakeLoader struct {
	d      *ClusterData
	err    error
	window time.Duration
	end    time.Time
}

func (l *fakeLoader) Load(_ context.Context, cluster string, end time.Time, window time.Duration) (*ClusterData, error) {
	l.window, l.end = window, end
	if l.err != nil {
		return nil, l.err
	}
	l.d.Cluster = cluster
	return l.d, nil
}

func TestEngineRunCluster(t *testing.T) {
	cfg := DefaultConfig()
	pricing := Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004, Source: PricingSourceMetrics, Nodes: 3}
	newData := func() *ClusterData {
		return &ClusterData{End: testEnd, Window: 28 * 24 * time.Hour, Pricing: pricing, Workloads: map[hubapi.WorkloadKey]*RawWorkload{
			keyWeb: {Key: keyWeb, Pods: podsConst(14, 2), HasRequests: true, Containers: map[string]*RawContainer{
				"app": {Name: "app", CPU: flat(14, 0.5, 0.1, 1), Memory: flat(14, 200*mi, 0.1, 2),
					Current: Current{CPURequest: 1, MemoryRequest: 512 * mi}, Used24hCPU: 1, Used24hMemory: 400 * mi},
				"istio-proxy": {Name: "istio-proxy", Init: true, CPU: flat(14, 0.05, 0.1, 3), Memory: flat(14, 40*mi, 0.1, 4),
					Used24hCPU: 0.1, Used24hMemory: 80 * mi},
			}, HPA: &HPA{Name: "web-hpa", Metrics: []string{"cpu/utilization"}}},
			keyDB: {Key: keyDB, Pods: podsConst(14, 1), HasRequests: true, Containers: map[string]*RawContainer{
				"postgres": {Name: "postgres", CPU: flat(14, 1, 0.1, 5), Memory: flat(14, 2048*mi, 0.05, 6),
					Current: Current{CPURequest: 2, MemoryRequest: 4096 * mi}},
			}},
			keyReport: {Key: keyReport, HasRequests: true, Containers: map[string]*RawContainer{
				"job": {Name: "job", Current: Current{CPURequest: 0.25, MemoryRequest: 128 * mi}},
			}},
		}}
	}
	devFor := func(k hubapi.WorkloadKey) *hubapi.ValueSettings {
		if k == keyWeb {
			return &hubapi.ValueSettings{Profile: hubapi.ProfileDev}
		}
		return nil
	}
	cases := []struct {
		name        string
		settings    func(hubapi.WorkloadKey) *hubapi.ValueSettings
		loadErr     error
		wantWindows map[string]string // workload name -> explanation window
		wantProfile map[string]string
	}{
		{
			name:        "per-workload settings",
			settings:    devFor,
			wantWindows: map[string]string{"web": "168h0m0s", "db": "336h0m0s"},
			wantProfile: map[string]string{"web": hubapi.ProfileDev, "db": hubapi.ProfileProduction},
		},
		{
			name:        "nil settings callback uses defaults",
			wantWindows: map[string]string{"web": "336h0m0s", "db": "336h0m0s"},
			wantProfile: map[string]string{"web": hubapi.ProfileProduction, "db": hubapi.ProfileProduction},
		},
		{
			name:        "unknown profile falls back to default",
			settings:    func(hubapi.WorkloadKey) *hubapi.ValueSettings { return &hubapi.ValueSettings{Profile: "nope"} },
			wantWindows: map[string]string{"web": "336h0m0s", "db": "336h0m0s"},
			wantProfile: map[string]string{"web": hubapi.ProfileProduction, "db": hubapi.ProfileProduction},
		},
		{
			name:    "loader error",
			loadErr: errors.New("prometheus down"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &fakeLoader{d: newData(), err: tc.loadErr}
			e := &Engine{Loader: l, Cfg: cfg, Strategy: PercentileStrategy{}}
			var asked []hubapi.WorkloadKey
			var settings func(hubapi.WorkloadKey) *hubapi.ValueSettings
			if tc.settings != nil {
				settings = func(k hubapi.WorkloadKey) *hubapi.ValueSettings {
					asked = append(asked, k)
					return tc.settings(k)
				}
			}
			res, err := e.RunCluster(context.Background(), "c1", testEnd, settings)
			if l.window != 28*24*time.Hour || !l.end.Equal(testEnd) {
				t.Errorf("Load window/end = %s/%s, want the longest profile window 672h", l.window, l.end)
			}
			if tc.loadErr != nil {
				if !errors.Is(err, tc.loadErr) || res != nil {
					t.Fatalf("err = %v res = %v, want loader error", err, res)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Cluster != "c1" || !res.End.Equal(testEnd) || !reflect.DeepEqual(res.Pricing, pricing) {
				t.Errorf("result header = %s %s %+v", res.Cluster, res.End, res.Pricing)
			}
			if settings != nil && len(asked) != 3 {
				t.Errorf("settings asked for %d workloads, want 3", len(asked))
			}

			// Inventory: every workload, sorted by id.
			var invKeys []hubapi.WorkloadKey
			inv := map[string]WorkloadInventory{}
			for _, w := range res.Inventory {
				invKeys = append(invKeys, w.Key)
				inv[w.Key.Name] = w
			}
			if want := newData().SortedKeys(); !reflect.DeepEqual(invKeys, want) {
				t.Fatalf("inventory = %v, want %v", invKeys, want)
			}
			if got := inv["web"].Containers; !reflect.DeepEqual(got, []string{"app", "istio-proxy"}) {
				t.Errorf("web containers = %v", got)
			}
			if got := inv["report"].Containers; !reflect.DeepEqual(got, []string{"job"}) {
				t.Errorf("report containers = %v", got)
			}
			if w := inv["web"]; math.Abs(w.Used24hCPU-1.1) > 1e-9 || math.Abs(w.Used24hMemory-480*mi) > 1 {
				t.Errorf("web used24h = %v / %v, want 1.1 / 480Mi", w.Used24hCPU, w.Used24hMemory)
			}
			if w := inv["web"]; w.HPA == nil || w.HPA.Name != "web-hpa" || math.Abs(w.AverageReplicas-2) > 1e-9 {
				t.Errorf("web inventory = %+v", w)
			}
			if w := inv["report"]; w.AverageReplicas != 0 || w.HPA != nil {
				t.Errorf("report inventory = %+v", w)
			}

			// Results: only workloads with usage data.
			got := map[string]hubapi.WorkloadRecommendation{}
			for _, r := range res.Results {
				got[r.Recommendation.Workload.Name] = r.Recommendation
				if !reflect.DeepEqual(r.Pricing, pricing) {
					t.Errorf("%s pricing = %+v", r.Recommendation.Workload.Name, r.Pricing)
				}
			}
			if len(got) != 2 || got["web"].Workload.Name == "" || got["db"].Workload.Name == "" {
				t.Fatalf("results for %v, want web and db", keysOf(got))
			}
			for name, rec := range got {
				if rec.Settings.Profile != tc.wantProfile[name] {
					t.Errorf("%s profile = %s, want %s", name, rec.Settings.Profile, tc.wantProfile[name])
				}
				if rec.DataCoverage.Window != tc.wantWindows[name] {
					t.Errorf("%s coverage window = %s, want %s", name, rec.DataCoverage.Window, tc.wantWindows[name])
				}
				for _, c := range rec.Containers {
					if c.Explanation == nil || c.Explanation.Window != tc.wantWindows[name] || c.Explanation.Profile != tc.wantProfile[name] {
						t.Errorf("%s/%s explanation = %+v, want window %s", name, c.Name, c.Explanation, tc.wantWindows[name])
					}
				}
			}
			if !slices.Contains(got["web"].RiskFlags, hubapi.FlagHPACoupled) {
				t.Errorf("web flags = %v, want %s", got["web"].RiskFlags, hubapi.FlagHPACoupled)
			}
		})
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EndAlignment moves a run's data end to its own alignment (dev/e2e); the step and the query
// shapes stay the same.
func TestSourceLoadEndAlignment(t *testing.T) {
	cases := []struct {
		name  string
		align time.Duration
		at    time.Duration // after testEnd
		want  time.Duration // data end after testEnd
	}{
		{"default: the step", 0, 4*time.Minute + 50*time.Second, 0},
		{"30s", 30 * time.Second, 4*time.Minute + 50*time.Second, 4*time.Minute + 30*time.Second},
		{"1m", time.Minute, 61 * time.Second, time.Minute},
		{"equal to the step", 5 * time.Minute, 4 * time.Minute, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.EndAlignment = tc.align
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			f, sq := sourceFixture(cfg)
			src := &Source{Q: f, Cfg: cfg}
			d, err := src.Load(context.Background(), "c1", testEnd.Add(tc.at), 7*24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			want := testEnd.Add(tc.want)
			if !d.End.Equal(want) || !f.ends[sq.cpu].Equal(want) || f.steps[sq.cpu] != cfg.Step {
				t.Errorf("end %v, cpu query end/step %v/%v; want end %v step %v", d.End, f.ends[sq.cpu], f.steps[sq.cpu], want, cfg.Step)
			}
		})
	}
}

func TestConfigEndAlignmentValidate(t *testing.T) {
	for _, tc := range []struct {
		align time.Duration
		ok    bool
	}{{0, true}, {30 * time.Second, true}, {5 * time.Minute, true}, {-time.Second, false}, {6 * time.Minute, false}} {
		cfg := DefaultConfig()
		cfg.EndAlignment = tc.align
		if err := cfg.Validate(); (err == nil) != tc.ok {
			t.Errorf("endAlignment %s: err %v, want ok=%v", tc.align, err, tc.ok)
		}
	}
}
