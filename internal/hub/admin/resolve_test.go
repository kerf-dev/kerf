package admin

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/api/v1alpha1/validate"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

var (
	checkout = hubapi.WorkloadKey{Cluster: "c1", Namespace: "payments", Kind: "Deployment", Name: "checkout"}
	api      = hubapi.WorkloadKey{Cluster: "c1", Namespace: "payments", Kind: "Deployment", Name: "api"}
	worker   = hubapi.WorkloadKey{Cluster: "c1", Namespace: "payments", Kind: "StatefulSet", Name: "worker"}
	web      = hubapi.WorkloadKey{Cluster: "c1", Namespace: "shop", Kind: "Deployment", Name: "web"}
	remote   = hubapi.WorkloadKey{Cluster: "c2", Namespace: "payments", Kind: "Deployment", Name: "checkout"}
)

func modes(cpu, mem string) v1alpha1.RightsizingPolicySpec {
	var s v1alpha1.RightsizingPolicySpec
	if cpu != "" {
		s.CPU = &v1alpha1.CPUPolicy{Mode: v1alpha1.Mode(cpu)}
	}
	if mem != "" {
		s.Memory = &v1alpha1.MemoryPolicy{Mode: v1alpha1.Mode(mem)}
	}
	return s
}

func global(spec v1alpha1.RightsizingPolicySpec) Candidate {
	return Candidate{Name: v1alpha1.GlobalDefaultsPolicyName, Origin: "hub", Type: v1alpha1.HubPolicyTypeGlobalDefault, PolicyID: "g", Spec: spec}
}

func clusterDef(cluster string, spec v1alpha1.RightsizingPolicySpec) Candidate {
	spec.Priority = v1alpha1.PriorityClusterDefault
	return Candidate{Name: v1alpha1.ClusterDefaultsPolicyName, Origin: "hub", Type: v1alpha1.HubPolicyTypeClusterDefault,
		PolicyID: "cd-" + cluster, Clusters: []string{cluster}, Spec: spec}
}

func hubPol(name string, prio int32, ns []string, spec v1alpha1.RightsizingPolicySpec, clusters ...string) Candidate {
	spec.Priority = prio
	spec.Selector.Namespaces = ns
	return Candidate{Name: name, Origin: "hub", Type: v1alpha1.HubPolicyTypePolicy, PolicyID: "p-" + name, Clusters: clusters, Spec: spec}
}

func gitPol(cluster, name string, prio int32, ns []string, spec v1alpha1.RightsizingPolicySpec) Candidate {
	spec.Priority = prio
	spec.Selector.Namespaces = ns
	return Candidate{Name: name, Origin: "git", Clusters: []string{cluster}, Spec: spec}
}

func override(k hubapi.WorkloadKey, spec v1alpha1.RightsizingPolicySpec) Candidate {
	kind := v1alpha1.WorkloadKind(k.Kind)
	spec.Priority = v1alpha1.PriorityWorkloadOverride
	spec.Selector = validate.OverrideSelector(k.Namespace, kind, k.Name)
	return Candidate{Name: v1alpha1.OverridePolicyName(k.Namespace, kind, k.Name), Origin: "hub",
		Type: v1alpha1.HubPolicyTypeWorkloadOverride, PolicyID: "o-" + k.Name, Clusters: []string{k.Cluster}, WorkloadID: k.ID(), Spec: spec}
}

func status(policy, cpu, cpuSrc, mem, memSrc string) *hubapi.WorkloadStatus {
	return &hubapi.WorkloadStatus{Policy: policy, Modes: hubapi.EffectiveModes{CPU: cpu, CPUSource: cpuSrc, Memory: mem, MemorySource: memSrc}}
}

func fieldMap(fs []hubapi.EffectiveField) map[string]hubapi.EffectiveField {
	m := map[string]hubapi.EffectiveField{}
	for _, f := range fs {
		m[f.Path] = f
	}
	return m
}

func em(cpu, cpuSrc, mem, memSrc string) hubapi.EffectiveModes {
	return hubapi.EffectiveModes{CPU: cpu, CPUSource: cpuSrc, Memory: mem, MemorySource: memSrc}
}

func TestResolveLayering(t *testing.T) {
	g := global(modes("audit", ""))
	g.Spec.Guardrails = &v1alpha1.Guardrails{MinConfidence: "0.8"}
	cd := clusterDef("c1", modes("", "off"))
	cd.Spec.Apply = &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyRollout}
	cd2 := clusterDef("c2", modes("off", "off"))
	win := hubPol("payments-cpu-auto", 100, []string{"payments"}, modes("auto-patch", ""), "c1")
	lose := gitPol("c1", "team-defaults", 50, []string{"pay*"}, modes("off", ""))
	lose.Spec.Profile = v1alpha1.ProfileDev
	ov := override(checkout, modes("", "audit"))
	otherOv := override(api, modes("off", "off"))
	cands := []Candidate{otherOv, ov, lose, win, cd2, cd, g}

	st := status("payments-cpu-auto", "auto-patch", "policy", "audit", "policy")
	st.Override = ov.Name
	ep := Resolve(Workload{Key: checkout, Status: st}, cands)
	if ep.WorkloadID != checkout.ID() {
		t.Fatalf("workloadId %q", ep.WorkloadID)
	}
	var got []string
	for _, l := range ep.Layers {
		got = append(got, l.Source+":"+l.Name)
	}
	want := []string{"built-in:", "global-default:ro-global-defaults", "cluster-default:ro-cluster-defaults",
		"policy:team-defaults", "policy:payments-cpu-auto", "workload-override:" + ov.Name}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layers %v, want %v", got, want)
	}
	l := ep.Layers[3]
	if l.Applied || l.Origin != "git" || l.Spec != nil || l.Note != "priority 50 < 100 of payments-cpu-auto" || *l.Priority != 50 {
		t.Errorf("losing layer %+v", l)
	}
	if l := ep.Layers[4]; !l.Applied || l.Origin != "hub" || l.PolicyID != "p-payments-cpu-auto" || l.Spec == nil || *l.Priority != 100 {
		t.Errorf("winning layer %+v", l)
	}
	if l := ep.Layers[0]; !l.Applied || l.Spec != nil || l.Priority != nil {
		t.Errorf("built-in layer %+v", l)
	}

	fm := fieldMap(ep.Fields)
	checks := []struct{ path, value, source, name string }{
		{"cpu.mode", "auto-patch", hubapi.LayerPolicy, "payments-cpu-auto"},
		{"memory.mode", "audit", hubapi.LayerWorkloadOverride, ov.Name},
		{"guardrails.minConfidence", "0.8", hubapi.LayerGlobalDefault, "ro-global-defaults"},
		{"apply.strategy", "rollout", hubapi.LayerClusterDefault, "ro-cluster-defaults"},
		{"profile", "production", hubapi.LayerBuiltIn, ""},
		{"apply.fallbackAfter", "10m0s", hubapi.LayerBuiltIn, ""},
		{"guardrails.learningPeriod", "168h0m0s", hubapi.LayerBuiltIn, ""},
		{"guardrails.minCPU", "10m", hubapi.LayerBuiltIn, ""},
		{"guardrails.minMemory", "32Mi", hubapi.LayerBuiltIn, ""},
		{"guardrails.maxChangePercent", "50", hubapi.LayerBuiltIn, ""},
		{"guardrails.rollback.oomKills", "1", hubapi.LayerBuiltIn, ""},
		{"guardrails.rollback.throttlingRatio", "0.25", hubapi.LayerBuiltIn, ""},
		{"memory.limitEqualsRequest", "true", hubapi.LayerBuiltIn, ""},
		{"cpu.limits", "keep", hubapi.LayerBuiltIn, ""},
		{"jvm.detect", "true", hubapi.LayerBuiltIn, ""},
		{"hpa.coordinate", "false", hubapi.LayerBuiltIn, ""},
		{"hpa.targetBounds.maxUtilization", "90", hubapi.LayerBuiltIn, ""},
	}
	for _, c := range checks {
		f, ok := fm[c.path]
		if !ok {
			t.Errorf("field %s missing", c.path)
			continue
		}
		if f.Value != c.value || f.Source != c.source || f.Name != c.name {
			t.Errorf("field %s = %+v, want %s from %s/%s", c.path, f, c.value, c.source, c.name)
		}
	}
	for p := range fm {
		if strings.HasPrefix(p, "selector") || p == "priority" {
			t.Errorf("unexpected field %s", p)
		}
	}
	if !sort.SliceIsSorted(ep.Fields, func(i, j int) bool { return ep.Fields[i].Path < ep.Fields[j].Path }) {
		t.Error("fields not sorted")
	}
	if want := em("auto-patch", "policy", "audit", "policy"); ep.Modes != want {
		t.Errorf("modes %+v, want %+v", ep.Modes, want)
	}
	if ep.AgentModes == nil || ep.AgentPolicy != "payments-cpu-auto" {
		t.Errorf("agent %+v %q", ep.AgentModes, ep.AgentPolicy)
	}
	if ep.Warnings != nil {
		t.Errorf("warnings %v", ep.Warnings)
	}
}

func TestResolveModes(t *testing.T) {
	sel := func(c Candidate, f func(*v1alpha1.PolicySelector)) Candidate {
		f(&c.Spec.Selector)
		return c
	}
	tests := []struct {
		name   string
		key    hubapi.WorkloadKey
		cands  []Candidate
		want   hubapi.EffectiveModes
		policy string
	}{
		{"built-in", checkout, nil, em("audit", "global-default", "audit", "global-default"), ""},
		{"global default", checkout, []Candidate{global(modes("off", ""))}, em("off", "global-default", "audit", "global-default"), ""},
		{"cluster default of own cluster", checkout, []Candidate{clusterDef("c1", modes("auto-patch", ""))},
			em("auto-patch", "cluster-default", "audit", "global-default"), ""},
		{"cluster default of other cluster", checkout, []Candidate{clusterDef("c2", modes("auto-patch", ""))},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"policy on other cluster", checkout, []Candidate{hubPol("p", 5, nil, modes("off", ""), "c2")},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"policy on all clusters", remote, []Candidate{hubPol("p", 5, nil, modes("off", ""))},
			em("off", "policy", "audit", "global-default"), "p"},
		{"git cr of other cluster", remote, []Candidate{gitPol("c1", "g", 5, nil, modes("off", ""))},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"higher priority wins", checkout, []Candidate{hubPol("a", 5, nil, modes("off", "")), gitPol("c1", "b", 6, nil, modes("auto-patch", ""))},
			em("auto-patch", "policy", "audit", "global-default"), "b"},
		{"tie goes to lowest name", checkout, []Candidate{hubPol("b", 5, nil, modes("off", "")), gitPol("c1", "a", 5, nil, modes("auto-patch", ""))},
			em("auto-patch", "policy", "audit", "global-default"), "a"},
		{"no field merge across policies", checkout, []Candidate{hubPol("a", 5, nil, modes("off", "")), hubPol("b", 6, nil, modes("", "off"))},
			em("audit", "global-default", "off", "policy"), "b"},
		{"namespace mismatch", web, []Candidate{hubPol("a", 5, []string{"payments"}, modes("off", ""))},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"excluded namespace", checkout, []Candidate{sel(hubPol("a", 5, nil, modes("off", "")), func(s *v1alpha1.PolicySelector) { s.ExcludeNamespaces = []string{"pay*"} })},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"kind mismatch", worker, []Candidate{sel(hubPol("a", 5, nil, modes("off", "")), func(s *v1alpha1.PolicySelector) { s.Kinds = []v1alpha1.WorkloadKind{"Deployment"} })},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"selector.workloads excludes", api, []Candidate{sel(hubPol("a", 5, nil, modes("off", "")), func(s *v1alpha1.PolicySelector) {
			s.Workloads = []v1alpha1.NamespacedWorkloadRef{{Kind: "Deployment", Namespace: "payments", Name: "checkout"}}
		})}, em("audit", "global-default", "audit", "global-default"), ""},
		{"selector.workloads includes", checkout, []Candidate{sel(hubPol("a", 5, nil, modes("off", "")), func(s *v1alpha1.PolicySelector) {
			s.Workloads = []v1alpha1.NamespacedWorkloadRef{{Kind: "Deployment", Namespace: "payments", Name: "checkout"}}
		})}, em("off", "policy", "audit", "global-default"), "a"},
		{"override of other workload", checkout, []Candidate{override(api, modes("off", "off"))},
			em("audit", "global-default", "audit", "global-default"), ""},
		{"override beats policy", checkout, []Candidate{override(checkout, modes("off", "")), hubPol("a", 899999, nil, modes("auto-patch", "auto-patch"))},
			em("off", "policy", "auto-patch", "policy"), "a"},
		{"unknown type ignored", checkout, []Candidate{{Name: "x", Type: "bogus", Spec: modes("off", "off")}},
			em("audit", "global-default", "audit", "global-default"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := Workload{Key: tc.key}
			if got := Modes(w, tc.cands); got != tc.want {
				t.Errorf("modes %+v, want %+v", got, tc.want)
			}
			if got := resolve(w, tc.cands).winner; got != tc.policy {
				t.Errorf("winner %q, want %q", got, tc.policy)
			}
		})
	}
}

func TestResolveTieNote(t *testing.T) {
	ep := Resolve(Workload{Key: checkout}, []Candidate{hubPol("b", 5, nil, modes("off", "")), hubPol("a", 5, nil, modes("off", ""))})
	l := ep.Layers[1]
	if l.Name != "b" || l.Applied || l.Note != "priority 5 = 5 of a; ties go to the lowest name" {
		t.Errorf("tie layer %+v", l)
	}
	if ep.Layers[2].Name != "a" || !ep.Layers[2].Applied {
		t.Errorf("winner layer %+v", ep.Layers[2])
	}
}

func TestResolveAnnotations(t *testing.T) {
	pol := hubPol("p", 5, nil, modes("auto-patch", "auto-patch"))
	tests := []struct {
		name   string
		st     *hubapi.WorkloadStatus
		layers []string // annotation layer names
		want   hubapi.EffectiveModes
	}{
		{"none", status("p", "auto-patch", "policy", "auto-patch", "policy"), nil, em("auto-patch", "policy", "auto-patch", "policy")},
		{"memory", status("p", "auto-patch", "policy", "off", "annotation"), []string{v1alpha1.AnnotationMemoryMode},
			em("auto-patch", "policy", "off", "annotation")},
		{"cpu", status("p", "off", "annotation", "auto-patch", "policy"), []string{v1alpha1.AnnotationCPUMode},
			em("off", "annotation", "auto-patch", "policy")},
		{"both same", status("p", "off", "annotation", "off", "annotation"), []string{v1alpha1.AnnotationMode},
			em("off", "annotation", "off", "annotation")},
		{"both differ", status("p", "off", "annotation", "audit", "annotation"), []string{v1alpha1.AnnotationCPUMode, v1alpha1.AnnotationMemoryMode},
			em("off", "annotation", "audit", "annotation")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := Resolve(Workload{Key: checkout, Status: tc.st}, []Candidate{pol})
			var names []string
			for _, l := range ep.Layers {
				if l.Source == hubapi.LayerAnnotation {
					if !l.Applied || l.Spec == nil || l.Origin != "" || l.Priority != nil {
						t.Errorf("annotation layer %+v", l)
					}
					names = append(names, l.Name)
				}
			}
			if !reflect.DeepEqual(names, tc.layers) {
				t.Errorf("annotation layers %v, want %v", names, tc.layers)
			}
			if ep.Modes != tc.want {
				t.Errorf("modes %+v, want %+v", ep.Modes, tc.want)
			}
			if ep.Warnings != nil {
				t.Errorf("warnings %v", ep.Warnings)
			}
			if tc.layers != nil {
				f := fieldMap(ep.Fields)
				for _, p := range []string{"cpu.mode", "memory.mode"} {
					if f[p].Source == hubapi.LayerAnnotation && !strings.HasPrefix(f[p].Name, v1alpha1.Prefix) {
						t.Errorf("field %s name %q", p, f[p].Name)
					}
				}
			}
		})
	}
}

func TestResolveLabels(t *testing.T) {
	labelled := func(name string, prio int32, ns []string) Candidate {
		c := hubPol(name, prio, ns, modes("auto-patch", ""))
		c.Spec.Selector.MatchLabels = map[string]string{"team": "pay"}
		return c
	}
	warn := labelWarning
	tests := []struct {
		name     string
		st       *hubapi.WorkloadStatus
		cands    []Candidate
		winner   string
		warnings []string
	}{
		{"unconfirmed, no other policy", nil, []Candidate{labelled("lab", 5, nil)}, "", []string{warnNoStatus, warn("lab")}},
		{"confirmed by agent", status("lab", "auto-patch", "policy", "audit", "global-default"), []Candidate{labelled("lab", 5, nil)}, "lab", []string{warn("lab")}},
		{"unconfirmed, outranks winner", status("plain", "off", "policy", "audit", "global-default"),
			[]Candidate{labelled("lab", 50, nil), hubPol("plain", 5, nil, modes("off", ""))}, "plain", []string{warn("lab")}},
		{"unconfirmed, loses anyway", status("plain", "off", "policy", "audit", "global-default"),
			[]Candidate{labelled("lab", 1, nil), hubPol("plain", 5, nil, modes("off", ""))}, "plain", nil},
		{"namespace mismatch", status("", "audit", "global-default", "audit", "global-default"),
			[]Candidate{labelled("lab", 5, []string{"shop"})}, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := Resolve(Workload{Key: checkout, Status: tc.st}, tc.cands)
			if got := resolve(Workload{Key: checkout, Status: tc.st}, tc.cands).winner; got != tc.winner {
				t.Errorf("winner %q, want %q", got, tc.winner)
			}
			if !reflect.DeepEqual(ep.Warnings, tc.warnings) {
				t.Errorf("warnings %q, want %q", ep.Warnings, tc.warnings)
			}
		})
	}
}

func TestResolveWarnings(t *testing.T) {
	pol := hubPol("p", 5, nil, modes("auto-patch", ""))
	ov := override(checkout, modes("", "off"))
	tests := []struct {
		name string
		st   *hubapi.WorkloadStatus
		want []string
	}{
		{"no status", nil, []string{warnNoStatus}},
		{"agrees", &hubapi.WorkloadStatus{Policy: "p", Override: ov.Name, Modes: em("auto-patch", "policy", "off", "policy")}, nil},
		{"agent without sources", &hubapi.WorkloadStatus{Policy: "p", Override: ov.Name, Modes: hubapi.EffectiveModes{CPU: "auto-patch", Memory: "off"}}, nil},
		{"differs", &hubapi.WorkloadStatus{Modes: em("audit", "global-default", "off", "policy")}, []string{
			"agent reports cpu audit (global-default); the hub computes auto-patch (policy)",
			`agent reports policy none; the hub computes "p"`,
			`agent reports override none; the hub computes "` + ov.Name + `"`,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := Resolve(Workload{Key: checkout, Status: tc.st}, []Candidate{pol, ov})
			if !reflect.DeepEqual(ep.Warnings, tc.want) {
				t.Errorf("warnings %q, want %q", ep.Warnings, tc.want)
			}
			if (tc.st == nil) != (ep.AgentModes == nil) {
				t.Errorf("agentModes %+v", ep.AgentModes)
			}
		})
	}
}

func TestResolveFieldValues(t *testing.T) {
	g := global(v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitRatio, LimitRatio: "2"}})
	f := fieldMap(Resolve(Workload{Key: checkout}, []Candidate{g}).Fields)
	if f["cpu.limits"].Value != "ratio" || f["cpu.limitRatio"].Value != "2" || f["cpu.limitRatio"].Source != hubapi.LayerGlobalDefault {
		t.Errorf("ratio fields %+v %+v", f["cpu.limits"], f["cpu.limitRatio"])
	}

	start := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	no := false
	p := hubPol("p", 5, nil, v1alpha1.RightsizingPolicySpec{
		CPU:    &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitKeep},
		Memory: &v1alpha1.MemoryPolicy{LimitEqualsRequest: &no},
		Apply: &v1alpha1.ApplyPolicy{FreezeWindows: []v1alpha1.TimeWindow{
			{Schedule: "0 22 * * 5", Duration: &metav1.Duration{Duration: 2 * time.Hour}, TimeZone: "UTC"},
			{Start: &start, End: &start},
		}},
	})
	f = fieldMap(Resolve(Workload{Key: checkout}, []Candidate{g, p}).Fields)
	if _, ok := f["cpu.limitRatio"]; ok {
		t.Errorf("limitRatio kept after limits changed: %+v", f["cpu.limitRatio"])
	}
	if f["cpu.limits"].Value != "keep" || f["cpu.limits"].Name != "p" {
		t.Errorf("limits %+v", f["cpu.limits"])
	}
	if f["memory.limitEqualsRequest"].Value != "false" || f["memory.limitEqualsRequest"].Source != hubapi.LayerPolicy {
		t.Errorf("limitEqualsRequest %+v", f["memory.limitEqualsRequest"])
	}
	want := `[{"schedule":"0 22 * * 5","duration":"2h0m0s","timeZone":"UTC"},{"start":"2026-01-02T03:04:05Z","end":"2026-01-02T03:04:05Z"}]`
	if got := f["apply.freezeWindows"].Value; got != want {
		t.Errorf("freezeWindows %s, want %s", got, want)
	}
	if _, ok := f["apply.maintenanceWindows"]; ok {
		t.Error("unset maintenanceWindows listed")
	}
}

func TestModeSource(t *testing.T) {
	for in, want := range map[string]string{
		hubapi.LayerBuiltIn: "global-default", hubapi.LayerGlobalDefault: "global-default",
		hubapi.LayerClusterDefaultChart: "cluster-default", hubapi.LayerClusterDefault: "cluster-default",
		hubapi.LayerPolicy: "policy", hubapi.LayerWorkloadOverride: "policy", hubapi.LayerAnnotation: "annotation",
	} {
		if got := modeSource(in); got != want {
			t.Errorf("modeSource(%s) = %s, want %s", in, got, want)
		}
	}
}
