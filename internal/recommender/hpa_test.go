package recommender

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

func hpaContainer(name, curCPU, recCPU, curMem, recMem string, flags ...string) hubapi.ContainerRecommendation {
	vals := func(cpu, mem string) hubapi.Resources {
		return hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: cpu, Memory: mem}}
	}
	return hubapi.ContainerRecommendation{
		Name: name, Current: vals(curCPU, curMem), Recommended: vals(recCPU, recMem), RiskFlags: flags,
		MonthlySavingsUSD: &hubapi.Savings{},
		Explanation: &hubapi.ContainerExplanation{
			CPU:    &hubapi.ResourceExplanation{LimitRule: "keep"},
			Memory: &hubapi.ResourceExplanation{LimitRule: "equals request"},
		},
	}
}

func intp(v int) *int { return &v }

func TestCoordinateHPA(t *testing.T) {
	cfg := DefaultConfig()
	cpuHPA := func(target float64) *HPA {
		return &HPA{Name: "web", Metrics: []string{"cpu/utilization"}, Targets: map[string]float64{"cpu": target},
			MinReplicas: 2, MaxReplicas: 6, PeakReplicas: 3}
	}
	type want struct {
		hpa    *hubapi.HPACoordination
		cpu    []string // recommended CPU requests after coordination, per container
		memory []string
	}
	cases := []struct {
		name       string
		hpa        *HPA
		containers []hubapi.ContainerRecommendation
		want       want
	}{
		{"no HPA", nil, []hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")}, want{cpu: []string{"50m"}}},
		{"HPA on a custom metric only", &HPA{Name: "web", Metrics: []string{"http_requests/averagevalue"}},
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")}, want{cpu: []string{"50m"}}},
		{"target within bounds", cpuHPA(60), []hubapi.ContainerRecommendation{hpaContainer("app", "100m", "80m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, RecommendedTarget: 75,
				MinReplicas: intp(2), MaxReplicas: intp(6)}, cpu: []string{"80m"}}},
		// dev/workloads/hpa-cpu: 100m request, ~40m usage, target 60 %.
		{"target above 90 %: requests raised to keep it at the bound", cpuHPA(60),
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, RecommendedTarget: 86,
				MinReplicas: intp(2), MaxReplicas: intp(6)}, cpu: []string{"70m"}}},
		{"target below 40 %: requests lowered to keep it at the bound", cpuHPA(60),
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "200m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, RecommendedTarget: 40,
				MinReplicas: intp(2), MaxReplicas: intp(6)}, cpu: []string{"150m"}}},
		{"pod sum: sidecar counts, init container does not", cpuHPA(60), []hubapi.ContainerRecommendation{
			hpaContainer("app", "100m", "40m", "", ""), hpaContainer("proxy", "50m", "10m", "", ""),
			func() hubapi.ContainerRecommendation {
				c := hpaContainer("setup", "", "500m", "", "")
				c.Init = true
				return c
			}(),
		}, want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, RecommendedTarget: 90,
			MinReplicas: intp(2), MaxReplicas: intp(6)}, cpu: []string{"80m", "20m", "500m"}}},
		{"current target above the bounds widens them", cpuHPA(95),
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "100m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 95, RecommendedTarget: 95,
				MinReplicas: intp(2), MaxReplicas: intp(6)}, cpu: []string{"100m"}}},
		{"memory utilization", &HPA{Name: "web", Metrics: []string{"memory/utilization"}, Targets: map[string]float64{"memory": 70}},
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "256Mi", "128Mi")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "memory", CurrentTarget: 70, RecommendedTarget: 90},
				cpu: []string{"50m"}, memory: []string{"200Mi"}}},
		{"at maxReplicas in the window", &HPA{Name: "web", Metrics: []string{"cpu/utilization"}, Targets: map[string]float64{"cpu": 60},
			MaxReplicas: 4, PeakReplicas: 4},
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, MaxReplicas: intp(4),
				SkipReason: hubapi.HPASkipMaxReplicas}, cpu: []string{"50m"}}},
		{"utilization plus a custom metric", &HPA{Name: "web", Metrics: []string{"cpu/utilization", "http_requests/averagevalue"},
			Targets: map[string]float64{"cpu": 60}},
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, SkipReason: hubapi.HPASkipUnsupported}, cpu: []string{"50m"}}},
		{"cpu and memory utilization", &HPA{Name: "web", Metrics: []string{"cpu/utilization", "memory/utilization"},
			Targets: map[string]float64{"cpu": 60, "memory": 70}},
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, SkipReason: hubapi.HPASkipUnsupported}, cpu: []string{"50m"}}},
		{"no target known", &HPA{Name: "web", Metrics: []string{"cpu/utilization"}},
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", SkipReason: hubapi.HPASkipUnsupported}, cpu: []string{"50m"}}},
		{"container without a request", cpuHPA(60),
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "50m", "", ""), hpaContainer("proxy", "", "20m", "", "")},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 60, MinReplicas: intp(2), MaxReplicas: intp(6),
				SkipReason: hubapi.HPASkipMissingRequest}, cpu: []string{"50m", "20m"}}},
		{"throttled container would have to go lower", cpuHPA(45),
			[]hubapi.ContainerRecommendation{hpaContainer("app", "100m", "200m", "", "", hubapi.FlagThrottled)},
			want{hpa: &hubapi.HPACoordination{Name: "web", Metric: "cpu", CurrentTarget: 45, MinReplicas: intp(2), MaxReplicas: intp(6),
				SkipReason: hubapi.HPASkipTargetOutOfRange}, cpu: []string{"200m"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := hubapi.WorkloadRecommendation{Containers: tc.containers}
			w := WorkloadData{HPA: tc.hpa, AverageReplicas: 2}
			req := Request{Pricing: Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004}}
			got := coordinateHPA(&rec, w, req, cfg)
			if !reflect.DeepEqual(got, tc.want.hpa) {
				t.Errorf("hpa = %+v, want %+v", got, tc.want.hpa)
			}
			for i, c := range rec.Containers {
				if c.Recommended.Requests.CPU != tc.want.cpu[i] {
					t.Errorf("%s cpu = %s, want %s", c.Name, c.Recommended.Requests.CPU, tc.want.cpu[i])
				}
				if tc.want.memory != nil && c.Recommended.Requests.Memory != tc.want.memory[i] {
					t.Errorf("%s memory = %s, want %s", c.Name, c.Recommended.Requests.Memory, tc.want.memory[i])
				}
			}
			// The invariant: sum(requests) x target stays within rounding of the original.
			if got != nil && got.RecommendedTarget > 0 {
				res := got.Metric
				var cur, next float64
				for _, c := range rec.Containers {
					if c.Init {
						continue
					}
					cc, cm, _, _ := values(c.Current)
					rc, rm, _, _ := values(c.Recommended)
					if res == "cpu" {
						cur, next = cur+cc, next+rc
					} else {
						cur, next = cur+cm, next+rm
					}
				}
				before, after := cur*float64(got.CurrentTarget), next*float64(got.RecommendedTarget)
				if math.Abs(after-before)/before > 0.04 {
					t.Errorf("sum(requests) x target %v -> %v", before, after)
				}
			}
		})
	}
}

func TestCoordinateHPALimitsAndSavings(t *testing.T) {
	cfg := DefaultConfig()
	w := WorkloadData{AverageReplicas: 2, HPA: &HPA{Name: "web", Metrics: []string{"memory/utilization"}, Targets: map[string]float64{"memory": 70}}}
	req := Request{Pricing: Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004}}

	c := hpaContainer("app", "100m", "50m", "256Mi", "128Mi")
	c.Recommended.Limits = &hubapi.ResourceValues{Memory: "128Mi"}
	cpuRatio := hpaContainer("app", "100m", "50m", "", "")
	cpuRatio.Explanation.CPU.LimitRule = "ratio 2"
	cpuRatio.Recommended.Limits = &hubapi.ResourceValues{CPU: "100m"}
	cpuKeep := hpaContainer("app", "100m", "50m", "", "")
	cpuKeep.Current.Limits = &hubapi.ResourceValues{CPU: "60m"}
	cpuKeep.Recommended.Limits = &hubapi.ResourceValues{CPU: "60m"}

	rec := hubapi.WorkloadRecommendation{Containers: []hubapi.ContainerRecommendation{c}}
	h := coordinateHPA(&rec, w, req, cfg)
	got := rec.Containers[0]
	if h.RecommendedTarget != 90 || got.Recommended.Limits.Memory != "200Mi" {
		t.Errorf("memory limit (equals request) = %+v, hpa %+v", got.Recommended.Limits, h)
	}
	wantSv := (256.0 - 200) / 1024 * 0.004 * 2 * HoursPerMonth
	if math.Abs(got.MonthlySavingsUSD.Memory-wantSv) > 0.01 || got.MonthlySavingsUSD.Total != got.MonthlySavingsUSD.Memory {
		t.Errorf("savings %+v, want memory %.2f", got.MonthlySavingsUSD, wantSv)
	}
	if !slices.ContainsFunc(got.Explanation.Memory.Notes, func(n string) bool { return strings.Contains(n, "70% -> 90%") }) ||
		!slices.ContainsFunc(got.Explanation.Memory.Notes, func(n string) bool { return strings.Contains(n, "adjusted") }) {
		t.Errorf("notes %v", got.Explanation.Memory.Notes)
	}

	w.HPA = &HPA{Name: "web", Metrics: []string{"cpu/utilization"}, Targets: map[string]float64{"cpu": 60}}
	for _, tc := range []struct {
		c         hubapi.ContainerRecommendation
		wantLimit string
	}{{cpuRatio, "140m"}, {cpuKeep, "70m"}} {
		rec := hubapi.WorkloadRecommendation{Containers: []hubapi.ContainerRecommendation{tc.c}}
		coordinateHPA(&rec, w, req, cfg)
		if l := rec.Containers[0].Recommended.Limits; l == nil || l.CPU != tc.wantLimit {
			t.Errorf("cpu limit (%s) = %+v, want %s", tc.c.Explanation.CPU.LimitRule, l, tc.wantLimit)
		}
	}
}

// TestRecommendHPA checks the hook in Recommend: the recommendation carries hpa and the
// workload savings are the sum of the adjusted container savings.
func TestRecommendHPA(t *testing.T) {
	cfg := DefaultConfig()
	end := testEnd
	p := cfg.Profiles[hubapi.ProfileProduction]
	w := WorkloadData{
		Key: hubapi.WorkloadKey{Cluster: "c1", Namespace: "default", Kind: hubapi.KindDeployment, Name: "hpa-cpu"}, AverageReplicas: 2,
		HPA: &HPA{Name: "hpa-cpu", Metrics: []string{"cpu/utilization"}, Targets: map[string]float64{"cpu": 60}, MinReplicas: 2, MaxReplicas: 4, PeakReplicas: 3},
		Containers: []ContainerData{{Name: "app", CPU: StatsFromSamples(flat(14, 0.04, 0, 1), Quantiles(p)),
			Memory:  StatsFromSamples(flat(14, 20*MiB, 0, 2), Quantiles(p)),
			Current: Current{CPURequest: 0.1, MemoryRequest: 32 * MiB, MemoryLimit: 64 * MiB}}},
	}
	res, ok := Recommend(Request{Workload: w, Profile: p, End: end, Pricing: Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004, Source: PricingSourceMetrics}},
		PercentileStrategy{}, cfg)
	if !ok {
		t.Fatal("no recommendation")
	}
	rec := res.Recommendation
	if rec.HPA == nil || rec.HPA.RecommendedTarget != 86 || rec.Containers[0].Recommended.Requests.CPU != "70m" {
		t.Fatalf("hpa %+v, cpu %s", rec.HPA, rec.Containers[0].Recommended.Requests.CPU)
	}
	if !slices.Contains(rec.RiskFlags, hubapi.FlagHPACoupled) {
		t.Errorf("flags %v", rec.RiskFlags)
	}
	var sum hubapi.Savings
	for _, c := range rec.Containers {
		sum = sum.Add(*c.MonthlySavingsUSD)
	}
	if roundSavings(sum) != rec.MonthlySavingsUSD {
		t.Errorf("workload savings %+v, containers %+v", rec.MonthlySavingsUSD, sum)
	}
}
