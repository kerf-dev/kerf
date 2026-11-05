package validate

import (
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

func ptr[T any](v T) *T { return &v }

func dur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func ts(s string) *metav1.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &metav1.Time{Time: t}
}

// errPaths returns "type:path" of each error, for compact assertions.
func errPaths(errs field.ErrorList) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, string(e.Type)+":"+e.Field)
	}
	return out
}

func TestSpec(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.RightsizingPolicySpec
		want []string // "type:path"; empty = valid
	}{
		{name: "empty spec", spec: v1alpha1.RightsizingPolicySpec{}},
		{name: "full valid spec", spec: v1alpha1.RightsizingPolicySpec{
			Selector: v1alpha1.PolicySelector{
				Namespaces:        []string{"team-*", "payments"},
				ExcludeNamespaces: []string{"kube-*"},
				MatchLabels:       map[string]string{"app.kubernetes.io/part-of": "shop"},
				Kinds:             []v1alpha1.WorkloadKind{v1alpha1.KindDeployment, v1alpha1.KindStatefulSet},
				Workloads:         []v1alpha1.NamespacedWorkloadRef{{Kind: v1alpha1.KindDeployment, Namespace: "payments", Name: "checkout"}},
			},
			Priority: 100,
			Profile:  v1alpha1.ProfileProduction,
			CPU:      &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch, Limits: v1alpha1.CPULimitRatio, LimitRatio: "1.5"},
			Memory:   &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAudit, LimitEqualsRequest: ptr(true)},
			Apply: &v1alpha1.ApplyPolicy{
				Strategy:           v1alpha1.StrategyDeferred,
				FallbackAfter:      dur(10 * time.Minute),
				MaintenanceWindows: []v1alpha1.TimeWindow{{Schedule: "0 2 * * 1-5", Duration: dur(2 * time.Hour), TimeZone: "Europe/Berlin"}},
				FreezeWindows:      []v1alpha1.TimeWindow{{Start: ts("2026-12-20T00:00:00Z"), End: ts("2027-01-02T00:00:00Z")}},
			},
			Guardrails: &v1alpha1.Guardrails{
				LearningPeriod: dur(168 * time.Hour), MinConfidence: "0.7", MaxChangePercent: ptr[int32](50),
				MinCPU: ptr(resource.MustParse("10m")), MinMemory: ptr(resource.MustParse("32Mi")), Cooldown: dur(24 * time.Hour),
				Rollback: &v1alpha1.RollbackPolicy{WatchDuration: dur(30 * time.Minute), OOMKills: ptr[int32](1), Restarts: ptr[int32](3), ThrottlingRatio: "0.25"},
			},
			HPA: &v1alpha1.HPAPolicy{Coordinate: ptr(true), TargetBounds: &v1alpha1.HPATargetBounds{MinUtilization: ptr[int32](40), MaxUtilization: ptr[int32](90)}},
			JVM: &v1alpha1.JVMPolicy{Detect: ptr(false)},
		}},
		{name: "priority too high", spec: v1alpha1.RightsizingPolicySpec{Priority: 1000001}, want: []string{"FieldValueInvalid:spec.priority"}},
		{name: "negative priority", spec: v1alpha1.RightsizingPolicySpec{Priority: -1}, want: []string{"FieldValueInvalid:spec.priority"}},
		{name: "bad profile", spec: v1alpha1.RightsizingPolicySpec{Profile: "custom"}, want: []string{"FieldValueNotSupported:spec.profile"}},
		{name: "bad cpu mode", spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Mode: "auto"}}, want: []string{"FieldValueNotSupported:spec.cpu.mode"}},
		{name: "bad memory mode", spec: v1alpha1.RightsizingPolicySpec{Memory: &v1alpha1.MemoryPolicy{Mode: "on"}}, want: []string{"FieldValueNotSupported:spec.memory.mode"}},
		{name: "bad cpu limits", spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Limits: "double"}}, want: []string{"FieldValueNotSupported:spec.cpu.limits"}},
		{name: "ratio without limitRatio", spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitRatio}}, want: []string{"FieldValueRequired:spec.cpu.limitRatio"}},
		{name: "limitRatio without ratio", spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitKeep, LimitRatio: "2"}}, want: []string{"FieldValueForbidden:spec.cpu.limitRatio"}},
		{name: "limitRatio below 1", spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitRatio, LimitRatio: "0.5"}}, want: []string{"FieldValueInvalid:spec.cpu.limitRatio"}},
		{name: "limitRatio 4 decimals", spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Limits: v1alpha1.CPULimitRatio, LimitRatio: "1.2345"}}, want: []string{"FieldValueInvalid:spec.cpu.limitRatio"}},
		{name: "bad strategy", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{Strategy: "recreate"}}, want: []string{"FieldValueNotSupported:spec.apply.strategy"}},
		{name: "negative fallbackAfter", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{FallbackAfter: dur(-time.Minute)}}, want: []string{"FieldValueInvalid:spec.apply.fallbackAfter"}},
		{name: "minConfidence above 1", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{MinConfidence: "1.5"}}, want: []string{"FieldValueInvalid:spec.guardrails.minConfidence"}},
		{name: "minConfidence 1.000 ok", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{MinConfidence: "1.000"}}},
		{name: "maxChangePercent 0", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{MaxChangePercent: ptr[int32](0)}}, want: []string{"FieldValueInvalid:spec.guardrails.maxChangePercent"}},
		{name: "maxChangePercent 101", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{MaxChangePercent: ptr[int32](101)}}, want: []string{"FieldValueInvalid:spec.guardrails.maxChangePercent"}},
		{name: "negative minCPU", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{MinCPU: ptr(resource.MustParse("-1m"))}}, want: []string{"FieldValueInvalid:spec.guardrails.minCPU"}},
		{name: "rollback counts 0", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{Rollback: &v1alpha1.RollbackPolicy{OOMKills: ptr[int32](0), Restarts: ptr[int32](0)}}},
			want: []string{"FieldValueInvalid:spec.guardrails.rollback.oomKills", "FieldValueInvalid:spec.guardrails.rollback.restarts"}},
		{name: "bad throttlingRatio", spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{Rollback: &v1alpha1.RollbackPolicy{ThrottlingRatio: ".25"}}}, want: []string{"FieldValueInvalid:spec.guardrails.rollback.throttlingRatio"}},
		{name: "hpa bounds out of range", spec: v1alpha1.RightsizingPolicySpec{HPA: &v1alpha1.HPAPolicy{TargetBounds: &v1alpha1.HPATargetBounds{MinUtilization: ptr[int32](0), MaxUtilization: ptr[int32](101)}}},
			want: []string{"FieldValueInvalid:spec.hpa.targetBounds.minUtilization", "FieldValueInvalid:spec.hpa.targetBounds.maxUtilization"}},
		{name: "hpa min > max", spec: v1alpha1.RightsizingPolicySpec{HPA: &v1alpha1.HPAPolicy{TargetBounds: &v1alpha1.HPATargetBounds{MinUtilization: ptr[int32](80), MaxUtilization: ptr[int32](50)}}},
			want: []string{"FieldValueInvalid:spec.hpa.targetBounds"}},
		{name: "too many kinds and duplicate", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Kinds: []v1alpha1.WorkloadKind{"Deployment", "Deployment", "StatefulSet", "DaemonSet", "CronJob"}}},
			want: []string{"FieldValueTooMany:spec.selector.kinds", "FieldValueDuplicate:spec.selector.kinds[1]"}},
		{name: "unknown kind", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Kinds: []v1alpha1.WorkloadKind{"Job"}}}, want: []string{"FieldValueNotSupported:spec.selector.kinds[0]"}},
		{name: "bad glob and duplicate namespace", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Namespaces: []string{"team-[", "a", "a"}}},
			want: []string{"FieldValueInvalid:spec.selector.namespaces[0]", "FieldValueDuplicate:spec.selector.namespaces[2]"}},
		{name: "namespace glob too long", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{ExcludeNamespaces: []string{strings.Repeat("a", 254)}}},
			want: []string{"FieldValueTooLong:spec.selector.excludeNamespaces[0]"}},
		{name: "bad label", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{MatchLabels: map[string]string{"bad key": "ok"}}},
			want: []string{"FieldValueInvalid:spec.selector.matchLabels[bad key]"}},
		{name: "workload ref missing fields", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Workloads: []v1alpha1.NamespacedWorkloadRef{{}}}},
			want: []string{"FieldValueRequired:spec.selector.workloads[0].kind", "FieldValueRequired:spec.selector.workloads[0].namespace", "FieldValueRequired:spec.selector.workloads[0].name"}},
		{name: "workload ref bad kind", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Workloads: []v1alpha1.NamespacedWorkloadRef{{Kind: "Pod", Namespace: "a", Name: "b"}}}},
			want: []string{"FieldValueNotSupported:spec.selector.workloads[0].kind"}},
		{name: "too many workloads", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Workloads: make101()}},
			want: []string{"FieldValueTooMany:spec.selector.workloads"}},
		{name: "window schedule and start", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{FreezeWindows: []v1alpha1.TimeWindow{{Schedule: "0 2 * * *", Duration: dur(time.Hour), Start: ts("2026-01-01T00:00:00Z")}}}},
			want: []string{"FieldValueInvalid:spec.apply.freezeWindows[0]"}},
		{name: "window schedule without duration", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{MaintenanceWindows: []v1alpha1.TimeWindow{{Schedule: "0 2 * * *"}}}},
			want: []string{"FieldValueInvalid:spec.apply.maintenanceWindows[0]"}},
		{name: "window bad cron", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{MaintenanceWindows: []v1alpha1.TimeWindow{{Schedule: "every night", Duration: dur(time.Hour)}}}},
			want: []string{"FieldValueInvalid:spec.apply.maintenanceWindows[0].schedule"}},
		{name: "window zero duration", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{MaintenanceWindows: []v1alpha1.TimeWindow{{Schedule: "0 2 * * *", Duration: dur(0)}}}},
			want: []string{"FieldValueInvalid:spec.apply.maintenanceWindows[0].duration"}},
		{name: "window bad time zone", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{MaintenanceWindows: []v1alpha1.TimeWindow{{Schedule: "0 2 * * *", Duration: dur(time.Hour), TimeZone: "Mars/Olympus"}}}},
			want: []string{"FieldValueInvalid:spec.apply.maintenanceWindows[0].timeZone"}},
		{name: "one-off window end before start", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{FreezeWindows: []v1alpha1.TimeWindow{{Start: ts("2026-02-01T00:00:00Z"), End: ts("2026-01-01T00:00:00Z")}}}},
			want: []string{"FieldValueInvalid:spec.apply.freezeWindows[0].end"}},
		{name: "one-off window without end", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{FreezeWindows: []v1alpha1.TimeWindow{{Start: ts("2026-02-01T00:00:00Z")}}}},
			want: []string{"FieldValueInvalid:spec.apply.freezeWindows[0]"}},
		{name: "too many windows", spec: v1alpha1.RightsizingPolicySpec{Apply: &v1alpha1.ApplyPolicy{FreezeWindows: make([]v1alpha1.TimeWindow, 17)}},
			want: append([]string{"FieldValueTooMany:spec.apply.freezeWindows"}, repeatWindowErrs(17)...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errPaths(Spec(&tt.spec, field.NewPath("spec")))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("errors:\n got  %v\n want %v", got, tt.want)
			}
		})
	}
}

func make101() []v1alpha1.NamespacedWorkloadRef {
	out := make([]v1alpha1.NamespacedWorkloadRef, 101)
	for i := range out {
		out[i] = v1alpha1.NamespacedWorkloadRef{Kind: v1alpha1.KindDeployment, Namespace: "ns", Name: "w"}
	}
	return out
}

func repeatWindowErrs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "FieldValueInvalid:spec.apply.freezeWindows[" + itoa(i) + "]"
	}
	return out
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func TestHubPolicy(t *testing.T) {
	sel := OverrideSelector("payments", v1alpha1.KindDeployment, "checkout")
	ovName := v1alpha1.OverridePolicyName("payments", v1alpha1.KindDeployment, "checkout")
	tests := []struct {
		name   string
		typ    string
		crName string
		spec   v1alpha1.RightsizingPolicySpec
		want   []string
	}{
		{name: "global default", typ: v1alpha1.HubPolicyTypeGlobalDefault, crName: "ro-global-defaults", spec: v1alpha1.RightsizingPolicySpec{Priority: 0}},
		{name: "global default wrong name and priority", typ: v1alpha1.HubPolicyTypeGlobalDefault, crName: "defaults", spec: v1alpha1.RightsizingPolicySpec{Priority: 5},
			want: []string{"FieldValueInvalid:name", "FieldValueInvalid:spec.priority"}},
		{name: "global default with selector", typ: v1alpha1.HubPolicyTypeGlobalDefault, crName: "ro-global-defaults", spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Namespaces: []string{"a"}}},
			want: []string{"FieldValueForbidden:spec.selector"}},
		{name: "cluster default", typ: v1alpha1.HubPolicyTypeClusterDefault, crName: "ro-cluster-defaults", spec: v1alpha1.RightsizingPolicySpec{Priority: 10}},
		{name: "cluster default wrong priority", typ: v1alpha1.HubPolicyTypeClusterDefault, crName: "ro-cluster-defaults", spec: v1alpha1.RightsizingPolicySpec{Priority: 0},
			want: []string{"FieldValueInvalid:spec.priority"}},
		{name: "policy", typ: v1alpha1.HubPolicyTypePolicy, crName: "payments-auto", spec: v1alpha1.RightsizingPolicySpec{Priority: 100}},
		{name: "policy reserved prefix", typ: v1alpha1.HubPolicyTypePolicy, crName: "ro-mine", spec: v1alpha1.RightsizingPolicySpec{Priority: 100},
			want: []string{"FieldValueInvalid:name"}},
		{name: "policy priority 0", typ: v1alpha1.HubPolicyTypePolicy, crName: "p", spec: v1alpha1.RightsizingPolicySpec{Priority: 0},
			want: []string{"FieldValueInvalid:spec.priority"}},
		{name: "policy priority in override band", typ: v1alpha1.HubPolicyTypePolicy, crName: "p", spec: v1alpha1.RightsizingPolicySpec{Priority: 900000},
			want: []string{"FieldValueInvalid:spec.priority"}},
		{name: "policy bad name", typ: v1alpha1.HubPolicyTypePolicy, crName: "Payments_Auto", spec: v1alpha1.RightsizingPolicySpec{Priority: 100},
			want: []string{"FieldValueInvalid:name"}},
		{name: "override", typ: v1alpha1.HubPolicyTypeWorkloadOverride, crName: ovName, spec: v1alpha1.RightsizingPolicySpec{Priority: 900000, Selector: sel,
			CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeOff}, Memory: &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeOff}}},
		{name: "override wrong name", typ: v1alpha1.HubPolicyTypeWorkloadOverride, crName: "ro-override-0000000000000000", spec: v1alpha1.RightsizingPolicySpec{Priority: 900000, Selector: sel},
			want: []string{"FieldValueInvalid:name"}},
		{name: "override without workload", typ: v1alpha1.HubPolicyTypeWorkloadOverride, crName: ovName, spec: v1alpha1.RightsizingPolicySpec{Priority: 900000},
			want: []string{"FieldValueInvalid:spec.selector.workloads"}},
		{name: "override missing narrowing", typ: v1alpha1.HubPolicyTypeWorkloadOverride, crName: ovName, spec: v1alpha1.RightsizingPolicySpec{Priority: 900000,
			Selector: v1alpha1.PolicySelector{Workloads: sel.Workloads}},
			want: []string{"FieldValueInvalid:spec.selector"}},
		{name: "override wrong priority", typ: v1alpha1.HubPolicyTypeWorkloadOverride, crName: ovName, spec: v1alpha1.RightsizingPolicySpec{Priority: 899999, Selector: sel},
			want: []string{"FieldValueInvalid:spec.priority"}},
		{name: "unknown type", typ: "git", crName: "p", spec: v1alpha1.RightsizingPolicySpec{},
			want: []string{"FieldValueNotSupported:type"}},
		{name: "spec errors are included", typ: v1alpha1.HubPolicyTypePolicy, crName: "p", spec: v1alpha1.RightsizingPolicySpec{Priority: 100, CPU: &v1alpha1.CPUPolicy{Mode: "x"}},
			want: []string{"FieldValueNotSupported:spec.cpu.mode"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errPaths(HubPolicy(tt.typ, tt.crName, &tt.spec))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("errors:\n got  %v\n want %v", got, tt.want)
			}
		})
	}
}

func TestOverridePolicyName(t *testing.T) {
	a := v1alpha1.OverridePolicyName("payments", v1alpha1.KindDeployment, "checkout")
	b := v1alpha1.OverridePolicyName("payments", v1alpha1.KindStatefulSet, "checkout")
	if a == b || !strings.HasPrefix(a, v1alpha1.OverridePolicyNamePrefix) || len(a) != len(v1alpha1.OverridePolicyNamePrefix)+16 {
		t.Fatalf("names %q %q", a, b)
	}
	// Stable across releases: hub and agent compute it independently.
	if a != "ro-override-4f6e1e27aa6bac08" {
		t.Fatalf("OverridePolicyName changed: %q", a)
	}
}

// TestExamplesValid keeps the shipped example policies valid.
func TestExamplesValid(t *testing.T) {
	b, err := os.ReadFile("../../examples/rightsizingpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, doc := range strings.Split(string(b), "\n---") {
		if !strings.Contains(doc, "kind: RightsizingPolicy") {
			continue
		}
		var p v1alpha1.RightsizingPolicy
		if err := yaml.UnmarshalStrict([]byte(doc), &p); err != nil {
			t.Fatalf("parse: %v", err)
		}
		n++
		if errs := Spec(&p.Spec, field.NewPath("spec")); len(errs) > 0 {
			t.Errorf("%s: %v", p.Name, errs)
		}
	}
	if n == 0 {
		t.Fatal("no example policies found")
	}
}
