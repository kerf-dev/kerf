package recommender

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestGPUQueries(t *testing.T) {
	q := Queries{Cluster: "c1", RateWindow: 5 * time.Minute}
	all := q.GPUUtilization() + q.GPUMemoryUsed() + q.GPUMemoryTotal(time.Hour)
	// Both label styles of the exporter map to pod and namespace; the cluster's series are selected.
	for _, want := range []string{
		`label_replace(label_replace(`, `"pod", "$1", "exported_pod", "(.+)"`, `"namespace", "$1", "exported_namespace", "(.+)"`,
		`DCGM_FI_DEV_GPU_UTIL{cluster="c1"}`, `DCGM_FI_DEV_FB_USED{cluster="c1"}`, `DCGM_FI_DEV_FB_FREE{cluster="c1"}`,
		"last_over_time(DCGM_FI_DEV_FB_USED", "kube_replicaset_owner",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("queries lack %q", want)
		}
	}
	if p := q.GPUPods(); !strings.Contains(p, `kube_pod_container_resource_requests{cluster="c1", resource="nvidia_com_gpu"}`) || !strings.Contains(p, "node") {
		t.Errorf("GPUPods = %s", p)
	}
	// Pricing is read from the hub's own store, whatever the binding.
	m, _ := datasource.NewMatcher("c1", &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod"})
	bq := Queries{Cluster: "c1", Binding: &m}
	if p := bq.GPUPrice(time.Hour); !strings.Contains(p, `kerf_gpu_hourly_cost_usd{cluster="c1"}`) || strings.Contains(p, "k8s_cluster") {
		t.Errorf("GPUPrice = %s", p)
	}
	if u := bq.GPUUtilization(); !strings.Contains(u, `k8s_cluster="prod"`) || !strings.Contains(u, `"cluster", "c1", "", ""`) {
		t.Errorf("bound GPUUtilization lacks matcher or relabel: %.200s", u)
	}
}

// gpuSeries is n days of per-step samples ending at testEnd with a constant value.
func gpuSeries(days, v float64) []Sample {
	return gen(days, func(int, time.Time) (float64, bool) { return v, true })
}

func TestAnalyzeGPU(t *testing.T) {
	cfg := DefaultConfig()
	prod := cfg.Profiles[hubapi.ProfileProduction]
	days := prod.Window.Hours() / 24
	prices := map[string]float64{"gpu-1": 1.0, "gpu-4": 2.0}
	a10g := func(util, memUsedMiB float64, pods ...GPUPod) *RawWorkload {
		return &RawWorkload{GPU: &RawGPU{Pods: pods, Util: gpuSeries(days, util), MemUsedMiB: gpuSeries(days, memUsedMiB), MemTotalMiB: 23028, Model: "NVIDIA A10G"}}
	}
	onePod := GPUPod{Name: "p-0", Node: "gpu-1", GPUs: 1}
	tests := []struct {
		name         string
		w            *RawWorkload
		discount     float64
		wantNil      bool
		status       string
		kinds        []string
		cost         float64
		priced       bool
		gpus, perPod int
	}{
		{name: "no GPU data", w: &RawWorkload{}, wantNil: true},
		{name: "idle: scale to zero", w: a10g(1.2, 800, onePod), status: hubapi.GPUIdle, kinds: []string{"scale-to-zero"}, cost: 730, priced: true, gpus: 1, perPod: 1},
		{name: "idle boundary 5 % is under-used", w: a10g(5, 800, onePod), status: hubapi.GPUUnderUsed, kinds: []string{"time-slicing", "smaller-gpu"}, cost: 730, priced: true, gpus: 1, perPod: 1},
		{name: "under-used by utilisation: time-slicing, memory fits a T4", w: a10g(25, 8000, onePod), status: hubapi.GPUUnderUsed, kinds: []string{"time-slicing", "smaller-gpu"}, cost: 730, priced: true, gpus: 1, perPod: 1},
		{name: "under-used by utilisation, memory too big for a smaller GPU", w: a10g(25, 20000, onePod), status: hubapi.GPUUnderUsed, kinds: []string{"time-slicing"}, cost: 730, priced: true, gpus: 1, perPod: 1},
		{name: "busy compute, little memory: only a smaller GPU", w: a10g(70, 3000, onePod), status: hubapi.GPUUnderUsed, kinds: []string{"smaller-gpu"}, cost: 730, priced: true, gpus: 1, perPod: 1},
		{name: "40 % utilisation and 50 % memory is fine", w: a10g(40, 12000, onePod), status: hubapi.GPUOK, kinds: nil, cost: 730, priced: true, gpus: 1, perPod: 1},
		{name: "busy", w: a10g(85, 20000, onePod), status: hubapi.GPUOK, kinds: nil, cost: 730, priced: true, gpus: 1, perPod: 1},
		{
			name: "two pods with 4 GPUs on a priced node", w: a10g(2, 1000, GPUPod{Name: "a", Node: "gpu-4", GPUs: 4}, GPUPod{Name: "b", Node: "gpu-4", GPUs: 4}),
			status: hubapi.GPUIdle, kinds: []string{"scale-to-zero"}, cost: 8 * 2 * 730, priced: true, gpus: 8, perPod: 4,
		},
		{name: "unpriced node", w: a10g(1, 800, GPUPod{Name: "p", Node: "unknown", GPUs: 2}), status: hubapi.GPUIdle, kinds: []string{"scale-to-zero"}, cost: 0, priced: false, gpus: 2, perPod: 2},
		{name: "discount applies", w: a10g(1, 800, onePod), discount: 20, status: hubapi.GPUIdle, kinds: []string{"scale-to-zero"}, cost: 730 * 0.8, priced: true, gpus: 1, perPod: 1},
		{
			name: "no DCGM utilisation", w: &RawWorkload{GPU: &RawGPU{Pods: []GPUPod{onePod}}},
			status: hubapi.GPUNoData, cost: 730, priced: true, gpus: 1, perPod: 1,
		},
		{
			name: "A100 under-used: MIG next to time-slicing", w: &RawWorkload{GPU: &RawGPU{Pods: []GPUPod{onePod}, Util: gpuSeries(days, 20), MemUsedMiB: gpuSeries(days, 30000), MemTotalMiB: 81920, Model: "NVIDIA A100-SXM4-80GB"}},
			status: hubapi.GPUUnderUsed, kinds: []string{"time-slicing", "mig", "smaller-gpu"}, cost: 730, priced: true, gpus: 1, perPod: 1,
		},
		{
			name: "utilisation without memory series", w: &RawWorkload{GPU: &RawGPU{Pods: []GPUPod{onePod}, Util: gpuSeries(days, 10), Model: "NVIDIA A10G"}},
			status: hubapi.GPUUnderUsed, kinds: []string{"time-slicing"}, cost: 730, priced: true, gpus: 1, perPod: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.w.AnalyzeGPU(prod, testEnd, prices, tc.discount, GPUConfig{})
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("nil")
			}
			if got.Status != tc.status || got.Priced != tc.priced || got.GPUs != tc.gpus || got.GPUsPerPod != tc.perPod {
				t.Errorf("status %s priced %v gpus %d per pod %d, want %s %v %d %d", got.Status, got.Priced, got.GPUs, got.GPUsPerPod, tc.status, tc.priced, tc.gpus, tc.perPod)
			}
			if math.Abs(got.MonthlyCostUSD-tc.cost) > 0.01 {
				t.Errorf("monthly cost %v, want %v", got.MonthlyCostUSD, tc.cost)
			}
			var kinds []string
			for _, s := range got.Suggestions {
				kinds = append(kinds, s.Kind)
				if s.Message == "" {
					t.Errorf("suggestion %s without a message", s.Kind)
				}
			}
			if strings.Join(kinds, ",") != strings.Join(tc.kinds, ",") {
				t.Errorf("suggestions %v, want %v", kinds, tc.kinds)
			}
			if got.Suggestions == nil {
				t.Error("suggestions must be an empty list, not null")
			}
			if tc.status == hubapi.GPUIdle && tc.priced {
				if s := got.Suggestions[0]; s.MonthlySavingsUSD == nil || math.Abs(*s.MonthlySavingsUSD-tc.cost) > 0.01 {
					t.Errorf("scale-to-zero savings %v, want the cost %v", s.MonthlySavingsUSD, tc.cost)
				}
			}
		})
	}
}

func TestAnalyzeGPUDetails(t *testing.T) {
	prod := DefaultConfig().Profiles[hubapi.ProfileProduction]
	days := prod.Window.Hours() / 24
	// 90 % of the steps at 10 % utilisation, 10 % of the steps at 90 %: p95 is high, the mean low.
	var util []Sample
	for i, s := range gpuSeries(days, 10) {
		if i%10 == 0 {
			s.V = 90
		}
		util = append(util, s)
	}
	w := &RawWorkload{GPU: &RawGPU{Pods: []GPUPod{{Name: "p", Node: "n", GPUs: 1}}, Util: util, MemUsedMiB: gpuSeries(days, 6000), MemTotalMiB: 24000, Model: "NVIDIA L4"}}
	got := w.AnalyzeGPU(prod, testEnd, map[string]float64{"n": 2}, 0, GPUConfig{})
	// Bursty GPUs with a high p95 are not under-used on utilisation, but 25 % of the memory is.
	if got.Status != hubapi.GPUUnderUsed || len(got.Suggestions) != 1 || got.Suggestions[0].Kind != hubapi.GPUSuggestSmallerGPU {
		t.Errorf("status %s suggestions %+v, want under-used with only a smaller GPU", got.Status, got.Suggestions)
	}
	if got.UtilizationP95 == nil || *got.UtilizationP95 < 80 || got.UtilizationAvg == nil || math.Abs(*got.UtilizationAvg-18) > 1 {
		t.Errorf("p95 %v avg %v", got.UtilizationP95, got.UtilizationAvg)
	}
	if got.MemoryUtilizationP95 == nil || *got.MemoryUtilizationP95 != 25 || *got.MemoryUsedP95Bytes != 6000*mibBytes || *got.MemoryTotalBytes != 24000*mibBytes {
		t.Errorf("memory %v %v %v", got.MemoryUtilizationP95, got.MemoryUsedP95Bytes, got.MemoryTotalBytes)
	}
	// unused cost = cost x (1 - mean utilisation)
	if want := got.MonthlyCostUSD * (1 - *got.UtilizationAvg/100); math.Abs(got.UnusedMonthlyCostUSD-want) > 0.5 {
		t.Errorf("unused cost %v, want about %v", got.UnusedMonthlyCostUSD, want)
	}
	if got.Window != prod.Window.String() || got.Samples == 0 {
		t.Errorf("window %q samples %d", got.Window, got.Samples)
	}
	// Samples older than the profile window are ignored.
	old := &RawWorkload{GPU: &RawGPU{Pods: []GPUPod{{Name: "p", Node: "n", GPUs: 1}}, Util: gpuSeries(days+10, 1)}}
	old.GPU.Util = old.GPU.Util[:len(old.GPU.Util)/3] // only the oldest third remains
	if g := old.AnalyzeGPU(prod, testEnd, nil, 0, GPUConfig{}); g.Status != hubapi.GPUNoData {
		t.Errorf("status %s, want no-data for samples outside the window", g.Status)
	}
}

func TestSmallerGPU(t *testing.T) {
	const gib = 1024.0 * 1024 * 1024
	tests := []struct {
		name        string
		current     string
		used, total float64
		want        string
		wantOK      bool
	}{
		{"A10G with 8 GiB used fits a T4", "NVIDIA A10G", 8 * gib, 22.5 * gib, "NVIDIA T4", true},
		{"A10G with 12 GiB used needs more than a T4: nothing smaller than 24 GiB", "NVIDIA A10G", 12 * gib, 22.5 * gib, "", false},
		{"A100 80 GB with 28 GiB used fits an A100 40GB", "NVIDIA A100-SXM4-80GB", 28 * gib, 80 * gib, "NVIDIA A100 40GB", true},
		{"A100 80 GB with 30 GiB used needs an L40S", "NVIDIA A100-SXM4-80GB", 30 * gib, 80 * gib, "NVIDIA L40S", true},
		{"A100 80 GB with 15 GiB used fits an L4 first", "NVIDIA A100-SXM4-80GB", 15 * gib, 80 * gib, "NVIDIA L4", true},
		{"T4 has nothing smaller", "Tesla T4", 2 * gib, 15 * gib, "", false},
		{"unknown memory", "NVIDIA A10G", 0, 22 * gib, "", false},
	}
	for _, tc := range tests {
		got, ok := smallerGPU(tc.current, tc.used, tc.total, 1.25)
		if ok != tc.wantOK || got.Name != tc.want {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, got.Name, ok, tc.want, tc.wantOK)
		}
	}
	for model, want := range map[string]bool{"NVIDIA A100-SXM4-40GB": true, "NVIDIA H100 80GB HBM3": true, "NVIDIA A10G": false, "Tesla T4": false, "NVIDIA L4": false} {
		if supportsMIG(model) != want {
			t.Errorf("supportsMIG(%q) != %v", model, want)
		}
	}
}
