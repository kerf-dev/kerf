package synth

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
)

var testEnd = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func smallGenerator(t *testing.T, days float64, clusters ...string) *Generator {
	t.Helper()
	sc, err := LoadScenario("../scenarios/fleet-small.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) == 0 {
		clusters = []string{"synth-a"}
	}
	g, err := New(sc, Options{End: testEnd, Days: days, Clusters: clusters})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func traceOf(t *testing.T, g *Generator, name string) *WorkloadTrace {
	t.Helper()
	for _, cm := range g.Clusters {
		for _, w := range cm.Workloads {
			if w.Name == name {
				return g.Simulate(cm, w)
			}
		}
	}
	t.Fatalf("workload %s not found", name)
	return nil
}

// cpuPerPod returns all per-pod CPU samples of container ci.
func cpuPerPod(wt *WorkloadTrace, ci int) []float64 {
	var out []float64
	for _, p := range wt.Pods {
		out = append(out, p.Containers[ci].CPU...)
	}
	return out
}

func quantile(xs []float64, q float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 0 {
		return math.NaN()
	}
	return s[int(q*float64(len(s)-1))]
}

func mean(xs []float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{"1d12h", 36 * time.Hour, false},
		{"90s", 90 * time.Second, false},
		{"0", 0, false},
		{"", 0, false},
		{"xd", 0, true},
		{"5x", 0, true},
	} {
		got, err := ParseDuration(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v, err=%v", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestParseQuantity(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
	}{{"250m", 0.25}, {"2", 2}, {"512Mi", 512 * mib}, {"1Gi", gib}, {"1.5", 1.5}} {
		got, err := ParseQuantity(tc.in)
		if err != nil || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("ParseQuantity(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestScenarioValidation(t *testing.T) {
	const ok = `
clusters:
  - name: synth-x
    workloads:
      - {name: a, namespace: n, containers: [{name: c, cpu: {pattern: flat, base: 100m}}]}
`
	if _, err := ParseScenario([]byte(ok)); err != nil {
		t.Fatalf("valid scenario rejected: %v", err)
	}
	for _, tc := range []struct{ name, doc, want string }{
		{"real cluster name", `clusters: [{name: ro-spoke-1}]`, `must start with "synth-"`},
		{"instance type", `clusters: [{name: synth-x, nodePools: [{instanceType: t9.huge}]}]`, "unknown instance type"},
		{"capacity type", `clusters: [{name: synth-x, nodePools: [{instanceType: m6i.xlarge, capacityType: reserved}]}]`, "capacityType"},
		{"cronjob schedule", `clusters: [{name: synth-x, workloads: [{name: a, namespace: n, kind: CronJob, containers: [{name: c}]}]}]`, "schedule"},
		{"pattern", `clusters: [{name: synth-x, workloads: [{name: a, namespace: n, containers: [{name: c, cpu: {pattern: wobbly}}]}]}]`, "unknown pattern"},
		{"hpa kind", `clusters: [{name: synth-x, workloads: [{name: a, namespace: n, kind: StatefulSet, hpa: {min: 1, max: 2, target: 50}, containers: [{name: c}]}]}]`, "only supported on Deployments"},
		{"duplicate", `clusters: [{name: synth-x, workloads: [{name: a, namespace: n, containers: [{name: c}]}, {name: a, namespace: n, containers: [{name: c}]}]}]`, "duplicate workload"},
		{"unknown field", `clusters: [{name: synth-x, colour: red}]`, "colour"},
		{"retention", "days: 40\nclusters: [{name: synth-x}]", "retention"},
		{"profile", `clusters: [{name: synth-x, fleet: {workloads: 3, profiles: {lazy: 1}}}]`, "unknown profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseScenario([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

// TestPatterns checks every pattern workload of fleet-small produces the intended shape.
func TestPatterns(t *testing.T) {
	g := smallGenerator(t, 14)
	tl := g.Timeline
	for _, tc := range []struct {
		workload string
		check    func(t *testing.T, wt *WorkloadTrace)
	}{
		{"synth-flat-0", func(t *testing.T, wt *WorkloadTrace) {
			cpu := cpuPerPod(wt, 0)
			if p50 := quantile(cpu, 0.5); math.Abs(p50-0.2) > 0.03 {
				t.Errorf("flat p50 = %.3f, want ~0.2", p50)
			}
			if r := quantile(cpu, 0.95) / quantile(cpu, 0.05); r > 1.8 {
				t.Errorf("flat p95/p5 = %.2f, want small", r)
			}
		}},
		{"synth-diurnal-0", func(t *testing.T, wt *WorkloadTrace) {
			peak, trough := hourMean(wt, tl, 12), hourMean(wt, tl, 0)
			if peak/trough < 2.5 {
				t.Errorf("diurnal peak/trough = %.2f, want > 2.5", peak/trough)
			}
		}},
		{"synth-weekly-0", func(t *testing.T, wt *WorkloadTrace) {
			var we, wd []float64
			for _, p := range wt.Pods {
				for k, v := range p.Containers[0].CPU {
					switch tl.Start.Add(time.Duration(p.Start+k) * tl.Step).Weekday() {
					case time.Saturday, time.Sunday:
						we = append(we, v)
					default:
						wd = append(wd, v)
					}
				}
			}
			if r := mean(we) / mean(wd); r > 0.5 || r < 0.3 {
				t.Errorf("weekend/weekday = %.2f, want ~0.4", r)
			}
		}},
		{"synth-spiky-0", func(t *testing.T, wt *WorkloadTrace) {
			cpu := cpuPerPod(wt, 0)
			if r := quantile(cpu, 0.999) / quantile(cpu, 0.5); r < 3 {
				t.Errorf("spiky p99.9/p50 = %.2f, want > 3", r)
			}
			if r := quantile(cpu, 0.95) / quantile(cpu, 0.5); r > 1.6 {
				t.Errorf("spiky p95/p50 = %.2f: spikes should be rare", r)
			}
		}},
		{"synth-leak-0", func(t *testing.T, wt *WorkloadTrace) {
			lim := float64(wt.Spec.Containers[0].Limits.Memory)
			ooms := 0
			for _, p := range wt.Pods {
				for _, in := range p.Containers[0].Instances {
					if in.EndReason == "OOMKilled" {
						ooms++
					}
				}
				for _, m := range p.Containers[0].Mem {
					if m > lim {
						t.Fatalf("memory %.0f above limit %.0f", m, lim)
					}
				}
			}
			// ~12h to OOM, 2 pods, 14 days: ~56.
			if ooms < 30 || ooms > 90 {
				t.Errorf("OOM kills = %d, want ~56", ooms)
			}
		}},
		{"synth-step-0", func(t *testing.T, wt *WorkloadTrace) {
			at := tl.N - 1 - tl.steps(4*24*time.Hour)
			var before, after []float64
			for _, p := range wt.Pods {
				for k, v := range p.Containers[0].CPU {
					if i := p.Start + k; i < at-60 {
						before = append(before, v)
					} else if i > at+60 {
						after = append(after, v)
					}
				}
			}
			if r := mean(after) / mean(before); r < 2.6 || r > 3.4 {
				t.Errorf("step after/before = %.2f, want ~3", r)
			}
			revs := map[int]bool{}
			for _, p := range wt.Pods {
				revs[p.Revision] = true
			}
			if len(revs) < 2 {
				t.Errorf("step change should come with a rollout, got %d revisions", len(revs))
			}
		}},
		{"synth-sparse-0", func(t *testing.T, wt *WorkloadTrace) {
			if n := len(wt.Pods); n < 14*24-2 || n > 14*24+1 {
				t.Errorf("cronjob pods = %d, want one per hour", n)
			}
			for _, p := range wt.Pods {
				if d := time.Duration(p.End-p.Start) * tl.Step; d > 13*time.Minute {
					t.Fatalf("job pod ran %s, want ~10m", d)
				}
			}
			if len(wt.Owners) != len(wt.Pods) {
				t.Errorf("jobs = %d, pods = %d", len(wt.Owners), len(wt.Pods))
			}
		}},
		{"synth-throttled-0", func(t *testing.T, wt *WorkloadTrace) {
			lim := float64(wt.Spec.Containers[0].Limits.CPU)
			var thr []float64
			for _, p := range wt.Pods {
				c := p.Containers[0]
				for k := range c.CPU {
					if c.CPU[k] > lim {
						t.Fatalf("cpu %.3f above limit %.3f", c.CPU[k], lim)
					}
					thr = append(thr, c.Throttled[k])
				}
			}
			if p95 := quantile(thr, 0.95); p95 < 0.3 {
				t.Errorf("throttle ratio p95 = %.2f, want > 0.3", p95)
			}
		}},
		{"synth-hpa-0", func(t *testing.T, wt *WorkloadTrace) {
			lo, hi := math.Inf(1), 0.0
			for _, r := range wt.Replicas {
				lo, hi = math.Min(lo, r), math.Max(hi, r)
			}
			if lo != 2 || hi < 4 || hi > 8 {
				t.Errorf("replicas range [%v, %v], want [2, 4..8]", lo, hi)
			}
			// Per-pod CPU stays around the 70% target of 500m.
			if p95 := quantile(cpuPerPod(wt, 0), 0.95); p95 > 0.5*0.7*1.3 {
				t.Errorf("per-pod p95 = %.3f, HPA should hold it near 0.35", p95)
			}
		}},
		{"synth-jvm-0", func(t *testing.T, wt *WorkloadTrace) {
			p := wt.Pods[len(wt.Pods)-1]
			c := p.Containers[0]
			if c.CPU[0] < 3*quantile(c.CPU, 0.5) {
				t.Errorf("no JVM startup spike: first %.3f vs median %.3f", c.CPU[0], quantile(c.CPU, 0.5))
			}
			if !strings.Contains(c.Image, "temurin") {
				t.Errorf("image %q", c.Image)
			}
			revs := map[int]bool{}
			for _, p := range wt.Pods {
				revs[p.Revision] = true
			}
			if len(revs) != 3 {
				t.Errorf("revisions = %d, want 3 (two rollouts)", len(revs))
			}
		}},
		{"synth-new-0", func(t *testing.T, wt *WorkloadTrace) {
			if want := tl.N - 1 - tl.steps(36*time.Hour); wt.Created != want {
				t.Errorf("created = %d, want %d", wt.Created, want)
			}
			for _, p := range wt.Pods {
				if p.Start < wt.Created {
					t.Fatalf("pod before creation")
				}
			}
		}},
		{"synth-restarting-0", func(t *testing.T, wt *WorkloadTrace) {
			errs := 0
			for _, p := range wt.Pods {
				for _, in := range p.Containers[0].Instances {
					if in.EndReason == "Error" {
						errs++
					}
				}
			}
			if errs < 40 || errs > 130 { // 2 pods × 14d / 8h ≈ 84
				t.Errorf("crashes = %d, want ~84", errs)
			}
		}},
		{"synth-daemon-0", func(t *testing.T, wt *WorkloadTrace) {
			nodes := map[string]bool{}
			for _, p := range wt.Pods {
				nodes[p.Node.Name] = true
			}
			if len(nodes) != len(wt.Cluster.Nodes) {
				t.Errorf("daemonset on %d nodes, cluster has %d", len(nodes), len(wt.Cluster.Nodes))
			}
		}},
		{"synth-ledger-store-0", func(t *testing.T, wt *WorkloadTrace) {
			for _, p := range wt.Pods {
				if !strings.HasPrefix(p.Name, "synth-ledger-store-0-") || p.OwnerKind != KindStatefulSet {
					t.Fatalf("pod %s owner %s", p.Name, p.OwnerKind)
				}
			}
		}},
	} {
		t.Run(tc.workload, func(t *testing.T) { tc.check(t, traceOf(t, g, tc.workload)) })
	}
}

func hourMean(wt *WorkloadTrace, tl Timeline, hour int) float64 {
	var xs []float64
	for _, p := range wt.Pods {
		for k, v := range p.Containers[0].CPU {
			if tl.Start.Add(time.Duration(p.Start+k)*tl.Step).UTC().Hour() == hour {
				xs = append(xs, v)
			}
		}
	}
	return mean(xs)
}

// collect runs a generator and returns every emitted series.
func collect(t *testing.T, g *Generator) []*Series {
	t.Helper()
	var mu sync.Mutex
	var out []*Series
	emit := func(s *Series) error { mu.Lock(); out = append(out, s); mu.Unlock(); return nil }
	for _, cm := range g.Clusters {
		if err := g.EmitNodes(cm, emit); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Each(4, func(wt *WorkloadTrace) error { return g.EmitWorkload(wt, emit) }); err != nil {
		t.Fatal(err)
	}
	return out
}

// required labels per metric (docs/contracts/metrics.md section 1).
var contract = map[string][]string{
	"container_cpu_usage_seconds_total":                {"cluster", "namespace", "pod"},
	"container_memory_working_set_bytes":               {"cluster", "namespace", "pod"},
	"container_cpu_cfs_periods_total":                  {"cluster", "namespace", "pod", "container"},
	"container_cpu_cfs_throttled_periods_total":        {"cluster", "namespace", "pod", "container"},
	"container_oom_events_total":                       {"cluster", "namespace", "pod", "container"},
	"kube_pod_container_resource_requests":             {"cluster", "namespace", "pod", "container", "node", "resource", "unit"},
	"kube_pod_container_resource_limits":               {"cluster", "namespace", "pod", "container", "node", "resource", "unit"},
	"kube_pod_owner":                                   {"cluster", "namespace", "pod", "owner_kind", "owner_name"},
	"kube_replicaset_owner":                            {"cluster", "namespace", "replicaset", "owner_kind", "owner_name"},
	"kube_job_owner":                                   {"cluster", "namespace", "job_name", "owner_kind", "owner_name"},
	"kube_pod_info":                                    {"cluster", "namespace", "pod", "node"},
	"kube_pod_init_container_info":                     {"cluster", "namespace", "pod", "container"},
	"kube_pod_container_info":                          {"cluster", "namespace", "pod", "container", "image"},
	"kube_pod_container_status_restarts_total":         {"cluster", "namespace", "pod", "container"},
	"kube_pod_container_status_last_terminated_reason": {"cluster", "namespace", "pod", "container", "reason"},
	"kube_deployment_spec_replicas":                    {"cluster", "namespace", "deployment"},
	"kube_statefulset_replicas":                        {"cluster", "namespace", "statefulset"},
	"kube_daemonset_status_desired_number_scheduled":   {"cluster", "namespace", "daemonset"},
	"kube_horizontalpodautoscaler_info":                {"cluster", "namespace", "horizontalpodautoscaler", "scaletargetref_kind", "scaletargetref_name"},
	"kube_horizontalpodautoscaler_spec_target_metric":  {"cluster", "namespace", "horizontalpodautoscaler", "metric_name", "metric_target_type"},
	"kube_node_labels": {"cluster", "node", "label_node_kubernetes_io_instance_type", "label_karpenter_sh_capacity_type",
		"label_topology_kubernetes_io_region", "label_topology_kubernetes_io_zone"},
	"kube_node_status_allocatable": {"cluster", "node", "resource", "unit"},
}

func TestSeriesContract(t *testing.T) {
	g := smallGenerator(t, 3, "synth-a", "synth-b")
	g.Scenario.WorkloadLabel = false
	all := collect(t, g)
	seen := map[string]int{}
	pods := map[string]bool{} // cluster/ns/pod with kube_pod_owner
	rs := map[string]bool{}   // cluster/ns/replicaset
	jobs := map[string]bool{} // cluster/ns/job
	cadvisorPods := map[string]bool{}
	for _, s := range all {
		name := s.Name()
		seen[name]++
		if !strings.HasPrefix(s.Label("cluster"), "synth-") {
			t.Fatalf("%s: cluster %q", name, s.Label("cluster"))
		}
		for _, l := range contract[name] {
			if s.Label(l) == "" {
				t.Fatalf("%s missing label %s: %v", name, l, s.Labels)
			}
		}
		for i := 1; i < len(s.Labels); i++ {
			if s.Labels[i-1].Name >= s.Labels[i].Name {
				t.Fatalf("labels not sorted/unique: %v", s.Labels)
			}
		}
		key := s.Label("cluster") + "/" + s.Label("namespace") + "/"
		switch {
		case name == "kube_pod_owner":
			pods[key+s.Label("pod")] = true
		case name == "kube_replicaset_owner":
			rs[key+s.Label("replicaset")] = true
		case name == "kube_job_owner":
			jobs[key+s.Label("job_name")] = true
		case strings.HasPrefix(name, "container_"):
			if s.Label("workload") != "" || s.Label("workload_kind") != "" {
				t.Fatalf("cAdvisor series with workload label: %v", s.Labels)
			}
			cadvisorPods[key+s.Label("pod")] = true
			if strings.HasSuffix(name, "_total") {
				for k := 1; k < s.Len; k++ {
					if s.Value(k) < s.Value(k-1) {
						t.Fatalf("counter %s decreases: %v", name, s.Labels)
					}
				}
			}
		}
		if name == "kube_node_labels" {
			if it := s.Label("label_node_kubernetes_io_instance_type"); it != "m6i.xlarge" && it != "c6a.2xlarge" {
				t.Errorf("instance type %q", it)
			}
			if r := s.Label("label_topology_kubernetes_io_region"); r != "eu-west-1" {
				t.Errorf("region %q", r)
			}
		}
	}
	for m := range contract {
		if seen[m] == 0 {
			t.Errorf("no %s series emitted", m)
		}
	}
	for p := range cadvisorPods {
		if !pods[p] {
			t.Errorf("pod %s has cAdvisor series but no kube_pod_owner", p)
		}
	}
	for _, s := range all {
		if s.Name() != "kube_pod_owner" {
			continue
		}
		key := s.Label("cluster") + "/" + s.Label("namespace") + "/" + s.Label("owner_name")
		switch s.Label("owner_kind") {
		case "ReplicaSet":
			if !rs[key] {
				t.Errorf("ReplicaSet %s has no kube_replicaset_owner", key)
			}
		case "Job":
			if !jobs[key] {
				t.Errorf("Job %s has no kube_job_owner", key)
			}
		}
	}
}

func TestWorkloadLabelOption(t *testing.T) {
	g := smallGenerator(t, 1)
	g.Clusters[0].Workloads = g.Clusters[0].Workloads[:1]
	for _, s := range collect(t, g) {
		if s.Name() == "container_cpu_usage_seconds_total" && s.Label("workload") != "synth-flat-0" {
			t.Fatalf("workloadLabel: true should add workload label: %v", s.Labels)
		}
	}
}

func digest(series []*Series) uint64 {
	lines := make([]string, len(series))
	for i, s := range series {
		lines[i] = string(AppendJSONLine(nil, s, Timeline{Step: time.Minute}))
	}
	sort.Strings(lines)
	h := fnv.New64a()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return h.Sum64()
}

func TestDeterministic(t *testing.T) {
	a := digest(collect(t, smallGenerator(t, 2)))
	b := digest(collect(t, smallGenerator(t, 2)))
	if a != b {
		t.Fatal("same seed produced different series")
	}
	sc, _ := LoadScenario("../scenarios/fleet-small.yaml")
	seed := int64(7)
	g, err := New(sc, Options{End: testEnd, Days: 2, Seed: &seed, Clusters: []string{"synth-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if digest(collect(t, g)) == a {
		t.Fatal("different seed produced identical series")
	}
}

func TestTimelineAligned(t *testing.T) {
	end := time.Date(2026, 10, 1, 12, 34, 56, 0, time.UTC)
	tl := NewTimeline(end, 24*time.Hour, time.Minute)
	if tl.End() != time.Date(2026, 10, 1, 12, 34, 0, 0, time.UTC) || tl.N != 1441 || tl.Ms(0)%60000 != 0 {
		t.Fatalf("timeline %+v end %v", tl, tl.End())
	}
}

func TestClip(t *testing.T) {
	var got []*Series
	emit := Clip(func(s *Series) error { got = append(got, s); return nil }, 10, 20)
	emit(&Series{Start: 5, Len: 10, Values: []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}})
	emit(&Series{Start: 18, Len: 5, Const: 1})
	emit(&Series{Start: 25, Len: 5, Const: 1})
	if len(got) != 2 || got[0].Start != 10 || got[0].Len != 5 || got[0].Values[0] != 5 || got[1].Start != 18 || got[1].Len != 2 {
		t.Fatalf("clip: %+v %+v", got[0], got[1])
	}
}

func TestJSONLine(t *testing.T) {
	tl := Timeline{Start: time.Unix(1000, 0), Step: time.Minute, N: 10}
	s := &Series{Labels: []Label{{"__name__", "m"}, {"a", `x"y`}}, Start: 2, Len: 3, Values: []float64{1, 2.5, 3e9}}
	var got struct {
		Metric     map[string]string `json:"metric"`
		Values     []float64         `json:"values"`
		Timestamps []int64           `json:"timestamps"`
	}
	if err := json.Unmarshal(AppendJSONLine(nil, s, tl), &got); err != nil {
		t.Fatal(err)
	}
	if got.Metric["a"] != `x"y` || got.Values[1] != 2.5 || got.Values[2] != 3e9 || got.Timestamps[0] != 1000_000+120_000 {
		t.Fatalf("%+v", got)
	}
}

// TestRemoteWrite decodes the protobuf by hand and checks labels, values and timestamps.
func TestRemoteWrite(t *testing.T) {
	tl := Timeline{Start: time.Unix(1700000000, 0), Step: time.Minute, N: 100}
	in := []*Series{
		{Labels: []Label{{"__name__", "m1"}, {"cluster", "synth-a"}}, Start: 3, Len: 2, Values: []float64{1.5, 2}},
		{Labels: []Label{{"__name__", "m2"}}, Start: 0, Len: 3, Const: 7},
	}
	var b []byte
	for _, s := range in {
		b = AppendRemoteWrite(b, s, tl)
	}
	b, _ = snappy.Decode(nil, snappy.Encode(nil, b))
	type sample struct {
		v  float64
		ts int64
	}
	var got []struct {
		labels  []Label
		samples []sample
	}
	readLen := func(p []byte) ([]byte, []byte) {
		n, k := binary.Uvarint(p)
		return p[k : k+int(n)], p[k+int(n):]
	}
	for len(b) > 0 {
		if b[0] != 0x0a {
			t.Fatalf("tag %x", b[0])
		}
		var ts []byte
		ts, b = readLen(b[1:])
		var entry struct {
			labels  []Label
			samples []sample
		}
		for len(ts) > 0 {
			tag := ts[0]
			var m []byte
			m, ts = readLen(ts[1:])
			switch tag {
			case 0x0a:
				name, rest := readLen(m[1:])
				val, _ := readLen(rest[1:])
				entry.labels = append(entry.labels, Label{string(name), string(val)})
			case 0x12:
				v := math.Float64frombits(binary.LittleEndian.Uint64(m[1:9]))
				x, _ := binary.Uvarint(m[10:])
				entry.samples = append(entry.samples, sample{v, int64(x)})
			default:
				t.Fatalf("unexpected tag %x", tag)
			}
		}
		got = append(got, entry)
	}
	if len(got) != 2 || got[0].labels[1].Value != "synth-a" || got[0].samples[0] != (sample{1.5, 1700000000_000 + 180_000}) ||
		len(got[1].samples) != 3 || got[1].samples[2].v != 7 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestFleetMix(t *testing.T) {
	sc, err := LoadScenario("../scenarios/fleet-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(sc, Options{End: testEnd})
	if err != nil {
		t.Fatal(err)
	}
	for _, cm := range g.Clusters {
		profiles, kinds := map[string]int{}, map[string]int{}
		names := map[string]bool{}
		for _, w := range cm.Workloads {
			profiles[w.Profile]++
			kinds[w.Kind]++
			k := w.Namespace + "/" + w.Name
			if names[k] {
				t.Errorf("%s: duplicate %s", cm.Spec.Name, k)
			}
			names[k] = true
			if strings.HasPrefix(w.Name, "synth-") {
				t.Errorf("fleet name %q looks synthetic", w.Name)
			}
		}
		if len(cm.Workloads) < 200 {
			t.Errorf("%s: %d workloads", cm.Spec.Name, len(cm.Workloads))
		}
		if profiles[ProfileOverProvisioned] < len(cm.Workloads)*35/100 {
			t.Errorf("%s: over-provisioned should dominate: %v", cm.Spec.Name, profiles)
		}
		for _, p := range []string{ProfileRightSized, ProfileThrottled, ProfileOOMProne, ProfileHPACoupled, ProfileJVM, ProfileBatch, ProfileDaemon} {
			if profiles[p] == 0 {
				t.Errorf("%s: no %s workloads: %v", cm.Spec.Name, p, profiles)
			}
		}
		for _, k := range []string{KindDeployment, KindStatefulSet, KindDaemonSet, KindCronJob} {
			if kinds[k] == 0 {
				t.Errorf("%s: no %s: %v", cm.Spec.Name, k, kinds)
			}
		}
		types := map[string]bool{}
		for _, n := range cm.Nodes {
			types[n.InstanceType+"/"+n.CapacityType] = true
		}
		if len(types) != 2 {
			t.Errorf("%s: node types %v", cm.Spec.Name, types)
		}
	}
}

// TestFleetProfilesBehave checks generated profiles produce their intended risk signals and that
// only oom-prone workloads get OOM-killed.
func TestFleetProfilesBehave(t *testing.T) {
	sc, err := LoadScenario("../scenarios/fleet-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(sc, Options{End: testEnd, Days: 7, Clusters: []string{"synth-a"}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	ooms := map[string]int{}
	throttled := map[string][]float64{}
	g.Each(4, func(wt *WorkloadTrace) error {
		n := 0
		var thr []float64
		for _, p := range wt.Pods {
			for _, c := range p.Containers {
				for _, in := range c.Instances {
					if in.EndReason == "OOMKilled" {
						n++
					}
				}
			}
			thr = append(thr, p.Containers[0].Throttled...)
		}
		mu.Lock()
		defer mu.Unlock()
		ooms[wt.Spec.Profile] += n
		if len(thr) > 0 {
			throttled[wt.Spec.Profile] = append(throttled[wt.Spec.Profile], quantile(thr, 0.95))
		}
		return nil
	})
	for p, n := range ooms {
		if p != ProfileOOMProne && n > 0 {
			t.Errorf("%d OOM kills in profile %s", n, p)
		}
	}
	if ooms[ProfileOOMProne] == 0 {
		t.Error("oom-prone workloads never OOM")
	}
	if m := quantile(throttled[ProfileThrottled], 0.5); m < 0.25 {
		t.Errorf("median throttled-profile p95 throttle ratio = %.2f", m)
	}
	if m := quantile(throttled[ProfileOverProvisioned], 0.9); m > 0.05 {
		t.Errorf("over-provisioned workloads throttled: %.2f", m)
	}
}

func TestRevImage(t *testing.T) {
	for _, tc := range []struct {
		in   string
		rev  int
		want string
	}{
		{"r/ns/app:1.2.3", 0, "r/ns/app:1.2.3"},
		{"r/ns/app:1.2.3", 2, "r/ns/app:1.2.5"},
		{"r/ns/app:1.2.3-temurin21-jre", 1, "r/ns/app:1.2.4-temurin21-jre"},
		{"localhost:5000/app:latest", 1, "localhost:5000/app:latest"},
	} {
		if got := revImage(tc.in, tc.rev, false); got != tc.want {
			t.Errorf("revImage(%q, %d) = %q, want %q", tc.in, tc.rev, got, tc.want)
		}
	}
}

func TestValuesJSON(t *testing.T) {
	b, _ := json.Marshal(Values{1, math.NaN(), 0.123456789})
	if string(b) != "[1,null,0.123457]" {
		t.Fatalf("%s", b)
	}
	var v Values
	if err := json.Unmarshal(b, &v); err != nil || !math.IsNaN(v[1]) || v[0] != 1 {
		t.Fatalf("%v %v", v, err)
	}
}

func TestDescribeExpectations(t *testing.T) {
	// Every explicit pattern workload states what a recommender must conclude.
	sc, err := LoadScenario("../scenarios/fleet-small.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range sc.Clusters[0].Workloads {
		if w.Expect == nil || w.Expect.CPU == "" || w.Expect.Memory == "" {
			t.Errorf("%s: no expectation", w.Name)
		}
	}
}

// TestExtendIsSeamless: extending an anchored timeline (live mode) must reproduce every earlier
// sample exactly, so continuing a backfill never rewrites history or resets counters.
func TestExtendIsSeamless(t *testing.T) {
	load := func(end time.Time) *Generator {
		sc, err := LoadScenario("../scenarios/fleet-small.yaml")
		if err != nil {
			t.Fatal(err)
		}
		g, err := New(sc, Options{Start: testEnd.Add(-3 * 24 * time.Hour), Anchor: testEnd, End: end, Clusters: []string{"synth-a"}})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	base := load(testEnd)
	longer := load(testEnd.Add(7 * time.Hour))
	if longer.Timeline.N != base.Timeline.N+7*60 || longer.Timeline.Start != base.Timeline.Start {
		t.Fatalf("timelines %+v %+v", base.Timeline, longer.Timeline)
	}
	clip := func(g *Generator, n int) []*Series {
		var out []*Series
		for _, s := range collect(t, g) {
			if c := clipSeries(s, 0, n); c != nil {
				out = append(out, c)
			}
		}
		return out
	}
	a, b := clip(base, base.Timeline.N), clip(longer, base.Timeline.N)
	if len(a) != len(b) || digest(a) != digest(b) {
		t.Fatalf("extending the timeline changed history: %d vs %d series", len(a), len(b))
	}
	// The default fleet too (spikes, rollouts, evictions, cron jobs).
	sc, _ := LoadScenario("../scenarios/fleet-default.yaml")
	g1, _ := New(sc, Options{Start: testEnd.Add(-2 * 24 * time.Hour), Anchor: testEnd, End: testEnd, Clusters: []string{"synth-b"}})
	sc2, _ := LoadScenario("../scenarios/fleet-default.yaml")
	g2, _ := New(sc2, Options{Start: testEnd.Add(-2 * 24 * time.Hour), Anchor: testEnd, End: testEnd.Add(90 * time.Minute), Clusters: []string{"synth-b"}})
	if digest(clip(g1, g1.Timeline.N)) != digest(clip(g2, g1.Timeline.N)) {
		t.Fatal("fleet-default: extending the timeline changed history")
	}
}

func clipSeries(s *Series, from, to int) *Series {
	var out *Series
	Clip(func(c *Series) error { out = c; return nil }, from, to)(s)
	return out
}
