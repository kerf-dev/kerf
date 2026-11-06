//go:build e2e

package e2e

import (
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"testing"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// The synthetic GPU cluster (tools/synth/scenarios/fleet-gpu.yaml): `make synth-backfill-gpu`
// loads 30 days of history for it into VictoriaMetrics and keeps it current; it has to be
// registered in the hub (datasource `local`, cluster name synth-gpu) like any other cluster.
const gpuCluster = "synth-gpu"

// gpuWant is what the scenario's workloads must come out as.
type gpuWant struct {
	name        string
	status      string
	suggestions []string // kinds that must be present (none: no suggestion at all)
	gpusPerPod  int
	pods        int
	hasDCGM     bool
	utilP95     [2]float64 // percent, inclusive range; only with DCGM
	memP95      [2]float64 // percent of the GPU's memory
}

var gpuWorkloads = []gpuWant{
	{name: "trainer-idle", status: hubapi.GPUIdle, suggestions: []string{hubapi.GPUSuggestScaleToZero}, gpusPerPod: 1, pods: 1, hasDCGM: true,
		utilP95: [2]float64{0, 5}, memP95: [2]float64{0, 15}},
	{name: "inference-light", status: hubapi.GPUUnderUsed, suggestions: []string{hubapi.GPUSuggestTimeSlicing}, gpusPerPod: 1, pods: 2, hasDCGM: true,
		utilP95: [2]float64{15, 35}, memP95: [2]float64{15, 25}},
	{name: "inference-busy", status: hubapi.GPUOK, gpusPerPod: 1, pods: 2, hasDCGM: true,
		utilP95: [2]float64{70, 90}, memP95: [2]float64{60, 80}},
	{name: "batch-4gpu-light", status: hubapi.GPUUnderUsed, suggestions: []string{hubapi.GPUSuggestTimeSlicing}, gpusPerPod: 4, pods: 1, hasDCGM: true,
		utilP95: [2]float64{15, 35}, memP95: [2]float64{15, 35}},
	{name: "notebook-nodata", status: hubapi.GPUNoData, gpusPerPod: 1, pods: 1},
}

// Ordinary workloads of the same cluster: never listed with a GPU filter.
var gpuCPUOnly = []string{"model-gateway", "feature-cache", "embedding-export"}

// TestGPUVisibility: the hub reports GPU status, cost and suggestions of the synthetic GPU
// cluster (idle, under-used, ok, no-data), filters and counts them, and the DCGM readiness check
// passes. Read-only: it changes nothing in the hub or in any cluster. Skipped when synth-gpu is
// not registered in the hub.
func TestGPUVisibility(t *testing.T) {
	t.Parallel()
	viewer := as(t, hubapi.UserRoleViewer)
	if r := viewer.do(t, http.MethodGet, "/v1/clusters/"+url.PathEscape(gpuCluster)+"/readiness", nil, nil); r.Status == http.StatusNotFound {
		t.Skipf("cluster %s is not registered in the hub; load it with `make synth-backfill-gpu` and register it (datasource local) to run this test", gpuCluster)
	} else if r.Status != http.StatusOK {
		t.Fatalf("readiness of %s: %s", gpuCluster, r)
	}

	// A recommender run that sees the GPU series; without a recompute trigger the periodic run does.
	if env.Hub.recomputeNow(ctx(t), gpuCluster, t.Logf) {
		env.Hub.settle(t, gpuCluster)
	}
	var rows map[string]hubapi.WorkloadRow
	eventually(t, 10*time.Minute, 10*time.Second, "GPU status of all GPU workloads of "+gpuCluster, func() (bool, string) {
		env.Hub.maybeRecompute(ctx(t), gpuCluster, t.Logf)
		var list hubapi.WorkloadList
		if _, err := env.Hub.get(ctx(t), "/v1/workloads", gpuQuery(), &list); err != nil {
			return false, err.Error()
		}
		rows = map[string]hubapi.WorkloadRow{}
		for _, r := range list.Items {
			rows[r.Name] = r
		}
		for _, w := range gpuWorkloads {
			if r, ok := rows[w.name]; !ok || r.GPU == nil {
				return false, w.name + " has no GPU status yet (is `make synth-backfill-gpu` live mode running?)"
			}
		}
		return true, ""
	})

	t.Run("status", func(t *testing.T) {
		if len(rows) != len(gpuWorkloads) {
			t.Errorf("gpu=any lists %d workloads, want %d: %v", len(rows), len(gpuWorkloads), rowNames(rows))
		}
		for _, w := range gpuWorkloads {
			assertGPU(t, w, rows[w.name].GPU)
		}
	})

	t.Run("filter", func(t *testing.T) {
		for _, tc := range []struct {
			gpu  []string
			want []string
		}{
			{[]string{hubapi.GPUIdle}, []string{"trainer-idle"}},
			{[]string{hubapi.GPUUnderUsed}, []string{"batch-4gpu-light", "inference-light"}},
			{[]string{hubapi.GPUOK}, []string{"inference-busy"}},
			{[]string{hubapi.GPUNoData}, []string{"notebook-nodata"}},
			{[]string{hubapi.GPUIdle, hubapi.GPUNoData}, []string{"notebook-nodata", "trainer-idle"}},
			{[]string{hubapi.GPUAny}, []string{"batch-4gpu-light", "inference-busy", "inference-light", "notebook-nodata", "trainer-idle"}},
		} {
			q := url.Values{"cluster": {gpuCluster}, "limit": {"100"}, "gpu": tc.gpu}
			var list hubapi.WorkloadList
			if _, err := env.Hub.get(ctx(t), "/v1/workloads", q, &list); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range list.Items {
				got = append(got, r.Name)
				if r.GPU == nil || (tc.gpu[0] != hubapi.GPUAny && !slices.Contains(tc.gpu, r.GPU.Status)) {
					t.Errorf("gpu=%v lists %s with GPU %+v", tc.gpu, r.Name, r.GPU)
				}
			}
			sort.Strings(got)
			if !slices.Equal(got, tc.want) || list.Total != len(tc.want) {
				t.Errorf("gpu=%v: %v (total %d), want %v", tc.gpu, got, list.Total, tc.want)
			}
		}
		// Without the filter the cluster's ordinary workloads are listed, without GPU data.
		var all hubapi.WorkloadList
		if _, err := env.Hub.get(ctx(t), "/v1/workloads", url.Values{"cluster": {gpuCluster}, "limit": {"100"}}, &all); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, r := range all.Items {
			if slices.Contains(gpuCPUOnly, r.Name) {
				seen[r.Name] = true
				if r.GPU != nil {
					t.Errorf("%s has no GPUs but a GPU section: %+v", r.Name, r.GPU)
				}
			}
		}
		if len(seen) == 0 {
			t.Errorf("none of the CPU-only workloads %v is listed without the gpu filter", gpuCPUOnly)
		}
	})

	t.Run("facets", func(t *testing.T) {
		want := map[string]int{hubapi.GPUIdle: 1, hubapi.GPUUnderUsed: 2, hubapi.GPUOK: 1, hubapi.GPUNoData: 1}
		var f hubapi.WorkloadFacets
		if _, err := env.Hub.get(ctx(t), "/v1/workloads/facets", url.Values{"cluster": {gpuCluster}}, &f); err != nil {
			t.Fatal(err)
		}
		checkFacet(t, "no filter", f.GPU, want)
		if !sort.SliceIsSorted(f.GPU, func(i, j int) bool { return f.GPU[i].Value < f.GPU[j].Value }) {
			t.Errorf("gpu facet not sorted by value: %v", f.GPU)
		}
		// A facet is counted with every filter except its own: gpu=idle still shows all statuses,
		// while the total follows the filter.
		if _, err := env.Hub.get(ctx(t), "/v1/workloads/facets", url.Values{"cluster": {gpuCluster}, "gpu": {hubapi.GPUIdle}}, &f); err != nil {
			t.Fatal(err)
		}
		checkFacet(t, "gpu=idle", f.GPU, want)
		if f.Total != 1 {
			t.Errorf("gpu=idle: total %d, want 1", f.Total)
		}
	})

	t.Run("detail", func(t *testing.T) {
		for _, w := range gpuWorkloads {
			var d hubapi.WorkloadDetail
			if _, err := env.Hub.get(ctx(t), "/v1/workloads/"+url.PathEscape(rows[w.name].ID), nil, &d); err != nil {
				t.Fatal(err)
			}
			if d.Recommendation == nil || d.Recommendation.GPU == nil {
				t.Errorf("%s: detail has no recommendation.gpu", w.name)
				continue
			}
			assertGPU(t, w, d.Recommendation.GPU)
			if row := rows[w.name].GPU; row != nil && row.Status != d.Recommendation.GPU.Status {
				t.Errorf("%s: list says %s, detail %s", w.name, row.Status, d.Recommendation.GPU.Status)
			}
		}
		// A workload without GPUs has no gpu section.
		var all hubapi.WorkloadList
		if _, err := env.Hub.get(ctx(t), "/v1/workloads", url.Values{"cluster": {gpuCluster}, "q": {"model-gateway"}}, &all); err != nil {
			t.Fatal(err)
		}
		for _, r := range all.Items {
			var d hubapi.WorkloadDetail
			if _, err := env.Hub.get(ctx(t), "/v1/workloads/"+url.PathEscape(r.ID), nil, &d); err != nil {
				t.Fatal(err)
			}
			if d.Recommendation != nil && d.Recommendation.GPU != nil {
				t.Errorf("%s has no GPUs but recommendation.gpu %+v", r.Name, d.Recommendation.GPU)
			}
		}
	})

	t.Run("readiness", func(t *testing.T) {
		// The hub's metrics checks run on a recheck.
		acc := recheck(t, gpuCluster)
		var c *hubapi.ReadinessCheck
		eventually(t, 3*time.Minute, 5*time.Second, "metrics.dcgm checked after the recheck", func() (bool, string) {
			c = check(readiness(t, gpuCluster), rd.CheckDCGM)
			if c == nil {
				return false, "no metrics.dcgm check in the readiness report"
			}
			return !c.CheckedAt.Before(acc.RequestedAt.Add(-2 * time.Second)), "checked at " + c.CheckedAt.Format(time.TimeOnly) + ": " + c.Status
		})
		// Most GPU workloads have DCGM series (only notebook-nodata has none): pass.
		if c.Status != "pass" {
			t.Errorf("metrics.dcgm is %s, want pass: %s %v", c.Status, c.Message, c.Details)
		}
	})
}

// gpuQuery lists the GPU workloads of the cluster.
func gpuQuery() url.Values {
	return url.Values{"cluster": {gpuCluster}, "gpu": {hubapi.GPUAny}, "limit": {"100"}}
}

func rowNames(m map[string]hubapi.WorkloadRow) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkFacet(t *testing.T, what string, got []hubapi.FacetCount, want map[string]int) {
	t.Helper()
	have := map[string]int{}
	for _, f := range got {
		have[f.Value] = f.Count
	}
	if len(have) != len(want) {
		t.Errorf("%s: gpu facet %v, want %v", what, got, want)
		return
	}
	for v, n := range want {
		if have[v] != n {
			t.Errorf("%s: gpu facet %s = %d, want %d (%v)", what, v, have[v], n, got)
		}
	}
}

// assertGPU compares the hub's GPU section of a workload with what the scenario intends.
func assertGPU(t *testing.T, w gpuWant, g *hubapi.GPUUsage) {
	t.Helper()
	if g == nil {
		t.Errorf("%s: no gpu section", w.name)
		return
	}
	if g.Status != w.status {
		t.Errorf("%s: status %s, want %s (p95 util %v, memory p95 %v%%)", w.name, g.Status, w.status, numOr(g.UtilizationP95), numOr(g.MemoryUtilizationP95))
	}
	if g.GPUsPerPod != w.gpusPerPod || g.Pods != w.pods || g.GPUs != w.gpusPerPod*w.pods {
		t.Errorf("%s: %d GPUs per pod, %d pods, %d GPUs; want %d, %d, %d", w.name, g.GPUsPerPod, g.Pods, g.GPUs, w.gpusPerPod, w.pods, w.gpusPerPod*w.pods)
	}
	kinds := map[string]hubapi.GPUSuggestion{}
	for _, s := range g.Suggestions {
		kinds[s.Kind] = s
		if s.Message == "" {
			t.Errorf("%s: suggestion %s without a message", w.name, s.Kind)
		}
	}
	for _, k := range w.suggestions {
		if _, ok := kinds[k]; !ok {
			t.Errorf("%s: no %s suggestion (have %v)", w.name, k, g.Suggestions)
		}
	}
	if len(w.suggestions) == 0 && len(g.Suggestions) != 0 {
		t.Errorf("%s: unexpected suggestions %v", w.name, g.Suggestions)
	}

	if !w.hasDCGM {
		if g.Samples != 0 || g.UtilizationP95 != nil {
			t.Errorf("%s: no DCGM series, but %d samples and utilisation %v", w.name, g.Samples, numOr(g.UtilizationP95))
		}
		return
	}
	if g.Samples == 0 || g.UtilizationP95 == nil || g.UtilizationAvg == nil || g.MemoryUtilizationP95 == nil {
		t.Errorf("%s: no utilisation data: %+v", w.name, g)
		return
	}
	if g.Model != "NVIDIA A10G" {
		t.Errorf("%s: model %q, want NVIDIA A10G", w.name, g.Model)
	}
	if got := *g.UtilizationP95; got < w.utilP95[0] || got > w.utilP95[1] {
		t.Errorf("%s: utilisation p95 %.1f%%, want %v", w.name, got, w.utilP95)
	}
	if got := *g.UtilizationAvg; got > *g.UtilizationP95+0.5 {
		t.Errorf("%s: average utilisation %.1f%% above p95 %.1f%%", w.name, got, *g.UtilizationP95)
	}
	if got := *g.MemoryUtilizationP95; got < w.memP95[0] || got > w.memP95[1] {
		t.Errorf("%s: memory p95 %.1f%%, want %v", w.name, got, w.memP95)
	}
	if g.MemoryTotalBytes == nil || *g.MemoryTotalBytes != 24576*1024*1024 {
		t.Errorf("%s: memory total %v, want 24 GiB", w.name, numOr(g.MemoryTotalBytes))
	}

	// Costs, when the GPU nodes have a price: the whole cost per pod, a share unused.
	if !g.Priced {
		t.Logf("%s: not priced (no GPU node price in the pricing service); cost assertions skipped", w.name)
		return
	}
	if g.MonthlyCostUSD <= 0 || g.PerPodMonthlyCostUSD <= 0 {
		t.Errorf("%s: priced but monthly cost %v, per pod %v", w.name, g.MonthlyCostUSD, g.PerPodMonthlyCostUSD)
	}
	if math.Abs(g.PerPodMonthlyCostUSD*float64(g.Pods)-g.MonthlyCostUSD) > 0.01*g.MonthlyCostUSD {
		t.Errorf("%s: %d pods x %.2f != %.2f", w.name, g.Pods, g.PerPodMonthlyCostUSD, g.MonthlyCostUSD)
	}
	if g.UnusedMonthlyCostUSD < 0 || g.UnusedMonthlyCostUSD > g.MonthlyCostUSD+0.01 {
		t.Errorf("%s: unused cost %.2f of %.2f", w.name, g.UnusedMonthlyCostUSD, g.MonthlyCostUSD)
	}
	if w.status == hubapi.GPUIdle {
		// Scale to zero saves the whole cost, and nearly all of it is unused.
		s := numOr(kinds[hubapi.GPUSuggestScaleToZero].MonthlySavingsUSD)
		if s <= 0 || math.Abs(s-g.MonthlyCostUSD) > 0.01*g.MonthlyCostUSD {
			t.Errorf("%s: scale-to-zero saves %.2f of %.2f a month, want the whole cost", w.name, s, g.MonthlyCostUSD)
		}
		if g.UnusedMonthlyCostUSD < 0.9*g.MonthlyCostUSD {
			t.Errorf("%s: unused %.2f of %.2f", w.name, g.UnusedMonthlyCostUSD, g.MonthlyCostUSD)
		}
	}
	if w.status == hubapi.GPUOK && g.UnusedMonthlyCostUSD > 0.5*g.MonthlyCostUSD {
		t.Errorf("%s: busy workload, but %.2f of %.2f unused", w.name, g.UnusedMonthlyCostUSD, g.MonthlyCostUSD)
	}
}

// numOr dereferences an optional number for messages (-1 when absent).
func numOr(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}
