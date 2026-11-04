package recommender

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

const mi = float64(MiB)

type pattern struct {
	name     string
	kind     string
	cpu, mem []Sample
	pods     []Sample
	current  Current
	throttle float64
	oom      bool
	restarts float64
	hpa      *HPA
	settings *hubapi.ValueSettings
	cname    string
	init     bool

	// Expectations. Ranges are inclusive; 0 means "don't check".
	cpuMin, cpuMax float64 // recommended CPU request (cores)
	memMin, memMax float64 // recommended memory request (bytes)
	cpuDir, memDir int     // -1 decrease, +1 increase vs current request, 0 don't check
	flags          []string
	notFlags       []string
	confMin        float64
	confMax        float64
	check          func(t *testing.T, r hubapi.WorkloadRecommendation)
}

func run(t *testing.T, p pattern) hubapi.WorkloadRecommendation {
	t.Helper()
	cfg := DefaultConfig()
	kind := p.kind
	if kind == "" {
		kind = hubapi.KindDeployment
	}
	name := p.cname
	if name == "" {
		name = "app"
	}
	pods := p.pods
	if pods == nil {
		pods = podsConst(14, 2)
	}
	raw := &RawWorkload{
		Key:  hubapi.WorkloadKey{Cluster: "test", Namespace: "ns", Kind: kind, Name: p.name},
		Pods: pods,
		HPA:  p.hpa,
		Containers: map[string]*RawContainer{name: {
			Name: name, Init: p.init, CPU: p.cpu, Memory: p.mem, Current: p.current,
			Throttle: []Sample{{T: testEnd, V: p.throttle}}, OOMKilled: p.oom, Restarts: p.restarts,
		}},
	}
	if p.init {
		// A native sidecar needs a main container with comparable coverage.
		raw.Containers["main"] = &RawContainer{Name: "main", CPU: p.cpu, Memory: p.mem, Current: p.current}
	}
	s := cfg.EffectiveSettings(p.settings)
	prof := cfg.Profiles[s.Profile]
	data := raw.Data(prof, testEnd, cfg)
	pricing := Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004, Source: PricingSourceMetrics}
	res, ok := Recommend(Request{Workload: data, Settings: s, Profile: prof, Pricing: pricing, End: testEnd}, PercentileStrategy{}, cfg)
	if !ok {
		t.Fatalf("no recommendation")
	}
	return res.Recommendation
}

func containerByName(r hubapi.WorkloadRecommendation, n string) hubapi.ContainerRecommendation {
	for _, c := range r.Containers {
		if c.Name == n {
			return c
		}
	}
	return hubapi.ContainerRecommendation{}
}

func TestPatterns(t *testing.T) {
	big := Current{CPURequest: 1, CPULimit: 2, MemoryRequest: 1024 * mi, MemoryLimit: 1024 * mi}
	tests := []pattern{
		{
			name: "flat", cpu: flat(14, 0.1, 0.05, 1), mem: flat(14, 200*mi, 0.02, 2), current: big,
			cpuMin: 0.115, cpuMax: 0.125, memMin: 232 * mi, memMax: 240 * mi, cpuDir: -1, memDir: -1,
			notFlags: []string{hubapi.FlagLowConfidence, hubapi.FlagThrottled}, confMin: 0.9,
		},
		{
			// P95 of a half-sine day sits near the peak: request ~ (0.1+0.3*0.99)*1.15.
			name: "diurnal", cpu: diurnal(14, 0.1, 0.3), mem: diurnal(14, 300*mi, 100*mi), current: big,
			cpuMin: 0.43, cpuMax: 0.47, memMin: 460 * mi, memMax: 464 * mi, cpuDir: -1, memDir: -1, confMin: 0.8,
		},
		{
			// 10 of 14 days are weekdays: P95 is the weekday level.
			name: "weekly", cpu: weekly(14, 0.4, 0.05), mem: weekly(14, 512*mi, 128*mi), current: big,
			cpuMin: 0.46, cpuMax: 0.46, memMin: 592 * mi, memMax: 592 * mi, cpuDir: -1, memDir: -1,
		},
		{
			// Spikes in 1 of 100 steps: CPU P95 ignores them, memory peak does not.
			name: "spiky", cpu: spiky(14, 0.05, 1.5, 100), mem: spiky(14, 100*mi, 400*mi, 100), current: big,
			cpuMin: 0.06, cpuMax: 0.06, memMin: 460 * mi, memMax: 460 * mi, cpuDir: -1, memDir: -1,
		},
		{
			// Leak from 100Mi to 400Mi: memory follows the peak (max(p99,peak)).
			name: "memory-leak", cpu: flat(14, 0.05, 0, 3), mem: leak(14, 100*mi, 400*mi),
			current: Current{CPURequest: 0.1, MemoryRequest: 256 * mi, MemoryLimit: 256 * mi},
			memMin:  460 * mi, memMax: 460 * mi, memDir: 1,
		},
		{
			// Usage stepped up 4 days ago (29 % of the window): P95 sees the new level.
			name: "step-change", cpu: stepChange(14, 0.1, 0.4, 4), mem: stepChange(14, 200*mi, 600*mi, 4),
			current: Current{CPURequest: 0.2, MemoryRequest: 256 * mi},
			cpuMin:  0.46, cpuMax: 0.46, memMin: 692 * mi, memMax: 692 * mi, cpuDir: 1, memDir: 1,
		},
		{
			name: "sparse-cronjob", kind: hubapi.KindCronJob, cpu: sparse(14, 0.5, 1, 12), mem: sparse(14, 64*mi, 1, 12),
			pods: sparse(14, 1, 1, 12), current: Current{CPURequest: 1, MemoryRequest: 128 * mi},
			cpuMin: 0.575, cpuMax: 0.575, cpuDir: -1, memDir: -1,
			flags: []string{hubapi.FlagShortLived, hubapi.FlagLowConfidence}, confMax: 0.5,
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				if math.Abs(r.AverageReplicas-1.0/12) > 0.01 {
					t.Errorf("averageReplicas = %v, want ~0.083", r.AverageReplicas)
				}
			},
		},
		{
			// Throttled at its 200m limit: the recommendation is an increase (>= +20 %), limit raised.
			name: "throttled", cpu: flat(14, 0.05, 0.1, 4), mem: flat(14, 64*mi, 0, 5), throttle: 0.35,
			current: Current{CPURequest: 0.2, CPULimit: 0.2, MemoryRequest: 128 * mi, MemoryLimit: 128 * mi},
			cpuMin:  0.24, cpuMax: 0.24, cpuDir: 1, flags: []string{hubapi.FlagThrottled},
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				c := r.Containers[0]
				if c.Recommended.Limits.CPU != "240m" {
					t.Errorf("cpu limit = %s, want 240m", c.Recommended.Limits.CPU)
				}
				if r.MonthlySavingsUSD.CPU >= 0 {
					t.Errorf("cpu savings = %v, want negative for an increase", r.MonthlySavingsUSD.CPU)
				}
			},
		},
		{
			// OOM-killed: never a decrease; at least +20 % over max(request, limit).
			name: "oom-killed", cpu: flat(14, 0.05, 0, 6), mem: flat(14, 100*mi, 0, 7), oom: true, restarts: 12,
			current: Current{CPURequest: 0.1, MemoryRequest: 128 * mi, MemoryLimit: 256 * mi},
			memMin:  308 * mi, memMax: 308 * mi, memDir: 1, flags: []string{hubapi.FlagOOMKilled, hubapi.FlagRestarting},
		},
		{
			name: "hpa-coupled", cpu: flat(14, 0.1, 0, 8), mem: flat(14, 100*mi, 0, 9), current: big,
			hpa: &HPA{Name: "app", Metrics: []string{"cpu/utilization"}}, flags: []string{hubapi.FlagHPACoupled},
		},
		{
			name: "hpa-external-metric", cpu: flat(14, 0.1, 0, 8), mem: flat(14, 100*mi, 0, 9), current: big,
			hpa: &HPA{Name: "app", Metrics: []string{"queue_depth/value"}}, notFlags: []string{hubapi.FlagHPACoupled},
		},
		{
			name: "one-day-of-data", cpu: flat(1, 0.1, 0, 10), mem: flat(1, 100*mi, 0, 11), current: big,
			flags: []string{hubapi.FlagLowConfidence}, confMax: 0.5,
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				if r.DataCoverage.Covered != "23h55m0s" || r.DataCoverage.Samples != 288 {
					t.Errorf("coverage = %+v", r.DataCoverage)
				}
			},
		},
		{
			name: "istio-sidecar", cname: "istio-proxy", cpu: flat(14, 0.02, 0, 12), mem: flat(14, 60*mi, 0, 13), current: big,
			flags: []string{hubapi.FlagSidecar},
		},
		{
			name: "native-sidecar", cname: "log-shipper", init: true, cpu: flat(14, 0.02, 0, 12), mem: flat(14, 60*mi, 0, 13), current: big,
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				c := containerByName(r, "log-shipper")
				if !c.Init || !slices.Contains(c.RiskFlags, hubapi.FlagSidecar) {
					t.Errorf("native sidecar = %+v", c)
				}
				if r.Containers[0].Name != "main" {
					t.Errorf("app containers must come first: %v", r.Containers[0].Name)
				}
			},
		},
		{
			name: "tiny-usage-hits-minimums", cpu: flat(14, 0.001, 0, 14), mem: flat(14, 5*mi, 0, 15), current: big,
			cpuMin: 0.01, cpuMax: 0.01, memMin: 32 * mi, memMax: 32 * mi,
		},
		{
			name: "cpu-limit-ratio", cpu: flat(14, 0.1, 0, 16), mem: flat(14, 100*mi, 0, 17), current: big,
			settings: &hubapi.ValueSettings{CPULimits: hubapi.CPULimitsRatio, CPULimitRatio: "2"},
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				c := r.Containers[0]
				if c.Recommended.Requests.CPU != "115m" || c.Recommended.Limits.CPU != "230m" {
					t.Errorf("ratio: %+v %+v", *c.Recommended.Requests, *c.Recommended.Limits)
				}
				if r.Settings.CPULimits != "ratio" || r.Settings.CPULimitRatio != "2" {
					t.Errorf("settings not echoed: %+v", r.Settings)
				}
			},
		},
		{
			name: "cpu-limit-remove", cpu: flat(14, 0.1, 0, 16), mem: flat(14, 100*mi, 0, 17), current: big,
			settings: &hubapi.ValueSettings{CPULimits: hubapi.CPULimitsRemove},
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				c := r.Containers[0]
				if c.Recommended.Limits.CPU != "" || !slices.Equal(c.RemoveLimits, []string{"cpu"}) {
					t.Errorf("remove: %+v %v", *c.Recommended.Limits, c.RemoveLimits)
				}
			},
		},
		{
			name: "memory-limit-keep", cpu: flat(14, 0.1, 0, 16), mem: flat(14, 100*mi, 0, 17), current: big,
			settings: &hubapi.ValueSettings{MemoryLimitEqualsRequest: new(false)},
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				c := r.Containers[0]
				if c.Recommended.Limits.Memory != "1Gi" || c.Recommended.Requests.Memory != "116Mi" {
					t.Errorf("keep: %+v %+v", *c.Recommended.Requests, *c.Recommended.Limits)
				}
			},
		},
		{
			// dev profile: 7-day window, P90, +10 %.
			name: "dev-profile", cpu: stepChange(14, 0.5, 0.1, 7), mem: flat(14, 100*mi, 0, 18), current: big,
			settings: &hubapi.ValueSettings{Profile: hubapi.ProfileDev},
			cpuMin:   0.11, cpuMax: 0.11,
			check: func(t *testing.T, r hubapi.WorkloadRecommendation) {
				e := r.Containers[0].Explanation
				if e.Profile != "dev" || e.Window != "168h0m0s" || e.CPU.Statistic != "p90" || e.CPU.Margin != 0.1 {
					t.Errorf("explanation = %+v %+v", e, e.CPU)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, tc)
			c := r.Containers[0]
			if tc.cname != "" && !tc.init {
				c = containerByName(r, tc.cname)
			}
			if tc.init {
				c = containerByName(r, tc.cname)
			}
			cpu, _ := ParseCPU(c.Recommended.Requests.CPU)
			mem, _ := ParseMemory(c.Recommended.Requests.Memory)
			if tc.cpuMin > 0 && (cpu < tc.cpuMin-1e-9 || cpu > tc.cpuMax+1e-9) {
				t.Errorf("cpu request = %v, want [%v, %v]", cpu, tc.cpuMin, tc.cpuMax)
			}
			if tc.memMin > 0 && (mem < tc.memMin || mem > tc.memMax) {
				t.Errorf("memory request = %vMi, want [%v, %v]Mi", mem/mi, tc.memMin/mi, tc.memMax/mi)
			}
			if d := sign(cpu - tc.current.CPURequest); tc.cpuDir != 0 && d != tc.cpuDir {
				t.Errorf("cpu direction = %d, want %d (current %v, recommended %v)", d, tc.cpuDir, tc.current.CPURequest, cpu)
			}
			if d := sign(mem - tc.current.MemoryRequest); tc.memDir != 0 && d != tc.memDir {
				t.Errorf("memory direction = %d, want %d", d, tc.memDir)
			}
			for _, f := range tc.flags {
				if !slices.Contains(r.RiskFlags, f) {
					t.Errorf("missing flag %s in %v", f, r.RiskFlags)
				}
			}
			for _, f := range tc.notFlags {
				if slices.Contains(r.RiskFlags, f) {
					t.Errorf("unexpected flag %s in %v", f, r.RiskFlags)
				}
			}
			if tc.confMin > 0 && r.Confidence < tc.confMin {
				t.Errorf("confidence = %v, want >= %v", r.Confidence, tc.confMin)
			}
			if tc.confMax > 0 && r.Confidence >= tc.confMax {
				t.Errorf("confidence = %v, want < %v", r.Confidence, tc.confMax)
			}
			if tc.check != nil {
				tc.check(t, r)
			}
			// Savings sign follows the direction of the request change.
			if tc.cpuDir == -1 && c.MonthlySavingsUSD.CPU <= 0 {
				t.Errorf("cpu savings %v should be positive for a decrease", c.MonthlySavingsUSD.CPU)
			}
			assertContract(t, r)
		})
	}
}

func sign(v float64) int {
	switch {
	case v > 1e-9:
		return 1
	case v < -1e-9:
		return -1
	}
	return 0
}

var quantityRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$`)

// assertContract checks the API invariants of a recommendation.
func assertContract(t *testing.T, r hubapi.WorkloadRecommendation) {
	t.Helper()
	b, _ := json.Marshal(r)
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				walk(path+"."+k, vv)
			}
		case []any:
			for _, vv := range x {
				walk(path+"[]", vv)
			}
		case string:
			if regexp.MustCompile(`\.(requests|limits)\.(cpu|memory)$|\.observed\.p\d+$|\.observed\.max$|\.base$|\.roundedTo$`).MatchString(path) && !quantityRE.MatchString(x) {
				t.Errorf("%s = %q is not a contract quantity", path, x)
			}
		}
	}
	var m any
	_ = json.Unmarshal(b, &m)
	walk("", m)
	if r.Confidence < 0 || r.Confidence > 1 || r.DataCoverage.Ratio < 0 || r.DataCoverage.Ratio > 1 {
		t.Errorf("confidence/coverage out of range: %v %v", r.Confidence, r.DataCoverage.Ratio)
	}
	for _, f := range r.RiskFlags {
		if !hubapi.ValidRiskFlag(f) {
			t.Errorf("invalid flag %q", f)
		}
	}
	if math.Abs(r.MonthlySavingsUSD.Total-(r.MonthlySavingsUSD.CPU+r.MonthlySavingsUSD.Memory)) > 0.011 {
		t.Errorf("savings total mismatch: %+v", r.MonthlySavingsUSD)
	}
	for _, c := range r.Containers {
		if c.Recommended.Limits != nil && c.Recommended.Requests != nil {
			if c.Recommended.Limits.CPU != "" {
				lc, _ := ParseCPU(c.Recommended.Limits.CPU)
				rc, _ := ParseCPU(c.Recommended.Requests.CPU)
				if lc < rc {
					t.Errorf("cpu limit %v < request %v", lc, rc)
				}
			}
			if c.Recommended.Limits.Memory != "" {
				lm, _ := ParseMemory(c.Recommended.Limits.Memory)
				rm, _ := ParseMemory(c.Recommended.Requests.Memory)
				if lm < rm {
					t.Errorf("memory limit %v < request %v", lm, rm)
				}
			}
		}
	}
}

func TestSavingsFormula(t *testing.T) {
	// 2 replicas, 1 core -> 115m and 1Gi -> 116Mi at $0.03/core-h and $0.004/GiB-h.
	r := run(t, pattern{name: "s", cpu: flat(14, 0.1, 0, 1), mem: flat(14, 100*mi, 0, 1),
		current: Current{CPURequest: 1, MemoryRequest: 1024 * mi}})
	wantCPU := (1 - 0.115) * 0.03 * 2 * 730
	wantMem := (1024.0 - 116) / 1024 * 0.004 * 2 * 730
	if math.Abs(r.MonthlySavingsUSD.CPU-wantCPU) > 0.006 || math.Abs(r.MonthlySavingsUSD.Memory-wantMem) > 0.006 {
		t.Errorf("savings = %+v, want cpu %.2f memory %.2f", r.MonthlySavingsUSD, wantCPU, wantMem)
	}
}

func TestPricingFallbackIsFlagged(t *testing.T) {
	cfg := DefaultConfig()
	raw := &RawWorkload{Key: hubapi.WorkloadKey{Cluster: "c", Namespace: "n", Kind: "Deployment", Name: "w"}, Pods: podsConst(14, 1),
		Containers: map[string]*RawContainer{"a": {Name: "a", CPU: flat(14, 0.1, 0, 1), Memory: flat(14, 100*mi, 0, 1)}}}
	p := cfg.Profiles["production"]
	res, _ := Recommend(Request{Workload: raw.Data(p, testEnd, cfg), Settings: cfg.EffectiveSettings(nil), Profile: p,
		Pricing: Pricing{CPUCoreHour: 1, MemoryGiBHour: 1, Source: PricingSourceDefault}, End: testEnd}, PercentileStrategy{}, cfg)
	notes := res.Recommendation.Containers[0].Explanation.CPU.Notes
	if !slices.ContainsFunc(notes, func(s string) bool { return regexp.MustCompile("default rate").MatchString(s) }) {
		t.Errorf("fallback not noted: %v", notes)
	}
}

func TestNoDataNoRecommendation(t *testing.T) {
	cfg := DefaultConfig()
	p := cfg.Profiles["production"]
	raw := &RawWorkload{Key: hubapi.WorkloadKey{Cluster: "c", Namespace: "n", Kind: "Deployment", Name: "w"},
		Containers: map[string]*RawContainer{"a": {Name: "a", Current: Current{CPURequest: 1}}}}
	if _, ok := Recommend(Request{Workload: raw.Data(p, testEnd, cfg), Settings: cfg.EffectiveSettings(nil), Profile: p, End: testEnd}, PercentileStrategy{}, cfg); ok {
		t.Fatal("expected no recommendation without usage data")
	}
}

func TestEffectiveSettings(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name string
		in   *hubapi.ValueSettings
		want hubapi.ValueSettings
	}{
		{"nil", nil, cfg.DefaultSettings},
		{"profile", &hubapi.ValueSettings{Profile: "dev"}, hubapi.ValueSettings{Profile: "dev", CPULimits: "keep", MemoryLimitEqualsRequest: new(true)}},
		{"unknown profile", &hubapi.ValueSettings{Profile: "nope"}, cfg.DefaultSettings},
		{"ratio", &hubapi.ValueSettings{CPULimits: "ratio", CPULimitRatio: "1.5"}, hubapi.ValueSettings{Profile: "production", CPULimits: "ratio", CPULimitRatio: "1.5", MemoryLimitEqualsRequest: new(true)}},
		{"bad ratio", &hubapi.ValueSettings{CPULimits: "ratio", CPULimitRatio: "0.5"}, cfg.DefaultSettings},
		{"mem keep", &hubapi.ValueSettings{MemoryLimitEqualsRequest: new(false)}, hubapi.ValueSettings{Profile: "production", CPULimits: "keep", MemoryLimitEqualsRequest: new(false)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.EffectiveSettings(tc.in)
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(tc.want)
			if string(gb) != string(wb) {
				t.Errorf("got %s, want %s", gb, wb)
			}
		})
	}
}

func TestQuantityFormatting(t *testing.T) {
	tests := []struct {
		fn   func(float64) string
		in   float64
		want string
	}{
		{FormatCPU, 0.25, "250m"}, {FormatCPU, 1, "1"}, {FormatCPU, 2.5, "2500m"}, {FormatCPU, 0.0004, "0"},
		{FormatMemory, 512 * mi, "512Mi"}, {FormatMemory, 2048 * mi, "2Gi"}, {FormatMemory, 100000000, "100000000"},
		{FormatMemory, 1536 * 1024, "1536Ki"},
	}
	for _, tc := range tests {
		if got := tc.fn(tc.in); got != tc.want || !quantityRE.MatchString(got) {
			t.Errorf("format(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if v := roundUp(0.115, 0.005); math.Abs(v-0.115) > 1e-12 {
		t.Errorf("roundUp exact multiple = %v", v)
	}
	if v := roundUp(0.1151, 0.005); math.Abs(v-0.12) > 1e-12 {
		t.Errorf("roundUp = %v", v)
	}
}

func TestStatsMatchPromQL(t *testing.T) {
	// quantile_over_time(0.5, {1,2,3,4}) = 2.5 and (0.95) = 3.85 in Prometheus.
	s := StatsFromSamples([]Sample{{testEnd, 4}, {testEnd.Add(-time.Minute), 1}, {testEnd.Add(-2 * time.Minute), 3}, {testEnd.Add(-3 * time.Minute), 2}}, []float64{0.5, 0.95})
	if s.Quantiles[0.5] != 2.5 || math.Abs(s.Quantiles[0.95]-3.85) > 1e-9 || s.Max != 4 || s.Mean != 2.5 {
		t.Errorf("stats = %+v", s)
	}
	if !s.First.Equal(testEnd.Add(-3*time.Minute)) || !s.Last.Equal(testEnd) {
		t.Errorf("first/last = %v %v", s.First, s.Last)
	}
}
