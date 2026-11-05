package recommender

import (
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/tools/synth/synth"
)

func boolp(b bool) *bool { return &b }

// runJVM recommends for one container "app" with the given JVM settings (nil: not a JVM).
func runJVM(t *testing.T, mem []Sample, cur Current, jvm *JVMInfo, settings *hubapi.ValueSettings) (hubapi.WorkloadRecommendation, hubapi.ContainerRecommendation) {
	t.Helper()
	cfg := DefaultConfig()
	raw := &RawWorkload{
		Key:  hubapi.WorkloadKey{Cluster: "test", Namespace: "ns", Kind: hubapi.KindDeployment, Name: "jvm"},
		Pods: podsConst(14, 1),
		Containers: map[string]*RawContainer{"app": {
			Name: "app", CPU: flat(14, 0.05, 0.1, 1), Memory: mem, Current: cur,
		}},
	}
	s := cfg.EffectiveSettings(settings)
	prof := cfg.Profiles[s.Profile]
	data := raw.Data(prof, testEnd, cfg)
	var report []hubapi.JVMContainer
	if jvm != nil {
		report = []hubapi.JVMContainer{jvmReport(jvm)}
	}
	attachJVM(&data, raw, report, true, cfg.JVM)
	res, ok := Recommend(Request{Workload: data, Settings: s, Profile: prof, Pricing: Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004, Source: PricingSourceMetrics}, End: testEnd}, PercentileStrategy{}, cfg)
	if !ok {
		t.Fatal("no recommendation")
	}
	return res.Recommendation, containerByName(res.Recommendation, "app")
}

func jvmReport(j *JVMInfo) hubapi.JVMContainer {
	q := func(v float64) string {
		if v == 0 {
			return ""
		}
		return FormatMemory(v)
	}
	return hubapi.JVMContainer{Name: "app", DetectedBy: j.DetectedBy, MaxHeap: q(j.MaxHeap), InitialHeap: q(j.InitialHeap), MaxRAM: q(j.MaxRAM), MaxRAMPercentage: j.MaxRAMPercentage}
}

func memOf(t *testing.T, v *hubapi.ResourceValues) float64 {
	t.Helper()
	if v == nil || v.Memory == "" {
		return 0
	}
	b, err := ParseMemory(v.Memory)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasNote(e *hubapi.ResourceExplanation, sub string) bool {
	return slices.ContainsFunc(e.Notes, func(n string) bool { return strings.Contains(n, sub) })
}

func TestJVMMemory(t *testing.T) {
	cmd := []string{hubapi.JVMDetectedByCommand}
	// Working set like the dev fixtures: jvm-maxram ~202Mi, jvm-xmx ~160Mi.
	cases := []struct {
		name     string
		mem      []Sample
		cur      Current
		jvm      *JVMInfo
		settings *hubapi.ValueSettings
		// Expectations.
		memReq, memLim float64 // exact recommended values (0: don't check)
		sizing         string
		fixed          bool
		notes          []string
		check          func(t *testing.T, c hubapi.ContainerRecommendation)
	}{
		{
			name:   "fixed -Xmx: floor heap + non-heap, flagged audit-only",
			mem:    flat(14, 160*mi, 0.01, 2),
			cur:    Current{CPURequest: 0.1, MemoryRequest: 512 * mi, MemoryLimit: 768 * mi},
			jvm:    &JVMInfo{DetectedBy: cmd, MaxHeap: 128 * mi},
			memReq: 192 * mi, memLim: 192 * mi, // 128Mi + max(64Mi, 10%) > 160Mi x 1.15 = 184.9Mi
			sizing: hubapi.JVMHeapFixed, fixed: true,
			notes: []string{"raised to 192Mi: JVM heap 128Mi + non-heap 64Mi", "audit-only (jvm-fixed-heap)"},
		},
		{
			name:   "fixed -Xmx above usage: usage rule wins, still flagged",
			mem:    flat(14, 900*mi, 0.01, 3),
			cur:    Current{MemoryRequest: 2048 * mi, MemoryLimit: 2048 * mi},
			jvm:    &JVMInfo{DetectedBy: cmd, MaxHeap: 512 * mi, InitialHeap: 256 * mi},
			sizing: hubapi.JVMHeapFixed, fixed: true,
			check: func(t *testing.T, c hubapi.ContainerRecommendation) {
				// Usage rule (~1035Mi) is above the floor 512Mi + 64Mi.
				if got := memOf(t, c.Recommended.Requests); got < 1000*mi || got > 1100*mi {
					t.Errorf("memory = %s, want the usage rule", FormatMemory(got))
				}
			},
		},
		{
			name:   "fixed -Xmx large heap: non-heap 10%",
			mem:    flat(14, 1000*mi, 0.01, 4),
			cur:    Current{MemoryRequest: 8192 * mi, MemoryLimit: 8192 * mi},
			jvm:    &JVMInfo{DetectedBy: cmd, MaxHeap: 4096 * mi},
			memReq: 4096*mi + roundUp(409.6*mi, 4*mi), sizing: hubapi.JVMHeapFixed, fixed: true,
		},
		{
			name:   "MaxRAMPercentage=75: limit keeps the max heap above the observed peak",
			mem:    flat(14, 202*mi, 0, 5),
			cur:    Current{CPURequest: 0.1, MemoryRequest: 384 * mi, MemoryLimit: 512 * mi},
			jvm:    &JVMInfo{DetectedBy: []string{hubapi.JVMDetectedByImage, hubapi.JVMDetectedByEnv}, MaxRAMPercentage: 75},
			memReq: 272 * mi, memLim: 272 * mi, // 202Mi / 0.75 = 269.3Mi -> 272Mi; usage rule 232.3Mi
			sizing: hubapi.JVMHeapPercentage,
			notes:  []string{"must cover the heap need 202Mi", "75% of 272Mi = 204Mi, covers the heap need 202Mi"},
		},
		{
			name:   "MaxRAMPercentage: heap need capped at the current max heap",
			mem:    flat(14, 400*mi, 0, 6),
			cur:    Current{MemoryRequest: 512 * mi, MemoryLimit: 512 * mi},
			jvm:    &JVMInfo{DetectedBy: cmd, MaxRAMPercentage: 50}, // max heap 256Mi < working set
			memReq: 512 * mi,                                        // heap need 256Mi / 0.5 > usage rule 400 x 1.15 = 460Mi
			check: func(t *testing.T, c hubapi.ContainerRecommendation) {
				if got := c.Explanation.Memory.JVM.HeapNeed; got != "256Mi" {
					t.Errorf("heapNeed = %s, want 256Mi (current max heap)", got)
				}
			},
		},
		{
			name:   "no heap flags: JVM default 25% never shrinks the heap below its use",
			mem:    flat(14, 300*mi, 0, 7),
			cur:    Current{MemoryRequest: 2048 * mi, MemoryLimit: 2048 * mi},
			jvm:    &JVMInfo{DetectedBy: []string{hubapi.JVMDetectedByImage}},
			memReq: 1200 * mi, memLim: 1200 * mi, // heap need min(300, 512) = 300Mi / 0.25
			sizing: hubapi.JVMHeapDefault,
		},
		{
			name:     "limit kept: request from usage, limit already covers the heap",
			mem:      flat(14, 202*mi, 0, 8),
			cur:      Current{MemoryRequest: 384 * mi, MemoryLimit: 512 * mi},
			jvm:      &JVMInfo{DetectedBy: cmd, MaxRAMPercentage: 75},
			settings: &hubapi.ValueSettings{MemoryLimitEqualsRequest: boolp(false)},
			memReq:   236 * mi, memLim: 512 * mi,
			sizing: hubapi.JVMHeapPercentage,
		},
		{
			name:     "-Xms + non-heap raises the request and the kept limit",
			mem:      flat(14, 200*mi, 0, 9),
			cur:      Current{MemoryRequest: 256 * mi, MemoryLimit: 256 * mi},
			jvm:      &JVMInfo{DetectedBy: cmd, MaxRAMPercentage: 75, InitialHeap: 300 * mi},
			settings: &hubapi.ValueSettings{MemoryLimitEqualsRequest: boolp(false)},
			memReq:   364 * mi, memLim: 364 * mi,
			notes: []string{"raised to 364Mi: JVM initial heap -Xms 300Mi + non-heap 64Mi", "limit raised to the request"},
		},
		{
			name:     "no limit kept: heap follows the node",
			mem:      flat(14, 200*mi, 0, 10),
			cur:      Current{MemoryRequest: 512 * mi},
			jvm:      &JVMInfo{DetectedBy: cmd, MaxRAMPercentage: 75},
			settings: &hubapi.ValueSettings{MemoryLimitEqualsRequest: boolp(false)},
			memReq:   232 * mi,
			notes:    []string{"without a memory limit"},
			check: func(t *testing.T, c hubapi.ContainerRecommendation) {
				if c.Recommended.Limits != nil && c.Recommended.Limits.Memory != "" {
					t.Errorf("memory limit = %s, want none", c.Recommended.Limits.Memory)
				}
				if c.Explanation.Memory.JVM.MaxHeapAfter != "" {
					t.Errorf("maxHeapAfter = %s, want unknown", c.Explanation.Memory.JVM.MaxHeapAfter)
				}
			},
		},
		{
			name:   "MaxRAM caps the heap: limit floor is low",
			mem:    flat(14, 500*mi, 0, 11),
			cur:    Current{MemoryRequest: 2048 * mi, MemoryLimit: 2048 * mi},
			jvm:    &JVMInfo{DetectedBy: cmd, MaxRAM: 256 * mi},
			memReq: 576 * mi, // usage rule; heap need min(500, 64) = 64Mi -> floor 256Mi
			check: func(t *testing.T, c hubapi.ContainerRecommendation) {
				x := c.Explanation.Memory.JVM
				if x.MaxHeapBefore != "64Mi" || x.MaxHeapAfter != "64Mi" || x.Floor != "256Mi" {
					t.Errorf("jvm explanation = %+v", x)
				}
			},
		},
		{
			name:   "not a JVM: unchanged",
			mem:    flat(14, 160*mi, 0.01, 2),
			cur:    Current{MemoryRequest: 512 * mi, MemoryLimit: 768 * mi},
			memReq: 188 * mi,
			check: func(t *testing.T, c hubapi.ContainerRecommendation) {
				if c.Explanation.Memory.JVM != nil {
					t.Error("jvm explanation on a non-JVM container")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, c := runJVM(t, tc.mem, tc.cur, tc.jvm, tc.settings)
			req, lim := memOf(t, c.Recommended.Requests), memOf(t, c.Recommended.Limits)
			if tc.memReq > 0 && req != tc.memReq {
				t.Errorf("memory request = %s, want %s", FormatMemory(req), FormatMemory(tc.memReq))
			}
			if tc.memLim > 0 && lim != tc.memLim {
				t.Errorf("memory limit = %s, want %s", FormatMemory(lim), FormatMemory(tc.memLim))
			}
			if got := slices.Contains(c.RiskFlags, hubapi.FlagJVMFixedHeap); got != tc.fixed {
				t.Errorf("container flag jvm-fixed-heap = %v, want %v (%v)", got, tc.fixed, c.RiskFlags)
			}
			if got := slices.Contains(rec.RiskFlags, hubapi.FlagJVMFixedHeap); got != tc.fixed {
				t.Errorf("workload flag jvm-fixed-heap = %v, want %v", got, tc.fixed)
			}
			e := c.Explanation.Memory
			if tc.jvm != nil {
				if e.JVM == nil {
					t.Fatal("no jvm explanation")
				}
				if tc.sizing != "" && e.JVM.HeapSizing != tc.sizing {
					t.Errorf("heapSizing = %s, want %s", e.JVM.HeapSizing, tc.sizing)
				}
				if e.JVM.Source != hubapi.JVMSourceAgent {
					t.Errorf("source = %s", e.JVM.Source)
				}
				// Safety invariant: memory never below the floor; for sized heaps the max heap after
				// the change covers the heap need.
				floor, _ := ParseMemory(e.JVM.Floor)
				if top := math.Max(req, lim); top < floor {
					t.Errorf("memory %s below the JVM floor %s", FormatMemory(top), e.JVM.Floor)
				}
				if e.JVM.HeapSizing != hubapi.JVMHeapFixed && lim > 0 {
					after, _ := ParseMemory(e.JVM.MaxHeapAfter)
					need, _ := ParseMemory(e.JVM.HeapNeed)
					if after < need {
						t.Errorf("max heap after %s < heap need %s", e.JVM.MaxHeapAfter, e.JVM.HeapNeed)
					}
				}
			}
			for _, n := range tc.notes {
				if !hasNote(e, n) {
					t.Errorf("note %q missing in %q", n, e.Notes)
				}
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

func TestAttachJVM(t *testing.T) {
	cfg := DefaultConfig().JVM
	raw := &RawWorkload{Containers: map[string]*RawContainer{
		"app":     {Name: "app", Images: []string{"registry/app:1.0.0-temurin21-jre"}},
		"sidecar": {Name: "sidecar", Images: []string{"envoyproxy/envoy:v1.34.1"}},
	}}
	data := func() WorkloadData {
		return WorkloadData{Containers: []ContainerData{{Name: "app"}, {Name: "sidecar"}}}
	}
	t.Run("agent report wins", func(t *testing.T) {
		d := data()
		attachJVM(&d, raw, []hubapi.JVMContainer{{Name: "app", DetectedBy: []string{"env"}, MaxHeap: "128Mi", MaxRAMPercentage: 120}}, true, cfg)
		j := d.Containers[0].JVM
		if j == nil || j.Source != hubapi.JVMSourceAgent || j.MaxHeap != 128*mi || j.MaxRAMPercentage != 0 || d.Containers[1].JVM != nil {
			t.Errorf("app jvm = %+v, sidecar = %+v", j, d.Containers[1].JVM)
		}
	})
	t.Run("reported without JVM: no image guess", func(t *testing.T) {
		d := data()
		attachJVM(&d, raw, nil, true, cfg)
		if d.Containers[0].JVM != nil {
			t.Errorf("jvm = %+v, want nil (agent detection off or not a JVM)", d.Containers[0].JVM)
		}
	})
	t.Run("not reported: image", func(t *testing.T) {
		d := data()
		attachJVM(&d, raw, nil, false, cfg)
		j := d.Containers[0].JVM
		if j == nil || j.Source != hubapi.JVMSourceImage || d.Containers[1].JVM != nil {
			t.Errorf("app jvm = %+v, sidecar = %+v", j, d.Containers[1].JVM)
		}
	})
	t.Run("image detection off", func(t *testing.T) {
		d := data()
		off := cfg
		off.DetectByImage = false
		attachJVM(&d, raw, nil, false, off)
		if d.Containers[0].JVM != nil {
			t.Error("jvm set with image detection off")
		}
	})
	t.Run("invalid quantities ignored", func(t *testing.T) {
		j := JVMFromReport(hubapi.JVMContainer{Name: "app", MaxHeap: "lots", InitialHeap: "64Mi", MaxRAM: "-1"})
		if j.MaxHeap != 0 || j.InitialHeap != 64*mi || j.MaxRAM != 0 {
			t.Errorf("jvm = %+v", j)
		}
	})
}

func TestIsJVMImage(t *testing.T) {
	for img, want := range map[string]bool{
		"eclipse-temurin:21.0.13_9-jdk-alpine-3.23":                                          true,
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/payments/checkout:1.4.0-temurin21-jre": true,
		"amazoncorretto:21":                   true,
		"openjdk:17-slim":                     true,
		"ghcr.io/graalvm/jdk-community:23":    true,
		"registry/app:2.0-jre17":              true,
		"bellsoft/liberica-openjre-alpine:21": true,
		"nginx:1.27":                          false,
		"registry/jreport-exporter:1.0":       false,
		"envoyproxy/envoy:v1.34.1":            false,
		"python:3.13-slim":                    false,
	} {
		if got := IsJVMImage(img); got != want {
			t.Errorf("IsJVMImage(%q) = %v, want %v", img, got, want)
		}
	}
}

// TestSynthJVMFixture runs WO-02's JVM fixture (image-only detection, sawtooth memory near the
// limit): the recommendation keeps memory (expect.memory = keep) and explains the JVM default.
func TestSynthJVMFixture(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	fx, err := synth.LoadFixture(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "series", "synth-jvm-0.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	step := time.Duration(fx.Step) * time.Second
	start := time.Unix(fx.Start, 0).UTC()
	end := start.Add(time.Duration(fx.Points-1) * step)
	samples := func(v synth.Values) []Sample {
		var out []Sample
		for i, x := range v {
			if !math.IsNaN(x) {
				out = append(out, Sample{T: start.Add(time.Duration(i) * step), V: x})
			}
		}
		return out
	}
	raw := &RawWorkload{Key: hubapi.WorkloadKey{Cluster: fx.Cluster, Namespace: fx.Namespace, Kind: fx.WorkloadKind, Name: fx.Workload},
		Pods: samples(fx.Replicas), Containers: map[string]*RawContainer{}}
	for _, c := range fx.Containers {
		if c.CPU == nil {
			continue
		}
		raw.Containers[c.Name] = &RawContainer{Name: c.Name, Images: []string{c.Image}, CPU: samples(c.CPU.Max), Memory: samples(c.Memory.Max),
			Current: Current{CPURequest: c.Requests["cpu"], CPULimit: c.Limits["cpu"], MemoryRequest: c.Requests["memory"], MemoryLimit: c.Limits["memory"]}}
	}
	s := cfg.EffectiveSettings(nil)
	prof := cfg.Profiles[s.Profile]
	data := raw.Data(prof, end, cfg)
	attachJVM(&data, raw, nil, false, cfg.JVM)
	res, ok := Recommend(Request{Workload: data, Settings: s, Profile: prof, Pricing: Pricing{CPUCoreHour: 0.03, MemoryGiBHour: 0.004}, End: end}, PercentileStrategy{}, cfg)
	if !ok {
		t.Fatal("no recommendation")
	}
	for _, c := range res.Recommendation.Containers {
		cur := raw.Containers[c.Name].Current.MemoryRequest
		got := memOf(t, c.Recommended.Requests)
		if fx.Expect.Memory == "keep" && (got < 0.75*cur || got > 1.25*cur) {
			t.Errorf("%s memory %s, want keep (current %s)", c.Name, FormatMemory(got), FormatMemory(cur))
		}
		x := c.Explanation.Memory.JVM
		if x == nil || x.Source != hubapi.JVMSourceImage || x.HeapSizing != hubapi.JVMHeapDefault || !hasNote(c.Explanation.Memory, "assuming the JVM default MaxRAMPercentage=25") {
			t.Errorf("%s jvm explanation = %+v, notes %q", c.Name, x, c.Explanation.Memory.Notes)
		}
		if got := memOf(t, c.Recommended.Requests); got < cur {
			t.Errorf("%s memory decreased to %s below the JVM default floor (current %s)", c.Name, FormatMemory(got), FormatMemory(cur))
		}
	}
}

// TestEngineJVM: RunCluster uses the agent's JVM report for reported workloads and the image for
// the others.
func TestEngineJVM(t *testing.T) {
	cfg := DefaultConfig()
	mk := func(name string) *RawWorkload {
		return &RawWorkload{Key: hubapi.WorkloadKey{Cluster: "c1", Namespace: "ns", Kind: hubapi.KindDeployment, Name: name},
			Pods: podsConst(14, 1), HasRequests: true, Containers: map[string]*RawContainer{"app": {
				Name: "app", Images: []string{"eclipse-temurin:21-jre"}, CPU: flat(14, 0.05, 0.1, 1), Memory: flat(14, 160*mi, 0.01, 2),
				Current: Current{CPURequest: 0.1, MemoryRequest: 512 * mi, MemoryLimit: 768 * mi},
			}}}
	}
	xmx, img := mk("jvm-xmx"), mk("synthetic")
	d := &ClusterData{Cluster: "c1", End: testEnd, Workloads: map[hubapi.WorkloadKey]*RawWorkload{xmx.Key: xmx, img.Key: img}}
	e := &Engine{Loader: &fakeLoader{d: d}, Cfg: cfg, Strategy: PercentileStrategy{},
		JVM: func(k hubapi.WorkloadKey) ([]hubapi.JVMContainer, bool) {
			if k == xmx.Key {
				return []hubapi.JVMContainer{{Name: "app", DetectedBy: []string{"image", "command"}, MaxHeap: "128Mi"}}, true
			}
			return nil, false
		}}
	res, err := e.RunCluster(t.Context(), "c1", testEnd, nil)
	if err != nil || len(res.Results) != 2 {
		t.Fatalf("results %v %v", res, err)
	}
	for _, r := range res.Results {
		rec := r.Recommendation
		x := rec.Containers[0].Explanation.Memory.JVM
		switch rec.Workload.Name {
		case "jvm-xmx":
			if !slices.Equal(rec.RiskFlags, []string{hubapi.FlagJVMFixedHeap}) || x.Source != hubapi.JVMSourceAgent || x.HeapSizing != hubapi.JVMHeapFixed {
				t.Errorf("jvm-xmx flags %v jvm %+v", rec.RiskFlags, x)
			}
		case "synthetic":
			if len(rec.RiskFlags) != 0 || x.Source != hubapi.JVMSourceImage || x.HeapSizing != hubapi.JVMHeapDefault {
				t.Errorf("synthetic flags %v jvm %+v", rec.RiskFlags, x)
			}
		}
	}
}
