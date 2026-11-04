package webhook

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

func rl(kv ...string) corev1.ResourceList {
	if len(kv) == 0 {
		return nil
	}
	out := corev1.ResourceList{}
	for i := 0; i < len(kv); i += 2 {
		out[corev1.ResourceName(kv[i])] = resource.MustParse(kv[i+1])
	}
	return out
}

func rsr(phase v1alpha1.Phase, strategy v1alpha1.Strategy, gen, observed int64) *v1alpha1.RightsizingRecommendation {
	r := &v1alpha1.RightsizingRecommendation{ObjectMeta: metav1.ObjectMeta{Generation: gen}}
	r.Spec.Strategy = strategy
	r.Status.Phase = phase
	r.Status.ObservedGeneration = observed
	return r
}

func TestEligible(t *testing.T) {
	tests := []struct {
		name string
		r    *v1alpha1.RightsizingRecommendation
		want string
	}{
		{"applied in-place", rsr(v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, 3, 3), ""},
		{"applied rollout", rsr(v1alpha1.PhaseApplied, v1alpha1.StrategyRollout, 1, 1), ReasonNotInPlace},
		{"applied deferred", rsr(v1alpha1.PhaseApplied, v1alpha1.StrategyDeferred, 1, 1), ReasonNotInPlace},
		{"pending", rsr(v1alpha1.PhasePending, v1alpha1.StrategyInPlace, 1, 1), ReasonNotApplied},
		{"applying", rsr(v1alpha1.PhaseApplying, v1alpha1.StrategyInPlace, 1, 1), ReasonNotApplied},
		{"blocked", rsr(v1alpha1.PhaseBlocked, v1alpha1.StrategyInPlace, 1, 1), ReasonNotApplied},
		{"rolled back", rsr(v1alpha1.PhaseRolledBack, v1alpha1.StrategyInPlace, 1, 1), ReasonNotApplied},
		{"released", rsr(v1alpha1.PhaseReleased, v1alpha1.StrategyInPlace, 1, 1), ReasonNotApplied},
		{"no status yet", rsr("", v1alpha1.StrategyInPlace, 1, 0), ReasonNotApplied},
		{"new spec, status not updated", rsr(v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, 4, 3), ReasonStaleStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Eligible(tt.r); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func container(name string, req, lim corev1.ResourceList) corev1.Container {
	return corev1.Container{Name: name, Image: "x", Resources: corev1.ResourceRequirements{Requests: req, Limits: lim}}
}

func crec(name string, req, lim corev1.ResourceList, remove ...corev1.ResourceName) v1alpha1.ContainerRecommendation {
	return v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: name, Requests: req, Limits: lim}, RemoveLimits: remove}
}

func TestMutate(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	tests := []struct {
		name        string
		spec        corev1.PodSpec
		rec         []v1alpha1.ContainerRecommendation
		wantSpec    corev1.PodSpec
		wantInj     []v1alpha1.ContainerResources
		wantChanged bool
		wantSkipped int
	}{
		{
			name:        "cpu and memory requests and limits",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1", "memory", "1Gi"), rl("cpu", "2", "memory", "2Gi"))}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "250m", "memory", "300Mi"), rl("cpu", "500m", "memory", "300Mi"))},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "250m", "memory", "300Mi"), rl("cpu", "500m", "memory", "300Mi"))}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Requests: rl("cpu", "250m", "memory", "300Mi"), Limits: rl("cpu", "500m", "memory", "300Mi")}},
			wantChanged: true,
		},
		{
			name: "cpu only (memory not in auto-patch): memory and other keys untouched",
			spec: corev1.PodSpec{Containers: []corev1.Container{container("app",
				rl("cpu", "1", "memory", "1Gi", "ephemeral-storage", "1Gi"), rl("memory", "1Gi"))}},
			rec: []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "200m"), nil)},
			wantSpec: corev1.PodSpec{Containers: []corev1.Container{container("app",
				rl("cpu", "200m", "memory", "1Gi", "ephemeral-storage", "1Gi"), rl("memory", "1Gi"))}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Requests: rl("cpu", "200m")}},
			wantChanged: true,
		},
		{
			name:        "container without resources gets them",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", nil, nil)}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "100m"), rl("memory", "128Mi"))},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "100m"), rl("memory", "128Mi"))}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Requests: rl("cpu", "100m"), Limits: rl("memory", "128Mi")}},
			wantChanged: true,
		},
		{
			name:        "removeLimits drops the cpu limit",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), rl("cpu", "1", "memory", "1Gi"))}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "300m"), nil, corev1.ResourceCPU)},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "300m"), rl("memory", "1Gi"))}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Requests: rl("cpu", "300m")}},
			wantChanged: true,
		},
		{
			name:        "removing the only limit leaves limits nil",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), rl("cpu", "1"))}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", nil, nil, corev1.ResourceCPU)},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), nil)}},
			wantChanged: true,
		},
		{
			name:     "removeLimits of a limit that is not set is no change",
			spec:     corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), nil)}},
			rec:      []v1alpha1.ContainerRecommendation{crec("app", nil, nil, corev1.ResourceCPU)},
			wantSpec: corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), nil)}},
		},
		{
			name:        "explicit limit wins over removeLimits of the same resource",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), rl("cpu", "1"))}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", nil, rl("cpu", "2"), corev1.ResourceCPU)},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), rl("cpu", "2"))}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Limits: rl("cpu", "2")}},
			wantChanged: true,
		},
		{
			name:     "values already equal (different notation): recorded, no change",
			spec:     corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "0.5"), rl("memory", "1024Mi"))}},
			rec:      []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "500m"), rl("memory", "1Gi"))},
			wantSpec: corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "500m"), rl("memory", "1Gi"))}},
			wantInj:  []v1alpha1.ContainerResources{{Name: "app", Requests: rl("cpu", "500m"), Limits: rl("memory", "1Gi")}},
		},
		{
			name:        "request above the template limit: that resource is left alone",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "500m", "memory", "1Gi"), rl("cpu", "1", "memory", "1Gi"))}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "2", "memory", "512Mi"), nil)},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "500m", "memory", "512Mi"), rl("cpu", "1", "memory", "1Gi"))}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Requests: rl("memory", "512Mi")}},
			wantChanged: true,
			wantSkipped: 1,
		},
		{
			name:        "recommended limit below the template request: left alone",
			spec:        corev1.PodSpec{Containers: []corev1.Container{container("app", rl("memory", "1Gi"), rl("memory", "2Gi"))}},
			rec:         []v1alpha1.ContainerRecommendation{crec("app", nil, rl("memory", "512Mi"))},
			wantSpec:    corev1.PodSpec{Containers: []corev1.Container{container("app", rl("memory", "1Gi"), rl("memory", "2Gi"))}},
			wantSkipped: 1,
		},
		{
			name: "init and native sidecar containers, container missing from pod",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{container("init", rl("cpu", "1"), nil), {Name: "proxy", RestartPolicy: &always,
					Resources: corev1.ResourceRequirements{Requests: rl("cpu", "1")}}},
				Containers: []corev1.Container{container("app", rl("cpu", "1"), nil)},
			},
			rec: []v1alpha1.ContainerRecommendation{
				crec("proxy", rl("cpu", "50m"), nil), crec("app", rl("cpu", "400m"), nil),
				crec("init", rl("cpu", "100m"), nil), crec("gone", rl("cpu", "1"), nil),
			},
			wantSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{container("init", rl("cpu", "100m"), nil), {Name: "proxy", RestartPolicy: &always,
					Resources: corev1.ResourceRequirements{Requests: rl("cpu", "50m")}}},
				Containers: []corev1.Container{container("app", rl("cpu", "400m"), nil)},
			},
			wantInj: []v1alpha1.ContainerResources{
				{Name: "app", Requests: rl("cpu", "400m")}, {Name: "init", Requests: rl("cpu", "100m")}, {Name: "proxy", Requests: rl("cpu", "50m")},
			},
			wantChanged: true,
			wantSkipped: 1,
		},
		{
			name: "only the named container changes",
			spec: corev1.PodSpec{Containers: []corev1.Container{container("app", rl("cpu", "1"), nil), container("log", rl("cpu", "100m"), nil)}},
			rec:  []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "300m"), nil)},
			wantSpec: corev1.PodSpec{Containers: []corev1.Container{
				container("app", rl("cpu", "300m"), nil), container("log", rl("cpu", "100m"), nil)}},
			wantInj:     []v1alpha1.ContainerResources{{Name: "app", Requests: rl("cpu", "300m")}},
			wantChanged: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: *tt.spec.DeepCopy()}
			m := Mutate(pod, tt.rec)
			if m.Changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", m.Changed, tt.wantChanged)
			}
			if len(m.Skipped) != tt.wantSkipped {
				t.Errorf("skipped = %v, want %d entries", m.Skipped, tt.wantSkipped)
			}
			if !equalResources(m.Injected, tt.wantInj) {
				t.Errorf("injected = %+v\nwant       %+v", m.Injected, tt.wantInj)
			}
			if !equalSpec(pod.Spec, tt.wantSpec) {
				t.Errorf("spec = %+v\nwant   %+v", pod.Spec, tt.wantSpec)
			}
		})
	}
}

// equalResources compares quantities by value (Cmp), not by notation.
func equalRL(a, b corev1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || v.Cmp(w) != 0 {
			return false
		}
	}
	return true
}

func equalResources(a, b []v1alpha1.ContainerResources) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !equalRL(a[i].Requests, b[i].Requests) || !equalRL(a[i].Limits, b[i].Limits) {
			return false
		}
	}
	return true
}

func equalSpec(a, b corev1.PodSpec) bool {
	cs := func(x, y []corev1.Container) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i].Name != y[i].Name || !equalRL(x[i].Resources.Requests, y[i].Resources.Requests) ||
				!equalRL(x[i].Resources.Limits, y[i].Resources.Limits) ||
				(x[i].Resources.Limits == nil) != (y[i].Resources.Limits == nil) ||
				!reflect.DeepEqual(x[i].RestartPolicy, y[i].RestartPolicy) {
				return false
			}
		}
		return true
	}
	return cs(a.Containers, b.Containers) && cs(a.InitContainers, b.InitContainers)
}
