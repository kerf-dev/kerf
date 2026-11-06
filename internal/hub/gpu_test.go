package hub

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// gpuEnv gives three workloads of cluster c1 GPUs: ledger idle, checkout under-used and priced,
// frontend without DCGM series.
func gpuEnv(t *testing.T) *testEnv {
	e := newEnv(t)
	e.loader.gpuPrices = map[string]float64{"gpu-1": 1.5}
	for i, w := range e.loader.workloads["c1"] {
		pod := []recommender.GPUPod{{Name: w.key.Name + "-0", Node: "gpu-1", GPUs: 1}}
		switch w.key {
		case kLedger:
			e.loader.workloads["c1"][i].gpu = &gpuSpec{pods: pod, util: 1, memUsedMiB: 900, totalMiB: 23028, model: "NVIDIA A10G"}
		case kCheckout:
			e.loader.workloads["c1"][i].gpu = &gpuSpec{pods: append(pod, recommender.GPUPod{Name: "checkout-1", Node: "gpu-1", GPUs: 1}), util: 20, memUsedMiB: 6000, totalMiB: 23028, model: "NVIDIA A10G"}
		case kFrontend:
			e.loader.workloads["c1"][i].gpu = &gpuSpec{pods: pod, noDCGM: true}
		}
	}
	e.run(e.now, false)
	return e
}

func TestWorkloadsGPU(t *testing.T) {
	e := gpuEnv(t)
	cases := []struct {
		query string
		want  []string
	}{
		{"?gpu=any&sort=name", []string{"checkout", "frontend", "ledger"}},
		{"?gpu=idle", []string{"ledger"}},
		{"?gpu=under-used", []string{"checkout"}},
		{"?gpu=no-data", []string{"frontend"}},
		{"?gpu=ok", nil},
		{"?gpu=idle&gpu=no-data&sort=name", []string{"frontend", "ledger"}},
		{"?gpu=any&cluster=c2", nil},
		{"?gpu=idle&namespace=web", nil},
	}
	for _, tc := range cases {
		var l hubapi.WorkloadList
		e.getJSON("/v1/workloads"+tc.query, tokUI, &l)
		if got := rowNames(l.Items); !slices.Equal(got, tc.want) || l.Total != len(tc.want) {
			t.Errorf("%s: rows %v (total %d), want %v", tc.query, got, l.Total, tc.want)
		}
	}
	if r := e.do("GET", "/v1/workloads?gpu=amd", tokUI, nil); r.code != 400 || !strings.Contains(string(r.body), "invalid_gpu") {
		t.Errorf("invalid gpu filter: %d %s", r.code, r.body)
	}

	// The row carries the GPU data; workloads without GPUs have none.
	var l hubapi.WorkloadList
	e.getJSON("/v1/workloads?limit=500", tokUI, &l)
	for _, r := range l.Items {
		switch r.Name {
		case "ledger":
			g := r.GPU
			if g == nil || g.Status != hubapi.GPUIdle || g.GPUs != 1 || !g.Priced || g.MonthlyCostUSD != 1.5*recommender.HoursPerMonth || len(g.Suggestions) != 1 || g.Suggestions[0].Kind != hubapi.GPUSuggestScaleToZero {
				t.Errorf("ledger gpu = %+v", g)
			}
		case "checkout":
			g := r.GPU
			if g == nil || g.Status != hubapi.GPUUnderUsed || g.GPUs != 2 || g.Pods != 2 || g.GPUsPerPod != 1 || g.PerPodMonthlyCostUSD != 1.5*recommender.HoursPerMonth || g.UtilizationP95 == nil || *g.UtilizationP95 != 20 {
				t.Errorf("checkout gpu = %+v", g)
			}
		case "frontend":
			if r.GPU == nil || r.GPU.Status != hubapi.GPUNoData || r.GPU.UtilizationP95 != nil {
				t.Errorf("frontend gpu = %+v", r.GPU)
			}
		default:
			if r.GPU != nil {
				t.Errorf("%s has GPU data: %+v", r.Name, r.GPU)
			}
		}
	}

	// The detail has it under recommendation.gpu.
	var d hubapi.WorkloadDetail
	e.getJSON("/v1/workloads/"+url.PathEscape(kLedger.ID()), tokUI, &d)
	if d.Recommendation == nil || d.Recommendation.GPU == nil || d.Recommendation.GPU.Status != hubapi.GPUIdle {
		t.Errorf("detail gpu = %+v", d.Recommendation)
	}

	// Facets count the GPU statuses with the other filters applied, and the GPU filter itself is skipped.
	var f hubapi.WorkloadFacets
	e.getJSON("/v1/workloads/facets?gpu=idle", tokUI, &f)
	want := []hubapi.FacetCount{{Value: "idle", Count: 1}, {Value: "no-data", Count: 1}, {Value: "under-used", Count: 1}}
	if !slices.Equal(f.GPU, want) || f.Total != 1 {
		t.Errorf("facets gpu = %+v total %d, want %+v total 1", f.GPU, f.Total, want)
	}
	e.getJSON("/v1/workloads/facets?namespace=web", tokUI, &f)
	if want := []hubapi.FacetCount{{Value: "no-data", Count: 1}}; !slices.Equal(f.GPU, want) {
		t.Errorf("facets gpu in web = %+v, want %+v", f.GPU, want)
	}

	// The agent's recommendation list carries the GPU data too (same shape).
	var rl hubapi.RecommendationList
	e.getJSON("/v1/clusters/c1/recommendations", tokC1, &rl)
	n := 0
	for _, r := range rl.Items {
		if r.GPU != nil {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d recommendations with GPU data, want 3", n)
	}
}
