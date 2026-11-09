package dynatrace_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace"
	dtt "github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace/dynatracetest"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

var end = time.Date(2026, 11, 9, 11, 20, 0, 0, time.UTC)

func source(t *testing.T, srv *dtt.Server) *dynatrace.Source {
	t.Helper()
	s := dynatrace.New(srv.URL, "", dtt.BearerClient())
	s.Client.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

func srvClient() *http.Client { return &http.Client{} }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLoadWorkloadsUnits(t *testing.T) {
	web := hubapi.WorkloadKey{Cluster: "prod", Namespace: "shop", Kind: hubapi.KindDeployment, Name: "web"}
	db := hubapi.WorkloadKey{Cluster: "prod", Namespace: "shop", Kind: hubapi.KindStatefulSet, Name: "db"}
	for _, tc := range []struct {
		name    string
		cpuUnit string
		// factor from the fixture's numbers to cores
		cores float64
	}{
		{"millicores", "MilliCores", 1e-3},
		{"cores", "Cores", 1},
		{"no unit: millicores assumed", "", 1e-3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := dtt.New(t, dtt.Healthy(t, end, tc.cpuUnit)...)
			m := datasource.Matcher{Cluster: "prod", Label: "cluster", Value: "prod-eu1"}
			got, err := source(t, srv).LoadWorkloads(context.Background(), recommender.WorkloadRequest{
				Cluster: "prod", Match: &m, Start: end.Add(-20 * time.Minute), End: end, Step: 5 * time.Minute,
				RiskWindow: 2 * time.Hour, InventoryLookback: 20 * time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 {
				t.Fatalf("workloads = %v, want web and db (the job is not rightsized)", keys(got))
			}
			w := got[web]
			if w == nil || !w.ThrottleDerived || !w.HPAUnknown || !w.HasRequests || !w.Live {
				t.Fatalf("web = %+v", w)
			}
			app := w.Containers["app"]
			wantCPU := []recommender.Sample{{T: end.Add(-15 * time.Minute), V: 250 * tc.cores}, {T: end.Add(-10 * time.Minute), V: 300 * tc.cores}, {T: end, V: 500 * tc.cores}}
			if len(app.CPU) != len(wantCPU) {
				t.Fatalf("cpu = %v", app.CPU)
			}
			for i, s := range wantCPU {
				if !app.CPU[i].T.Equal(s.T) || !near(app.CPU[i].V, s.V) {
					t.Errorf("cpu[%d] = %v, want %v", i, app.CPU[i], s)
				}
			}
			if !near(app.Used24hCPU, (500+600+1000)/3.0*tc.cores) {
				t.Errorf("used24hCPU = %v", app.Used24hCPU)
			}
			if app.Memory[0].V != 104857600 || !near(app.Used24hMemory, (209715200+230686720+251658240)/3.0) {
				t.Errorf("memory = %v, used24h %v", app.Memory, app.Used24hMemory)
			}
			if !near(app.Current.CPURequest, 500*tc.cores) || !near(app.Current.CPULimit, 500*tc.cores) || app.Current.MemoryRequest != 268435456 {
				t.Errorf("current = %+v", app.Current)
			}
			if len(app.Throttle) != 2 || !near(app.Throttle[0].V, 0.125) {
				t.Errorf("throttle (percent to ratio) = %v", app.Throttle)
			}
			if !app.OOMKilled || app.Restarts != 3 {
				t.Errorf("oom %v restarts %v", app.OOMKilled, app.Restarts)
			}
			if len(w.Pods) != 3 || w.Pods[0].V != 2 {
				t.Errorf("pods (numbers sent as strings) = %v", w.Pods)
			}
			// db has a request in the lookback but none in the last step: in the inventory, not live
			if d := got[db]; d == nil || d.Live || !d.HasRequests || !near(d.Containers["postgres"].Current.CPURequest, 200*tc.cores) {
				t.Errorf("db = %+v", d)
			}
			// every query selects the binding's Dynatrace cluster name
			for _, q := range srv.Queries {
				if !strings.Contains(q, `k8s.cluster.name == "prod-eu1"`) {
					t.Errorf("query without the cluster filter: %s", q)
				}
			}
		})
	}
}

func keys(m map[hubapi.WorkloadKey]*recommender.RawWorkload) []string {
	var out []string
	for k := range m {
		out = append(out, k.ID())
	}
	return out
}

func TestLoadWorkloadsBatch(t *testing.T) {
	srv := dtt.New(t, dtt.Healthy(t, end, "MilliCores")...)
	_, err := source(t, srv).LoadWorkloads(context.Background(), recommender.WorkloadRequest{
		Cluster: "prod", Start: end.Add(-14 * 24 * time.Hour), End: end, Step: 5 * time.Minute,
		RiskWindow: 7 * 24 * time.Hour, InventoryLookback: 25 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 14 days at 5 minutes is 4,032 steps: three chunks of at most 1,440 buckets per series
	if n := srv.Count("avg(dt.kubernetes.container.cpu_usage)"); n != 3 {
		t.Errorf("cpu queries = %d, want 3 chunks", n)
	}
	if n := srv.Count("summarize pods"); n != 3 {
		t.Errorf("pods queries = %d, want 3", n)
	}
	// one batch: 3 x (cpu, memory, pods) + throttle + oom + restarts + 4 inventory
	if n := len(srv.Queries); n != 16 {
		t.Errorf("queries = %d, want 16:\n%s", n, strings.Join(srv.Queries, "\n---\n"))
	}
	if n := srv.Count("avg(dt.containers.cpu.throttling_ratio)", "interval: 1h"); n != 1 {
		t.Errorf("throttle queries at 1h = %d", n)
	}
}

func TestQueryRetriesAndPolls(t *testing.T) {
	srv := dtt.New(t, dtt.Rule{Contains: []string{"data record"}, Records: `[{"ok":"1"}]`})
	srv.TooMany = 2
	srv.Running = true
	var waits []time.Duration
	s := dynatrace.New(srv.URL, "", dtt.BearerClient())
	s.Client.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, err := s.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || srv.Polls() != 1 {
		t.Errorf("waits %v (Retry-After 0 twice), polls %d", waits, srv.Polls())
	}
}

func TestQueryErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rule   dtt.Rule
		client bool // without the token
		status int
		hint   string
	}{
		{"invalid token", dtt.Rule{}, true, 401, "platform token"},
		{"missing scope", dtt.Rule{Contains: []string{"data record"}, Status: 403, Body: `{"error":{"code":403,"message":"Missing scope storage:metrics:read"}}`}, false, 403, "storage:metrics:read"},
		{"syntax", dtt.Rule{Contains: []string{"data record"}, Status: 400, Body: `{"error":{"code":400,"message":"DQL error","details":{"errorMessage":"unknown command"}}}`}, false, 400, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := dtt.New(t, tc.rule)
			hc := dtt.BearerClient()
			if tc.client {
				hc = srvClient()
			}
			s := dynatrace.New(srv.URL, "", hc)
			_, err := s.Probe(context.Background())
			var e *dynatrace.Error
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), dtt.Token) {
				t.Error("the error contains the token")
			}
			if h := dynatrace.Hint(err); !strings.Contains(h, tc.hint) {
				t.Errorf("hint %q, want %q", h, tc.hint)
			}
		})
	}
}

func TestPromQLRefused(t *testing.T) {
	s := dynatrace.New("https://abc12345.apps.dynatrace.com", "", nil)
	if _, err := s.Instant(context.Background(), "vector(1)", end); !errors.Is(err, dynatrace.ErrNoPromQL) {
		t.Errorf("instant: %v", err)
	}
	if _, err := s.Range(context.Background(), "vector(1)", end, end, time.Minute); !errors.Is(err, dynatrace.ErrNoPromQL) {
		t.Errorf("range: %v", err)
	}
}

func TestPlatformURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://abc12345.live.dynatrace.com":      "https://abc12345.apps.dynatrace.com",
		"https://abc12345.live.dynatrace.com/":     "https://abc12345.apps.dynatrace.com",
		"https://abc12345.apps.dynatrace.com":      "https://abc12345.apps.dynatrace.com",
		"https://dynatrace.example.internal/e/abc": "https://dynatrace.example.internal/e/abc",
	} {
		if got := dynatrace.PlatformURL(in); got != want {
			t.Errorf("PlatformURL(%q) = %q, want %q", in, got, want)
		}
	}
	if s := dynatrace.New("https://abc12345.live.dynatrace.com", "https://activegate.example.internal:9999/", nil); s.Client.Base != "https://activegate.example.internal:9999" {
		t.Errorf("ActiveGate base = %q", s.Client.Base)
	}
}

func TestNodes(t *testing.T) {
	srv := dtt.New(t, dtt.Healthy(t, end, "MilliCores")...)
	nodes, err := source(t, srv).Nodes(context.Background(), "prod-eu1", end)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("nodes = %+v", nodes)
	}
	a, b, c := nodes[0], nodes[1], nodes[2]
	if a.InstanceType != "m6i.large" || a.CapacityType != "spot" || a.Zone != "eu-west-1a" || a.Region != "eu-west-1" || !near(a.AllocatableCores, 1.93) || a.AllocatableMemoryBytes != 7.2e9 {
		t.Errorf("karpenter node from the Kubernetes object = %+v", a)
	}
	if b.InstanceType != "m6g.xlarge" || b.CapacityType != "ON_DEMAND" || !near(b.AllocatableCores, 3.92) {
		t.Errorf("managed node group node from tags:k8s.labels = %+v", b)
	}
	if c.InstanceType != "" || c.Zone != "eu-west-1c" {
		t.Errorf("node without an instance type = %+v", c)
	}
}

func TestCountStatuses(t *testing.T) {
	srv := dtt.New(t, append([]dtt.Rule{{Contains: []string{"count(dt.kubernetes.container.oom_kills, scalar: true)"}, Records: "[]"}}, dtt.Healthy(t, end, "MilliCores")...)...)
	c, err := source(t, srv).Count(context.Background(), "prod-eu1", end)
	if err != nil {
		t.Fatal(err)
	}
	st := c.Statuses()
	by := map[string]hubapi.DatasourceMetricStatus{}
	for _, s := range st {
		by[s.Input] = s
	}
	if len(st) != len(dynatrace.Inputs) {
		t.Fatalf("statuses = %d, want %d", len(st), len(dynatrace.Inputs))
	}
	for input, want := range map[string]string{
		"container_cpu_usage_seconds_total":                                          hubapi.MetricFound,
		"kube_pod_container_resource_requests":                                       hubapi.MetricFound,
		"kube_pod_owner, kube_replicaset_owner, kube_job_owner":                      hubapi.MetricFound,
		"kube_node_labels":                                                           hubapi.MetricFound,
		"container_oom_events_total":                                                 hubapi.MetricMissing,
		"kube_horizontalpodautoscaler_*":                                             hubapi.MetricNotAvailable,
		"container_cpu_cfs_throttled_periods_total, container_cpu_cfs_periods_total": hubapi.MetricFound,
	} {
		if by[input].Status != want {
			t.Errorf("%s: %+v, want %s", input, by[input], want)
		}
	}
	if m := dynatrace.Missing(st); len(m) != 1 || m[0] != dynatrace.KeyOOMKills {
		t.Errorf("missing = %v", m)
	}
	if by["kube_pod_container_resource_requests"].Series != 8 || by["kube_node_labels"].Series != 3 {
		t.Errorf("series counts: %+v %+v", by["kube_pod_container_resource_requests"], by["kube_node_labels"])
	}
}

func TestWorkloadUsage(t *testing.T) {
	srv := dtt.New(t, append([]dtt.Rule{{Contains: []string{"avg(dt.kubernetes.container.cpu_usage)", "vavg"},
		Records: `[{"timeframe":{"start":"2026-11-09T11:00:00Z"},"interval":"300000000000","k8s.container.name":"app","vmax":[400,500],"vavg":[200,250]}]`,
		Units:   map[string]string{dynatrace.KeyCPUUsage: "MilliCores"}}}, dtt.Healthy(t, end, "MilliCores")...)...)
	u, err := source(t, srv).WorkloadUsage(context.Background(), "prod-eu1", "shop", hubapi.KindDeployment, "web", "cpu", end.Add(-10*time.Minute), end, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Usage["app"]) != 2 || !near(u.Usage["app"][1].V, 0.5) || !near(u.Avg["app"][0].V, 0.2) || !near(u.Request["app"][0].V, 0.5) {
		t.Errorf("usage %+v", u)
	}
	q := srv.Queries[0]
	for _, want := range []string{`k8s.namespace.name == "shop"`, `k8s.workload.name == "web"`, `in(k8s.workload.kind, {"deployment", "Deployment"})`} {
		if !strings.Contains(q, want) {
			t.Errorf("usage query without %s: %s", want, q)
		}
	}
}

func TestHourlyReplicas(t *testing.T) {
	srv := dtt.New(t, dtt.Healthy(t, end, "MilliCores")...)
	reps, covered, err := source(t, srv).HourlyReplicas(context.Background(), "prod", "prod-eu1", end.Add(-20*time.Minute), end)
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)
	web := hubapi.WorkloadKey{Cluster: "prod", Namespace: "shop", Kind: hubapi.KindDeployment, Name: "web"}
	db := hubapi.WorkloadKey{Cluster: "prod", Namespace: "shop", Kind: hubapi.KindStatefulSet, Name: "db"}
	// 5-minute steps without a pod count as zero: web 3 steps x 2 pods / 12, db 4 x 1 / 12
	if !near(reps[web][hour], 0.5) || !near(reps[db][hour], 4.0/12) || !covered[hour] {
		t.Errorf("replicas %v covered %v", reps, covered)
	}
}

// TestGuideListsMapping keeps docs/install/dynatrace.md in step with the mapping table.
func TestGuideListsMapping(t *testing.T) {
	b, err := os.ReadFile("../../../../docs/install/dynatrace.md")
	if err != nil {
		t.Fatal(err)
	}
	guide := string(b)
	for _, in := range dynatrace.Inputs {
		first := strings.Split(in.Input, ",")[0]
		if !strings.Contains(guide, "`"+strings.TrimSpace(first)) {
			t.Errorf("the guide's mapping table lacks %s", first)
		}
		for _, k := range in.Keys {
			if !strings.Contains(guide, "`"+k+"`") {
				t.Errorf("the guide's mapping table lacks %s", k)
			}
		}
	}
}
