package policy

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

func pol(name string, prio int32, sel v1alpha1.PolicySelector, cpu, mem v1alpha1.Mode) v1alpha1.RightsizingPolicy {
	p := v1alpha1.RightsizingPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}}
	p.Spec.Priority = prio
	p.Spec.Selector = sel
	if cpu != "" {
		p.Spec.CPU = &v1alpha1.CPUPolicy{Mode: cpu}
	}
	if mem != "" {
		p.Spec.Memory = &v1alpha1.MemoryPolicy{Mode: mem}
	}
	return p
}

func clusterDefault(cpu, mem v1alpha1.Mode) *v1alpha1.RightsizingPolicySpec {
	s := &v1alpha1.RightsizingPolicySpec{}
	if cpu != "" {
		s.CPU = &v1alpha1.CPUPolicy{Mode: cpu}
	}
	if mem != "" {
		s.Memory = &v1alpha1.MemoryPolicy{Mode: mem}
	}
	return s
}

const (
	off   = v1alpha1.ModeOff
	audit = v1alpha1.ModeAudit
	auto  = v1alpha1.ModeAutoPatch

	srcGlobal  = v1alpha1.ModeSourceGlobalDefault
	srcCluster = v1alpha1.ModeSourceClusterDefault
	srcPolicy  = v1alpha1.ModeSourcePolicy
	srcAnn     = v1alpha1.ModeSourceAnnotation
)

// TestPrecedenceMatrix covers D-005: global < cluster default < policy < annotation, per resource.
func TestPrecedenceMatrix(t *testing.T) {
	web := Workload{Namespace: "shop", Kind: v1alpha1.KindDeployment, Name: "web", Labels: map[string]string{"tier": "web"}}
	all := v1alpha1.PolicySelector{}
	tests := []struct {
		name        string
		cluster     *v1alpha1.RightsizingPolicySpec
		policies    []v1alpha1.RightsizingPolicy
		ann         map[string]string
		cpu, mem    v1alpha1.Mode
		cpuS, memS  v1alpha1.ModeSource
		wantPolicy  string
		wantWarning bool
	}{
		{name: "global default only", cpu: audit, mem: audit, cpuS: srcGlobal, memS: srcGlobal},
		{name: "cluster default both", cluster: clusterDefault(auto, off), cpu: auto, mem: off, cpuS: srcCluster, memS: srcCluster},
		{name: "cluster default cpu only, memory inherits global", cluster: clusterDefault(auto, ""), cpu: auto, mem: audit, cpuS: srcCluster, memS: srcGlobal},
		{name: "policy beats cluster default", cluster: clusterDefault(off, off), policies: []v1alpha1.RightsizingPolicy{pol("p", 1, all, auto, auto)},
			cpu: auto, mem: auto, cpuS: srcPolicy, memS: srcPolicy, wantPolicy: "p"},
		{name: "policy memory only, cpu from cluster", cluster: clusterDefault(auto, off), policies: []v1alpha1.RightsizingPolicy{pol("p", 1, all, "", audit)},
			cpu: auto, mem: audit, cpuS: srcCluster, memS: srcPolicy, wantPolicy: "p"},
		{name: "annotation mode beats policy", policies: []v1alpha1.RightsizingPolicy{pol("p", 1, all, auto, auto)},
			ann: map[string]string{v1alpha1.AnnotationMode: "off"}, cpu: off, mem: off, cpuS: srcAnn, memS: srcAnn, wantPolicy: "p"},
		{name: "cpu-mode beats mode", ann: map[string]string{v1alpha1.AnnotationMode: "audit", v1alpha1.AnnotationCPUMode: "auto-patch"},
			cpu: auto, mem: audit, cpuS: srcAnn, memS: srcAnn},
		{name: "memory-mode alone", cluster: clusterDefault(off, off), ann: map[string]string{v1alpha1.AnnotationMemoryMode: "auto-patch"},
			cpu: off, mem: auto, cpuS: srcCluster, memS: srcAnn},
		{name: "invalid annotation ignored", cluster: clusterDefault(auto, auto), ann: map[string]string{v1alpha1.AnnotationCPUMode: "yolo"},
			cpu: auto, mem: auto, cpuS: srcCluster, memS: srcCluster, wantWarning: true},
		{name: "highest priority policy wins", policies: []v1alpha1.RightsizingPolicy{pol("low", 1, all, auto, auto), pol("high", 10, all, off, off)},
			cpu: off, mem: off, cpuS: srcPolicy, memS: srcPolicy, wantPolicy: "high"},
		{name: "tie broken by name", policies: []v1alpha1.RightsizingPolicy{pol("b", 5, all, off, off), pol("a", 5, all, auto, auto)},
			cpu: auto, mem: auto, cpuS: srcPolicy, memS: srcPolicy, wantPolicy: "a"},
		{name: "winner fields not merged with loser", policies: []v1alpha1.RightsizingPolicy{pol("low", 1, all, auto, auto), pol("high", 10, all, off, "")},
			cpu: off, mem: audit, cpuS: srcPolicy, memS: srcGlobal, wantPolicy: "high"},
		{name: "non-matching policy ignored", policies: []v1alpha1.RightsizingPolicy{pol("other", 100, v1alpha1.PolicySelector{Namespaces: []string{"payments-*"}}, auto, auto)},
			cpu: audit, mem: audit, cpuS: srcGlobal, memS: srcGlobal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := web
			w.Annotations = tt.ann
			e := Resolve(w, tt.policies, tt.cluster)
			if e.CPUMode != tt.cpu || e.MemoryMode != tt.mem || e.CPUModeSource != tt.cpuS || e.MemoryModeSource != tt.memS {
				t.Errorf("got cpu=%s(%s) mem=%s(%s), want cpu=%s(%s) mem=%s(%s)",
					e.CPUMode, e.CPUModeSource, e.MemoryMode, e.MemoryModeSource, tt.cpu, tt.cpuS, tt.mem, tt.memS)
			}
			if e.Policy != tt.wantPolicy {
				t.Errorf("policy = %q, want %q", e.Policy, tt.wantPolicy)
			}
			if (len(e.Warnings) > 0) != tt.wantWarning {
				t.Errorf("warnings = %v, want warning %v", e.Warnings, tt.wantWarning)
			}
		})
	}
}

func TestSelectorMatching(t *testing.T) {
	w := Workload{Namespace: "team-a", Kind: v1alpha1.KindStatefulSet, Name: "db", Labels: map[string]string{"app": "db", "tier": "data"}}
	tests := []struct {
		name string
		sel  v1alpha1.PolicySelector
		want bool
	}{
		{"empty matches all", v1alpha1.PolicySelector{}, true},
		{"namespace glob", v1alpha1.PolicySelector{Namespaces: []string{"team-*"}}, true},
		{"namespace miss", v1alpha1.PolicySelector{Namespaces: []string{"prod"}}, false},
		{"excluded beats included", v1alpha1.PolicySelector{Namespaces: []string{"team-*"}, ExcludeNamespaces: []string{"team-a"}}, false},
		{"labels all present", v1alpha1.PolicySelector{MatchLabels: map[string]string{"app": "db", "tier": "data"}}, true},
		{"label value differs", v1alpha1.PolicySelector{MatchLabels: map[string]string{"app": "web"}}, false},
		{"kind matches", v1alpha1.PolicySelector{Kinds: []v1alpha1.WorkloadKind{v1alpha1.KindStatefulSet}}, true},
		{"kind differs", v1alpha1.PolicySelector{Kinds: []v1alpha1.WorkloadKind{v1alpha1.KindDeployment}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Matches(tt.sel, w); got != tt.want {
				t.Errorf("Matches = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNonModeFieldsLayered(t *testing.T) {
	ten := resource.MustParse("50m")
	pct := int32(20)
	cluster := &v1alpha1.RightsizingPolicySpec{
		Profile: v1alpha1.ProfileDev,
		Apply:   &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyRollout, FallbackAfter: &metav1.Duration{Duration: time.Minute}},
		Guardrails: &v1alpha1.Guardrails{
			MinCPU: &ten, MinConfidence: "0.9", Cooldown: &metav1.Duration{Duration: time.Hour},
		},
	}
	p := pol("p", 1, v1alpha1.PolicySelector{}, "", "")
	p.Spec.Guardrails = &v1alpha1.Guardrails{MaxChangePercent: &pct}
	p.Spec.CPU = &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitRatio, LimitRatio: "2"}
	ann := map[string]string{v1alpha1.AnnotationStrategy: "in-place", v1alpha1.AnnotationProfile: "high-availability"}
	e := Resolve(Workload{Namespace: "x", Kind: v1alpha1.KindDeployment, Name: "y", Annotations: ann}, []v1alpha1.RightsizingPolicy{p}, cluster)

	if e.Strategy != v1alpha1.StrategyInPlace || e.Profile != v1alpha1.ProfileHighAvailability {
		t.Errorf("annotation overrides: strategy=%s profile=%s", e.Strategy, e.Profile)
	}
	if e.FallbackAfter != time.Minute || e.Cooldown != time.Hour || e.MinConfidence != 0.9 || e.MinCPU.Cmp(ten) != 0 {
		t.Errorf("cluster default guardrails not inherited: %+v", e)
	}
	if e.MaxChangePercent != 20 || e.LearningPeriod != DefaultLearningPeriod || e.MinMemory.Cmp(DefaultMinMemory) != 0 {
		t.Errorf("policy/global guardrails: maxChange=%d learning=%s minMem=%s", e.MaxChangePercent, e.LearningPeriod, e.MinMemory.String())
	}
	s := e.Settings()
	if s.CPULimits != "ratio" || s.CPULimitRatio != "2" || s.Profile != "high-availability" || !*s.MemoryLimitEqualsRequest {
		t.Errorf("settings = %+v", s)
	}
}

func TestSettingsMatch(t *testing.T) {
	tr, fa := true, false
	tests := []struct {
		name      string
		want, got hubclient.ValueSettings
		match     bool
	}{
		{"defaults equal empty", hubclient.ValueSettings{Profile: "production", CPULimits: "keep", MemoryLimitEqualsRequest: &tr}, hubclient.ValueSettings{}, true},
		{"profile differs", hubclient.ValueSettings{Profile: "dev"}, hubclient.ValueSettings{Profile: "production"}, false},
		{"ratio numeric equal", hubclient.ValueSettings{CPULimits: "ratio", CPULimitRatio: "2"}, hubclient.ValueSettings{CPULimits: "ratio", CPULimitRatio: "2.0"}, true},
		{"ratio differs", hubclient.ValueSettings{CPULimits: "ratio", CPULimitRatio: "2"}, hubclient.ValueSettings{CPULimits: "ratio", CPULimitRatio: "1.5"}, false},
		{"memory limit rule differs", hubclient.ValueSettings{MemoryLimitEqualsRequest: &fa}, hubclient.ValueSettings{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SettingsMatch(tt.want, tt.got); got != tt.match {
				t.Errorf("SettingsMatch = %v, want %v", got, tt.match)
			}
		})
	}
}

func TestInWindow(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 30, 0, 0, time.UTC) // Wednesday
	hour := &metav1.Duration{Duration: time.Hour}
	tests := []struct {
		name    string
		w       v1alpha1.TimeWindow
		want    bool
		wantErr bool
	}{
		{"recurring open", v1alpha1.TimeWindow{Schedule: "0 10 * * *", Duration: hour}, true, false},
		{"recurring closed", v1alpha1.TimeWindow{Schedule: "0 12 * * *", Duration: hour}, false, false},
		{"recurring at end is closed", v1alpha1.TimeWindow{Schedule: "30 9 * * *", Duration: hour}, false, false},
		{"weekday", v1alpha1.TimeWindow{Schedule: "0 0 * * 3", Duration: &metav1.Duration{Duration: 24 * time.Hour}}, true, false},
		{"time zone", v1alpha1.TimeWindow{Schedule: "0 12 * * *", Duration: hour, TimeZone: "Asia/Jerusalem"}, true, false},
		{"one-off open", v1alpha1.TimeWindow{Start: &metav1.Time{Time: now.Add(-time.Minute)}, End: &metav1.Time{Time: now.Add(time.Minute)}}, true, false},
		{"one-off past", v1alpha1.TimeWindow{Start: &metav1.Time{Time: now.Add(-2 * time.Hour)}, End: &metav1.Time{Time: now.Add(-time.Hour)}}, false, false},
		{"bad cron", v1alpha1.TimeWindow{Schedule: "nope", Duration: hour}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InWindow([]v1alpha1.TimeWindow{tt.w}, now)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("InWindow = %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func hubPol(p v1alpha1.RightsizingPolicy, typ string) v1alpha1.RightsizingPolicy {
	p.Labels = map[string]string{v1alpha1.LabelManagedBy: v1alpha1.ManagedByHub}
	p.Annotations = map[string]string{v1alpha1.AnnotationHubPolicyType: typ}
	return p
}

// TestHubLayers covers the D-022 layering: hub defaults and overrides are layers around the
// winning policy, not competitors.
func TestHubLayers(t *testing.T) {
	web := Workload{Namespace: "shop", Kind: v1alpha1.KindDeployment, Name: "web"}
	all := v1alpha1.PolicySelector{}
	global := hubPol(pol(v1alpha1.GlobalDefaultsPolicyName, 0, all, audit, off), v1alpha1.HubPolicyTypeGlobalDefault)
	global.Spec.Guardrails = &v1alpha1.Guardrails{MinConfidence: "0.8"}
	clusterHub := hubPol(pol(v1alpha1.ClusterDefaultsPolicyName, 10, all, "", audit), v1alpha1.HubPolicyTypeClusterDefault)
	team := pol("team", 50, v1alpha1.PolicySelector{Namespaces: []string{"shop"}}, auto, "")
	overrideName := v1alpha1.OverridePolicyName("shop", v1alpha1.KindDeployment, "web")
	override := hubPol(pol(overrideName, v1alpha1.PriorityWorkloadOverride, v1alpha1.PolicySelector{
		Namespaces: []string{"shop"}, Kinds: []v1alpha1.WorkloadKind{v1alpha1.KindDeployment},
		Workloads: []v1alpha1.NamespacedWorkloadRef{{Kind: v1alpha1.KindDeployment, Namespace: "shop", Name: "web"}},
	}, "", off), v1alpha1.HubPolicyTypeWorkloadOverride)
	otherOverride := hubPol(pol(v1alpha1.OverridePolicyName("shop", v1alpha1.KindDeployment, "api"), v1alpha1.PriorityWorkloadOverride, v1alpha1.PolicySelector{
		Workloads: []v1alpha1.NamespacedWorkloadRef{{Kind: v1alpha1.KindDeployment, Namespace: "shop", Name: "api"}},
	}, off, off), v1alpha1.HubPolicyTypeWorkloadOverride)
	// A Git CR pretending to be a layer is an ordinary policy (no managed-by label).
	fake := pol("git-fake-default", 1, all, off, off)
	fake.Annotations = map[string]string{v1alpha1.AnnotationHubPolicyType: v1alpha1.HubPolicyTypeGlobalDefault}

	tests := []struct {
		name               string
		cluster            *v1alpha1.RightsizingPolicySpec
		policies           []v1alpha1.RightsizingPolicy
		cpu, mem           v1alpha1.Mode
		cpuS, memS         v1alpha1.ModeSource
		wantPol, wantOverr string
	}{
		{"hub global default", nil, []v1alpha1.RightsizingPolicy{global}, audit, off, srcGlobal, srcGlobal, "", ""},
		{"chart cluster default over hub global", clusterDefault("", audit), []v1alpha1.RightsizingPolicy{global}, audit, audit, srcGlobal, srcCluster, "", ""},
		{"hub cluster default over chart", clusterDefault("", auto), []v1alpha1.RightsizingPolicy{global, clusterHub}, audit, audit, srcGlobal, srcCluster, "", ""},
		{"winner over defaults, defaults do not compete", nil, []v1alpha1.RightsizingPolicy{global, clusterHub, team}, auto, audit, srcPolicy, srcCluster, "team", ""},
		{"override over winner", nil, []v1alpha1.RightsizingPolicy{global, team, override, otherOverride}, auto, off, srcPolicy, srcPolicy, "team", overrideName},
		{"override alone", nil, []v1alpha1.RightsizingPolicy{override}, audit, off, srcGlobal, srcPolicy, "", overrideName},
		{"git CR with the annotation competes", nil, []v1alpha1.RightsizingPolicy{fake}, off, off, srcPolicy, srcPolicy, "git-fake-default", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Resolve(web, tt.policies, tt.cluster)
			if e.CPUMode != tt.cpu || e.MemoryMode != tt.mem || e.CPUModeSource != tt.cpuS || e.MemoryModeSource != tt.memS ||
				e.Policy != tt.wantPol || e.Override != tt.wantOverr {
				t.Errorf("got cpu %s(%s) mem %s(%s) policy %q override %q", e.CPUMode, e.CPUModeSource, e.MemoryMode,
					e.MemoryModeSource, e.Policy, e.Override)
			}
		})
	}
	if e := Resolve(web, []v1alpha1.RightsizingPolicy{global, team}, nil); e.MinConfidence != 0.8 {
		t.Errorf("hub global default guardrail not layered: %v", e.MinConfidence)
	}
}

func TestSelectorWorkloads(t *testing.T) {
	sel := v1alpha1.PolicySelector{Workloads: []v1alpha1.NamespacedWorkloadRef{
		{Kind: v1alpha1.KindDeployment, Namespace: "shop", Name: "web"},
		{Kind: v1alpha1.KindStatefulSet, Namespace: "db", Name: "pg"},
	}}
	tests := []struct {
		w    Workload
		want bool
	}{
		{Workload{Namespace: "shop", Kind: v1alpha1.KindDeployment, Name: "web"}, true},
		{Workload{Namespace: "db", Kind: v1alpha1.KindStatefulSet, Name: "pg"}, true},
		{Workload{Namespace: "shop", Kind: v1alpha1.KindStatefulSet, Name: "web"}, false},
		{Workload{Namespace: "other", Kind: v1alpha1.KindDeployment, Name: "web"}, false},
		{Workload{Namespace: "shop", Kind: v1alpha1.KindDeployment, Name: "api"}, false},
	}
	for _, tt := range tests {
		if got := Matches(sel, tt.w); got != tt.want {
			t.Errorf("%+v: got %v", tt.w, got)
		}
	}
	// AND with the other criteria.
	sel.Namespaces = []string{"db"}
	if Matches(sel, Workload{Namespace: "shop", Kind: v1alpha1.KindDeployment, Name: "web"}) {
		t.Error("workloads must be combined with namespaces (AND)")
	}
}
