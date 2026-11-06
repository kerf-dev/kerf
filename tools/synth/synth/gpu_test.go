package synth

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func gpuGenerator(t *testing.T, days float64) *Generator {
	t.Helper()
	sc, err := LoadScenario("../scenarios/fleet-gpu.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(sc, Options{End: testEnd, Days: days})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func workloadSpec(t *testing.T, g *Generator, name string) *WorkloadSpec {
	t.Helper()
	for _, w := range g.Clusters[0].Workloads {
		if w.Name == name {
			return w
		}
	}
	t.Fatalf("workload %s not found", name)
	return nil
}

// gpuAvg is the workload's GPU utilisation averaged over all GPUs of all running pods, per sample
// (NaN when no pod runs), and the framebuffer used on the busiest GPU.
func gpuAvg(wt *WorkloadTrace, ci int, n int) (util, usedMax []float64) {
	sum, cnt := make([]float64, n), make([]float64, n)
	usedMax = make([]float64, n)
	for _, p := range wt.Pods {
		ct := p.Containers[ci]
		for d := range ct.GPUUtil {
			for k, u := range ct.GPUUtil[d] {
				sum[p.Start+k] += u
				cnt[p.Start+k]++
				usedMax[p.Start+k] = math.Max(usedMax[p.Start+k], ct.GPUUsedMiB[d][k])
			}
		}
	}
	util = make([]float64, 0, n)
	for i := range sum {
		if cnt[i] > 0 {
			util = append(util, sum[i]/cnt[i])
		}
	}
	return util, usedMax
}

func TestGPUScenarioValidation(t *testing.T) {
	const wl = `{name: a, namespace: n, containers: [{name: c, gpu: %s}]}`
	pools := `nodePools: [{instanceType: m6i.xlarge, count: 1}, {instanceType: g5.xlarge}]`
	ok := fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{perPod: 1}`)
	if _, err := ParseScenario([]byte(ok)); err != nil {
		t.Fatalf("valid GPU scenario rejected: %v", err)
	}
	for _, tc := range []struct{ name, doc, want string }{
		{"perPod too large", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{perPod: 9}`), "gpu.perPod"},
		{"no pool for the GPUs", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{perPod: 4}`), "no node pool has a GPU instance type"},
		{"no GPU pool", `clusters: [{name: synth-x, workloads: [{name: a, namespace: n, containers: [{name: c, gpu: {perPod: 1}}]}]}]`, "no node pool has a GPU instance type"},
		{"only GPU pools", `clusters: [{name: synth-x, nodePools: [{instanceType: g5.xlarge}], workloads: [{name: a, namespace: n, containers: [{name: c}]}]}]`, "without GPUs need a node pool"},
		{"pattern", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{utilization: {pattern: wobbly}}`), "gpu.utilization.pattern"},
		{"p95", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{utilization: {p95: 120}}`), "gpu.utilization.p95"},
		{"label style", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{labelStyle: honor}`), "gpu.labelStyle"},
		{"memory both", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{memoryUsed: {fraction: 0.2, mib: 100}}`), "either fraction or mib"},
		{"memory over total", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{memoryMiB: 1000, memoryUsed: {mib: 2000}}`), "gpu.memoryUsed"},
		{"expect status", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{expect: {status: broken}}`), "gpu.expect.status"},
		{"expect suggestion", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{expect: {status: idle, suggestions: [reboot]}}`), "unknown kind"},
		{"daemonset", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [{name: a, namespace: n, kind: DaemonSet, containers: [{name: c, gpu: {}}]}]}]`, pools), "DaemonSets"},
		{"init container", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [{name: a, namespace: n, containers: [{name: c}], initContainers: [{name: i, gpu: {}}]}]}]`, pools), "init containers"},
		{"unknown field", fmt.Sprintf(`clusters: [{name: synth-x, %s, workloads: [`+wl+`]}]`, pools, `{count: 2}`), "count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseScenario([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
	// A pool with a count that is too small for the pods is an error at expansion time.
	sc, err := ParseScenario([]byte(`clusters: [{name: synth-x, nodePools: [{instanceType: m6i.xlarge, count: 1}, {instanceType: g5.xlarge, count: 1}],
  workloads: [{name: a, namespace: n, replicas: 2, containers: [{name: c, gpu: {}}]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(sc, Options{End: testEnd, Days: 1}); err == nil || !strings.Contains(err.Error(), "needs 2 g5.xlarge nodes") {
		t.Fatalf("got %v, want a node shortage error", err)
	}
}

func TestGPUDefaults(t *testing.T) {
	sc, err := ParseScenario([]byte(`clusters: [{name: synth-x, nodePools: [{instanceType: m6i.xlarge}, {instanceType: g5.xlarge}],
  workloads: [{name: a, namespace: n, containers: [{name: c, gpu: {utilization: {pattern: busy}}}]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	gs := sc.Clusters[0].Workloads[0].Containers[0].GPU
	if gs.PerPod != 1 || gs.Model != "NVIDIA A10G" || gs.MemoryMiB != 24576 || gs.LabelStyle != "pod" || !gs.DCGMEnabled() ||
		gs.Utilization.P95 != 85 || gs.MemoryUsed.Fraction != 0.75 {
		t.Fatalf("defaults: %+v", gs)
	}
}

func TestGPUNodes(t *testing.T) {
	g := gpuGenerator(t, 14)
	cm := g.Clusters[0]
	byType := map[string]int{}
	gpuNodes := map[*Node]bool{}
	for _, n := range cm.Nodes {
		byType[n.InstanceType]++
		if n.Type.GPUs > 0 {
			gpuNodes[n] = true
		}
	}
	// 5 single-GPU pods (1 + 2 + 2 + the notebook) and one 4-GPU pod, one pod per node.
	if byType["g5.xlarge"] != 6 || byType["g5.12xlarge"] != 1 || byType["m6i.xlarge"] != 2 {
		t.Fatalf("nodes %v", byType)
	}
	var nodeSeries []*Series
	got := map[string]map[string]float64{} // node -> metric/resource -> value
	for _, s := range collect(t, g) {
		switch s.Name() {
		case "kube_node_status_allocatable", "kube_node_status_capacity":
			if s.Label("resource") != "nvidia_com_gpu" {
				continue
			}
			nodeSeries = append(nodeSeries, s)
			if s.Label("unit") != "integer" {
				t.Errorf("GPU node resource unit %q", s.Label("unit"))
			}
			if got[s.Label("node")] == nil {
				got[s.Label("node")] = map[string]float64{}
			}
			got[s.Label("node")][s.Name()] = s.Value(0)
		}
	}
	if len(nodeSeries) != 2*7 {
		t.Fatalf("%d GPU node series, want 14", len(nodeSeries))
	}
	for _, n := range cm.Nodes {
		want := float64(n.Type.GPUs)
		if n.Type.GPUs == 0 {
			if got[n.Name] != nil {
				t.Errorf("CPU node %s has GPU resources", n.Name)
			}
			continue
		}
		for _, m := range []string{"kube_node_status_allocatable", "kube_node_status_capacity"} {
			if got[n.Name][m] != want {
				t.Errorf("%s %s nvidia_com_gpu = %v, want %v", n.Name, m, got[n.Name][m], want)
			}
		}
		if (n.InstanceType == "g5.xlarge") != (want == 1) || (n.InstanceType == "g5.12xlarge") != (want == 4) {
			t.Errorf("%s has %v GPUs", n.InstanceType, want)
		}
	}
	// GPU nodes run GPU pods only, at most one at a time; GPU pods run on GPU nodes only.
	g.Each(2, func(wt *WorkloadTrace) error { //nolint:errcheck // never fails
		isGPU := wt.Spec.GPUsPerPod() > 0
		for _, p := range wt.Pods {
			if gpuNodes[p.Node] != isGPU {
				t.Errorf("%s pod %s on %s (GPU node: %v)", wt.Spec.Name, p.Name, p.Node.InstanceType, gpuNodes[p.Node])
			}
			if isGPU && p.Node.Type.GPUs < wt.Spec.GPUsPerPod() {
				t.Errorf("%s: %d GPUs requested, node %s has %d", wt.Spec.Name, wt.Spec.GPUsPerPod(), p.Node.InstanceType, p.Node.Type.GPUs)
			}
		}
		return nil
	})
	byNode := map[string]string{}
	for _, cmw := range cm.Workloads {
		if cmw.GPUsPerPod() == 0 {
			continue
		}
		for j := 0; j < cmw.Replicas; j++ {
			n := cm.gpuNode(cmw, j)
			if prev, ok := byNode[n.Name]; ok {
				t.Errorf("node %s holds %s and %s", n.Name, prev, cmw.Name)
			}
			byNode[n.Name] = cmw.Name
		}
	}
	if n := cm.gpuNode(workloadSpec(t, g, "batch-4gpu-light"), 0); n.InstanceType != "g5.12xlarge" {
		t.Errorf("4-GPU pod on %s", n.InstanceType)
	}
}

var gpuUUID = regexp.MustCompile(`^GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestGPUSeries(t *testing.T) {
	g := gpuGenerator(t, 3)
	all := collect(t, g)
	pods := map[string]string{} // namespace/pod -> node (kube_pod_info)
	for _, s := range all {
		if s.Name() == "kube_pod_info" {
			pods[s.Label("namespace")+"/"+s.Label("pod")] = s.Label("node")
		}
	}
	type key struct{ workload, pod string }
	dcgm := map[key]map[string]map[string]*Series{} // workload/pod -> gpu -> metric -> series
	uuids := map[string]string{}                    // node/gpu -> UUID
	requests := map[string]*Series{}                // pod -> requests series
	limits := map[string]*Series{}
	for _, s := range all {
		name := s.Name()
		if strings.Contains(name, "nvidia") && !strings.HasPrefix(name, "DCGM") && s.Label("resource") != "" && s.Label("resource") != "nvidia_com_gpu" {
			t.Errorf("unexpected series %v", s.Labels)
		}
		switch {
		case name == "kube_pod_container_resource_requests" && s.Label("resource") == "nvidia_com_gpu":
			requests[s.Label("pod")] = s
		case name == "kube_pod_container_resource_limits" && s.Label("resource") == "nvidia_com_gpu":
			limits[s.Label("pod")] = s
		case strings.HasPrefix(name, "DCGM_FI_DEV_"):
			if s.Label("cluster") != "synth-gpu" {
				t.Errorf("%s cluster %q", name, s.Label("cluster"))
			}
			ns, pod, container := s.Label("namespace"), s.Label("pod"), s.Label("container")
			if s.Label("exported_pod") != "" {
				if ns != "gpu-operator" || !strings.HasPrefix(pod, "nvidia-dcgm-exporter-") || container != "nvidia-dcgm-exporter" {
					t.Errorf("exported style: exporter labels %v", s.Labels)
				}
				ns, pod, container = s.Label("exported_namespace"), s.Label("exported_pod"), s.Label("exported_container")
				if ns == "" || container == "" {
					t.Errorf("exported style without namespace/container: %v", s.Labels)
				}
			} else if s.Label("exported_namespace") != "" || s.Label("exported_container") != "" {
				t.Errorf("pod style with exported labels: %v", s.Labels)
			}
			node, ok := pods[ns+"/"+pod]
			if !ok {
				t.Errorf("%s of pod %s/%s that has no kube_pod_info", name, ns, pod)
			}
			if s.Label("Hostname") != node {
				t.Errorf("Hostname %q, pod runs on %q", s.Label("Hostname"), node)
			}
			if !gpuUUID.MatchString(s.Label("UUID")) {
				t.Errorf("UUID %q", s.Label("UUID"))
			}
			gpu := s.Label("gpu")
			if s.Label("device") != "nvidia"+gpu || s.Label("modelName") != "NVIDIA A10G" || s.Label("job") != "nvidia-dcgm-exporter" || !strings.HasSuffix(s.Label("instance"), ":9400") {
				t.Errorf("labels %v", s.Labels)
			}
			if id := node + "/" + gpu; uuids[id] != "" && uuids[id] != s.Label("UUID") {
				t.Errorf("GPU %s has UUIDs %s and %s", id, uuids[id], s.Label("UUID"))
			} else {
				uuids[id] = s.Label("UUID")
			}
			k := key{ns + "/" + container, pod}
			if dcgm[k] == nil {
				dcgm[k] = map[string]map[string]*Series{}
			}
			if dcgm[k][gpu] == nil {
				dcgm[k][gpu] = map[string]*Series{}
			}
			dcgm[k][gpu][name] = s
		}
	}
	if len(uuids) == 0 {
		t.Fatal("no DCGM series")
	}
	seenUUID := map[string]string{}
	for id, u := range uuids {
		if prev, ok := seenUUID[u]; ok {
			t.Errorf("UUID %s of both %s and %s", u, prev, id)
		}
		seenUUID[u] = id
	}
	// Requests and limits are per pod: GPUs per pod, same labels as the cpu series.
	want := map[string]int{"trainer-idle": 1, "inference-light": 1, "inference-busy": 1, "batch-4gpu-light": 4, "notebook-nodata": 1}
	perPod := map[string]int{}
	for pod, s := range requests {
		for _, l := range []string{"cluster", "namespace", "pod", "uid", "container", "node", "unit"} {
			if s.Label(l) == "" {
				t.Errorf("requests of %s lack %s: %v", pod, l, s.Labels)
			}
		}
		if s.Label("unit") != "integer" || s.Label("resource") != "nvidia_com_gpu" {
			t.Errorf("requests labels %v", s.Labels)
		}
		var wl string
		for name := range want {
			if strings.HasPrefix(pod, name+"-") {
				wl = name
			}
		}
		if wl == "" || s.Const != float64(want[wl]) || s.Values != nil {
			t.Errorf("pod %s requests %v GPUs (%v)", pod, s.Const, s.Labels)
		}
		if l := limits[pod]; l == nil || l.Const != s.Const || l.Label("node") != s.Label("node") || l.Label("uid") != s.Label("uid") {
			t.Errorf("pod %s: limits %v, requests %v", pod, l, s)
		}
		perPod[wl]++
		// The same pod has CPU requests too.
	}
	if len(limits) != len(requests) || len(perPod) != len(want) {
		t.Errorf("%d requests, %d limits, workloads %v", len(requests), len(limits), perPod)
	}
	// DCGM: one series per GPU per pod; none for notebook-nodata.
	gpusOfPod := map[string]int{}
	for k, gpus := range dcgm {
		for gpu, ms := range gpus {
			if len(ms) != 3 {
				t.Errorf("%v gpu %s has metrics %d, want util, used and free", k, gpu, len(ms))
				continue
			}
			u, used, free := ms["DCGM_FI_DEV_GPU_UTIL"], ms["DCGM_FI_DEV_FB_USED"], ms["DCGM_FI_DEV_FB_FREE"]
			if u == nil || used == nil || free == nil || u.Len != used.Len || used.Len != free.Len || u.Start != used.Start {
				t.Fatalf("%v gpu %s: mismatched series", k, gpu)
			}
			for i := 0; i < u.Len; i++ {
				if v := u.Value(i); v < 0 || v > 100 || v != math.Round(v) {
					t.Fatalf("%v util %v", k, v)
				}
				if used.Value(i)+free.Value(i) != 24576 || used.Value(i) < 0 || free.Value(i) < 0 || used.Value(i) != math.Round(used.Value(i)) {
					t.Fatalf("%v used %v + free %v != 24576", k, used.Value(i), free.Value(i))
				}
			}
		}
		gpusOfPod[k.pod] = len(gpus)
		for _, name := range []string{"notebook-nodata"} {
			if strings.HasPrefix(k.pod, name) {
				t.Errorf("DCGM series for %s", k.pod)
			}
		}
	}
	for pod, n := range gpusOfPod {
		for name, w := range want {
			if strings.HasPrefix(pod, name+"-") && n != w {
				t.Errorf("pod %s: %d GPU series sets, want %d", pod, n, w)
			}
		}
		if n == 4 { // gpu 0..3
			for _, gpu := range []string{"0", "1", "2", "3"} {
				found := false
				for k, gpus := range dcgm {
					if k.pod == pod && gpus[gpu] != nil {
						found = true
					}
				}
				if !found {
					t.Errorf("pod %s has no gpu=%s", pod, gpu)
				}
			}
		}
	}
	// Every pod that requests GPUs and is not the no-DCGM one has DCGM series.
	for pod := range requests {
		if strings.HasPrefix(pod, "notebook-nodata-") {
			continue
		}
		if gpusOfPod[pod] == 0 {
			t.Errorf("pod %s requests GPUs but has no DCGM series", pod)
		}
	}
	// Label styles: inference-light is `exported`, the others `pod`.
	for _, s := range all {
		if s.Name() != "DCGM_FI_DEV_GPU_UTIL" {
			continue
		}
		exported := s.Label("exported_pod") != ""
		pod := s.Label("pod")
		if !exported {
			// pod style: the pod label is the workload's
			if strings.HasPrefix(pod, "inference-light-") {
				t.Errorf("inference-light should use exported labels: %v", s.Labels)
			}
		} else if !strings.HasPrefix(s.Label("exported_pod"), "inference-light-") {
			t.Errorf("exported labels on %v", s.Labels)
		}
	}
}

func TestGPUGroundTruth(t *testing.T) {
	g := gpuGenerator(t, 2)
	found := map[string]*Series{}
	for _, s := range collect(t, g) {
		if s.Name() == "synth_gpu_workload_info" {
			found[s.Label("workload")] = s
		}
	}
	if len(found) != 5 {
		t.Fatalf("%d synth_gpu_workload_info series, want 5", len(found))
	}
	for name, want := range map[string][4]string{
		"trainer-idle":     {"idle", "scale-to-zero", "1", "true"},
		"inference-light":  {"under-used", "time-slicing", "1", "true"},
		"inference-busy":   {"ok", "", "1", "true"},
		"batch-4gpu-light": {"under-used", "time-slicing", "4", "true"},
		"notebook-nodata":  {"no-data", "", "1", "false"},
	} {
		s := found[name]
		if s == nil {
			t.Errorf("no ground truth for %s", name)
			continue
		}
		if s.Label("gpu_status") != want[0] || s.Label("gpu_suggestions") != want[1] || s.Label("gpus_per_pod") != want[2] || s.Label("dcgm") != want[3] {
			t.Errorf("%s: %v", name, s.Labels)
		}
	}
}

func TestGPUUtilizationPatterns(t *testing.T) {
	g := gpuGenerator(t, 14)
	n := g.Timeline.N
	stats := func(name string) (p95, avg, maxv, memFrac float64) {
		w := workloadSpec(t, g, name)
		wt := g.Simulate(g.Clusters[0], w)
		ci := w.gpuContainers()[0]
		util, used := gpuAvg(wt, ci, n)
		_, maxv = minMax(util)
		var memP []float64
		for _, u := range used {
			if u > 0 {
				memP = append(memP, u)
			}
		}
		return quantile(util, 0.95), mean(util), maxv, quantile(memP, 0.95) / w.Containers[ci].GPU.MemoryMiB
	}
	// idle: ~0-2 %, rare small blips that do not move the 95th percentile.
	p95, avg, maxv, mem := stats("trainer-idle")
	if p95 > 2 || avg > 1.5 || maxv < 3 || maxv > 12 || mem > 0.08 {
		t.Errorf("idle: p95 %.1f avg %.2f max %.1f mem %.2f", p95, avg, maxv, mem)
	}
	// light: diurnal around 25 %, memory used ~20 %.
	p95, avg, maxv, mem = stats("inference-light")
	if p95 < 22 || p95 > 28 || avg < 10 || avg > 20 || maxv > 40 || mem < 0.18 || mem > 0.23 {
		t.Errorf("light: p95 %.1f avg %.1f max %.1f mem %.2f", p95, avg, maxv, mem)
	}
	// busy: diurnal around 80 %.
	p95, avg, maxv, mem = stats("inference-busy")
	if p95 < 74 || p95 > 86 || avg < 55 || maxv > 100 || mem < 0.65 || mem > 0.75 {
		t.Errorf("busy: p95 %.1f avg %.1f max %.1f mem %.2f", p95, avg, maxv, mem)
	}
	// The four GPUs of the batch pod run light too.
	p95, _, _, _ = stats("batch-4gpu-light")
	if p95 < 22 || p95 > 28 {
		t.Errorf("batch-4gpu-light: p95 %.1f", p95)
	}
	// Diurnal: afternoon above night (light).
	w := workloadSpec(t, g, "inference-light")
	wt := g.Simulate(g.Clusters[0], w)
	var day, night []float64
	for _, p := range wt.Pods {
		for k, u := range p.Containers[0].GPUUtil[0] {
			switch h := g.Timeline.Start.Add(time.Duration(p.Start+k) * g.Timeline.Step).UTC().Hour(); {
			case h >= 13 && h <= 15:
				day = append(day, u)
			case h >= 1 && h <= 3:
				night = append(night, u)
			}
		}
	}
	if mean(day) < 1.5*mean(night) {
		t.Errorf("light GPU util not diurnal: day %.1f night %.1f", mean(day), mean(night))
	}
}

func minMax(xs []float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, x := range xs {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return lo, hi
}

func TestGPUDeterministic(t *testing.T) {
	a := digest(collect(t, gpuGenerator(t, 2)))
	if b := digest(collect(t, gpuGenerator(t, 2))); a != b {
		t.Fatal("same seed produced different GPU series")
	}
	sc, _ := LoadScenario("../scenarios/fleet-gpu.yaml")
	seed := int64(7)
	g, err := New(sc, Options{End: testEnd, Days: 2, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if digest(collect(t, g)) == a {
		t.Fatal("different seed produced identical GPU series")
	}
}

// Extending the timeline (live mode) must keep the GPU history too.
func TestGPUExtendIsSeamless(t *testing.T) {
	load := func(end time.Time) *Generator {
		sc, err := LoadScenario("../scenarios/fleet-gpu.yaml")
		if err != nil {
			t.Fatal(err)
		}
		g, err := New(sc, Options{Start: testEnd.Add(-3 * 24 * time.Hour), Anchor: testEnd, End: end})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	base, longer := load(testEnd), load(testEnd.Add(7*time.Hour))
	clip := func(g *Generator) []*Series {
		var out []*Series
		for _, s := range collect(t, g) {
			if c := clipSeries(s, 0, base.Timeline.N); c != nil {
				out = append(out, c)
			}
		}
		return out
	}
	a, b := clip(base), clip(longer)
	if len(a) != len(b) || digest(a) != digest(b) {
		t.Fatalf("extending the timeline changed GPU history: %d vs %d series", len(a), len(b))
	}
}

// The existing scenarios have no GPUs: no GPU series, no GPU nodes.
func TestNoGPUWithoutOption(t *testing.T) {
	for _, s := range collect(t, smallGenerator(t, 1)) {
		n := s.Name()
		if strings.HasPrefix(n, "DCGM_") || n == "synth_gpu_workload_info" || strings.Contains(strings.Join(labelValues(s), " "), "nvidia") {
			t.Fatalf("GPU series without a GPU option: %v", s.Labels)
		}
	}
	for _, it := range InstanceTypes {
		if it.GPUs > 0 && it.GPUModel == "" {
			t.Errorf("%+v has GPUs but no model", it)
		}
	}
	_ = strconv.Itoa
}

func labelValues(s *Series) []string {
	out := make([]string, len(s.Labels))
	for i, l := range s.Labels {
		out[i] = l.Value
	}
	return out
}
