package apply

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

func deploy(replicas int32, strategy appsv1.DeploymentStrategy, ann map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns", Annotations: ann},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas), Strategy: strategy,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}},
		},
	}
}

func rolling(surge, unavailable *intstr.IntOrString) appsv1.DeploymentStrategy {
	return appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: surge, MaxUnavailable: unavailable}}
}

func pdb(name string, minAvailable, maxUnavailable *intstr.IntOrString) policyv1.PodDisruptionBudget {
	return policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: minAvailable, MaxUnavailable: maxUnavailable,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
	}
}

func TestAssessDisruption(t *testing.T) {
	one, zero := intstr.FromInt32(1), intstr.FromInt32(0)
	pct50 := intstr.FromString("50%")
	sts := func(replicas int32) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns"},
			Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(replicas),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}
	}
	tests := []struct {
		name          string
		obj           interface{}
		pdbs          []policyv1.PodDisruptionBudget
		single, safe  bool
		allowed       bool
		whyContains   string
		rolloutReason string
	}{
		{name: "one replica, default strategy (25%/25% -> surge 1, unavailable 0)", obj: deploy(1, appsv1.DeploymentStrategy{}, nil),
			single: true, safe: true, whyContains: "replicas: 1"},
		{name: "one replica, Recreate", obj: deploy(1, appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, nil),
			single: true, safe: false, rolloutReason: "Recreate"},
		{name: "one replica, maxUnavailable 1", obj: deploy(1, rolling(&one, &one), nil), single: true, safe: false, rolloutReason: "maxUnavailable"},
		{name: "one replica, maxSurge 0 maxUnavailable 0 means unavailable 1", obj: deploy(1, rolling(&zero, &zero), nil), single: true, safe: false},
		{name: "one replica, surge 1 unavailable 0", obj: deploy(1, rolling(&one, &zero), nil), single: true, safe: true},
		{name: "opt-in annotation", obj: deploy(1, appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			map[string]string{v1alpha1.AnnotationAllowDisruption: "true"}), single: true, safe: false, allowed: true},
		{name: "three replicas, no PDB", obj: deploy(3, appsv1.DeploymentStrategy{}, nil), single: false, safe: true},
		{name: "three replicas, PDB minAvailable 3", obj: deploy(3, appsv1.DeploymentStrategy{}, nil),
			pdbs: []policyv1.PodDisruptionBudget{pdb("all", ptr.To(intstr.FromInt32(3)), nil)}, single: true, safe: true, whyContains: "PodDisruptionBudget all allows 0"},
		{name: "three replicas, PDB maxUnavailable 0", obj: deploy(3, appsv1.DeploymentStrategy{}, nil),
			pdbs: []policyv1.PodDisruptionBudget{pdb("none", nil, &zero)}, single: true, safe: true},
		{name: "three replicas, PDB minAvailable 50% allows 1", obj: deploy(3, appsv1.DeploymentStrategy{}, nil),
			pdbs: []policyv1.PodDisruptionBudget{pdb("half", &pct50, nil)}, single: false, safe: true},
		{name: "scaled to zero", obj: deploy(0, appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, nil), single: false, safe: true},
		{name: "StatefulSet one replica", obj: sts(1), single: true, safe: false, rolloutReason: "delete-first"},
		{name: "StatefulSet two replicas", obj: sts(2), single: false, safe: false},
		{name: "StatefulSet two replicas, PDB minAvailable 2", obj: sts(2),
			pdbs: []policyv1.PodDisruptionBudget{pdb("both", ptr.To(intstr.FromInt32(2)), nil)}, single: true, safe: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Disruption
			switch o := tc.obj.(type) {
			case *appsv1.Deployment:
				d = AssessDisruption(o, tc.pdbs)
			case *appsv1.StatefulSet:
				d = AssessDisruption(o, tc.pdbs)
			}
			if d.SingleReplica != tc.single || d.RolloutSafe != tc.safe || d.Allowed != tc.allowed {
				t.Fatalf("got single=%v safe=%v allowed=%v (%q / %q)", d.SingleReplica, d.RolloutSafe, d.Allowed, d.Why, d.RolloutWhy)
			}
			if !strings.Contains(d.Why, tc.whyContains) || !strings.Contains(d.RolloutWhy, tc.rolloutReason) {
				t.Fatalf("why %q / rolloutWhy %q", d.Why, d.RolloutWhy)
			}
			if d.Unsafe() != (tc.single && !tc.allowed) {
				t.Fatalf("Unsafe() = %v", d.Unsafe())
			}
		})
	}
}

func TestAssessDisruptionDaemonSet(t *testing.T) {
	one, zero := intstr.FromInt32(1), intstr.FromInt32(0)
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "ns"},
		Spec:   appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 2}}
	pdbs := []policyv1.PodDisruptionBudget{pdb("ds", nil, &zero)}
	if d := AssessDisruption(ds, pdbs); !d.SingleReplica || d.RolloutSafe {
		t.Fatalf("default DaemonSet update (surge 0) with a zero-disruption PDB: %+v", d)
	}
	ds.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxSurge: &one, MaxUnavailable: &zero}}
	if d := AssessDisruption(ds, pdbs); !d.SingleReplica || !d.RolloutSafe {
		t.Fatalf("surging DaemonSet: %+v", d)
	}
	if d := AssessDisruption(ds, nil); d.SingleReplica {
		t.Fatalf("DaemonSet without PDB is not single-replica: %+v", d)
	}
}

func TestAllowedDisruptions(t *testing.T) {
	p := func(s string) *intstr.IntOrString { v := intstr.Parse(s); return &v }
	tests := []struct {
		min, max *intstr.IntOrString
		expected int32
		want     int32
	}{
		{min: p("1"), expected: 1, want: 0},
		{min: p("1"), expected: 3, want: 2},
		{min: p("100%"), expected: 3, want: 0},
		{min: p("50%"), expected: 3, want: 1}, // 1.5 -> 2 available
		{max: p("0"), expected: 3, want: 0},
		{max: p("25%"), expected: 3, want: 1}, // rounds up
		{max: p("5"), expected: 3, want: 3},
		{expected: 2, want: 2},
	}
	for _, tc := range tests {
		got := AllowedDisruptions(policyv1.PodDisruptionBudgetSpec{MinAvailable: tc.min, MaxUnavailable: tc.max}, tc.expected)
		if got != tc.want {
			t.Errorf("min=%v max=%v expected=%d: got %d, want %d", tc.min, tc.max, tc.expected, got, tc.want)
		}
	}
}

func stsPod(name, rev string, ready bool, readySince time.Time, cpu string) corev1.Pod {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{appsv1.ControllerRevisionHashLabelKey: rev}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st, LastTransitionTime: metav1.NewTime(readySince)}}},
	}
}

func TestPlanOnDelete(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	long, recent := now.Add(-time.Hour), now.Add(-5*time.Second)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns", Generation: 2},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3)),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, UpdateRevision: "new", CurrentRevision: "old"},
	}
	if !IsOnDelete(sts) {
		t.Fatal("IsOnDelete")
	}
	names := func(ps []*corev1.Pod) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	tests := []struct {
		name       string
		pods       []corev1.Pod
		maxDeletes int
		evict      []string
		outdated   int
		wait       string
		done       bool
	}{
		{name: "highest ordinal first", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m"), stsPod("db-10", "old", true, long, "200m")},
			maxDeletes: 1, evict: []string{"db-10"}, outdated: 3},
		{name: "two at a time", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m"), stsPod("db-2", "old", true, long, "200m")},
			maxDeletes: 2, evict: []string{"db-2", "db-1"}, outdated: 3},
		{name: "waits for the replaced pod to be Ready", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m"), stsPod("db-2", "new", false, recent, "100m")},
			maxDeletes: 1, outdated: 2, wait: "become Ready: db-2"},
		{name: "waits the settle time after Ready", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m"), stsPod("db-2", "new", true, recent, "100m")},
			maxDeletes: 1, outdated: 2, wait: "after Ready"},
		{name: "waits for a missing pod", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m")},
			maxDeletes: 1, outdated: 2, wait: "2 of 3 present"},
		{name: "continues after settle", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m"), stsPod("db-2", "new", true, long, "100m")},
			maxDeletes: 1, evict: []string{"db-1"}, outdated: 2},
		{name: "old revision with matching resources is not outdated", pods: []corev1.Pod{
			stsPod("db-0", "old", true, long, "100m"), stsPod("db-1", "new", true, long, "100m"), stsPod("db-2", "new", true, long, "100m")},
			maxDeletes: 1, done: true},
		{name: "done", pods: []corev1.Pod{
			stsPod("db-0", "new", true, long, "100m"), stsPod("db-1", "new", true, long, "100m"), stsPod("db-2", "new", true, recent, "100m")},
			maxDeletes: 1, done: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanOnDelete(sts, tc.pods, now, 30*time.Second, tc.maxDeletes)
			if got := names(plan.Evict); strings.Join(got, ",") != strings.Join(tc.evict, ",") {
				t.Fatalf("evict %v, want %v (waiting %q)", got, tc.evict, plan.Waiting)
			}
			if len(plan.Outdated) != tc.outdated || plan.Done() != tc.done || !strings.Contains(plan.Waiting, tc.wait) {
				t.Fatalf("outdated %v done %v waiting %q", plan.Outdated, plan.Done(), plan.Waiting)
			}
			if tc.wait == "" && plan.Waiting != "" {
				t.Fatalf("unexpected wait %q", plan.Waiting)
			}
		})
	}

	stale := sts.DeepCopy()
	stale.Status.ObservedGeneration = 1
	plan := PlanOnDelete(stale, []corev1.Pod{stsPod("db-0", "old", true, long, "200m")}, now, 30*time.Second, 1)
	if len(plan.Evict) != 0 || !strings.Contains(plan.Waiting, "observe the new template") {
		t.Fatalf("stale status: %+v", plan)
	}
	slow := sts.DeepCopy()
	slow.Spec.MinReadySeconds = 600
	plan = PlanOnDelete(slow, []corev1.Pod{stsPod("db-0", "old", true, long, "200m"), stsPod("db-1", "old", true, long, "200m"),
		stsPod("db-2", "new", true, now.Add(-time.Minute), "100m")}, now, 30*time.Second, 1)
	if len(plan.Evict) != 0 || !strings.Contains(plan.Waiting, "10m0s") {
		t.Fatalf("minReadySeconds must extend the settle time: %+v", plan)
	}
}
