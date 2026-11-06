package controller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// hpaApply is the HPA as Argo CD applies it from Git: cpu utilization target.
func hpaApply(name string, target int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler",
		"metadata": map[string]any{"name": name, "namespace": "default"},
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": name},
			"minReplicas":    int64(1), "maxReplicas": int64(4),
			"metrics": []any{map[string]any{"type": "Resource", "resource": map[string]any{
				"name": "cpu", "target": map[string]any{"type": "Utilization", "averageUtilization": target}}}},
		},
	}}
}

func getHPA(t *testing.T, name string) *autoscalingv2.HorizontalPodAutoscaler {
	t.Helper()
	var h autoscalingv2.HorizontalPodAutoscaler
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &h); err != nil {
		t.Fatal(err)
	}
	return &h
}

func wantHPATarget(t *testing.T, name string, want int32) func() error {
	return func() error {
		h := getHPA(t, name)
		if got := apply.HPATargetOf(h, "cpu"); got != want {
			return fmt.Errorf("HPA target %d, want %d", got, want)
		}
		return nil
	}
}

func wantBlockedMessage(name, substr string) func() error {
	return func() error {
		for _, e := range eventsFor(name) {
			if e.Type == hubclient.EventBlocked && e.Reason == v1alpha1.ReasonHPACoupled && strings.Contains(e.Message, substr) {
				return nil
			}
		}
		return fmt.Errorf("no blocked/HPACoupled event containing %q: %+v", substr, eventsFor(name))
	}
}

// TestHPACoordination: with spec.hpa.coordinate the requests and the HPA target change in
// one rollout, the target is kept live, released with the workload and restored by a rollback.
func TestHPACoordination(t *testing.T) {
	needEnv(t)
	const name, bounded, atMax = "hpa-coord", "hpa-bounds", "hpa-at-max"
	for _, n := range []string{name, bounded, atMax} {
		// in-place is configured; HPA workloads are changed with rollout anyway.
		u := deploymentApply(n, map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "in-place"}, "500m", "256Mi")
		u.SetLabels(map[string]string{"hpa-coord": "yes"})
		if err := argoApply(t, u, false); err != nil {
			t.Fatal(err)
		}
		if err := argoApply(t, hpaApply(n, 60), false); err != nil {
			t.Fatal(err)
		}
	}
	h := getHPA(t, atMax)
	h.Status.CurrentReplicas, h.Status.DesiredReplicas = 4, 4
	if err := k8s.Status().Update(testCtx, h); err != nil {
		t.Fatal(err)
	}
	// 500m -> 400m: target 60 x 500 / 400 = 75 %.
	setRec(recFor(name, "400m", "200Mi"))
	// 500m -> 250m: 120 %, outside 40-90 %.
	setRec(recFor(bounded, "250m", "200Mi"))
	setRec(recFor(atMax, "400m", "200Mi"))

	// Coordination off (global default): blocked as before.
	eventually(t, 20*time.Second, "blocked without coordination", wantPhase(name, v1alpha1.PhaseBlocked))
	eventually(t, 5*time.Second, "blocked event", wantBlockedMessage(name, "coordination is off"))

	p := &v1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "hpa-coordinate"},
		Spec: v1alpha1.RightsizingPolicySpec{
			Priority: 10,
			Selector: v1alpha1.PolicySelector{MatchLabels: map[string]string{"hpa-coord": "yes"}},
			HPA:      &v1alpha1.HPAPolicy{Coordinate: ptr.To(true)},
		},
	}
	if err := k8s.Create(testCtx, p); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k8s.Delete(testCtx, p) }()

	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	d := getDeploy(t, name)
	if cpuReq(d) != "400m" || memReq(d) != "200Mi" {
		t.Fatalf("template: cpu %s mem %s", cpuReq(d), memReq(d))
	}
	eventually(t, 5*time.Second, "HPA target 75", wantHPATarget(t, name, 75))
	h = getHPA(t, name)
	if !apply.HPAOwned(h) {
		t.Fatalf("kerf does not own the HPA target: %+v", h.ManagedFields)
	}
	for _, mf := range h.ManagedFields {
		if mf.Manager == argoManager && strings.Contains(mf.FieldsV1.GetRawString(), "f:metrics") {
			t.Fatalf("argo still owns spec.metrics: %s", mf.FieldsV1.GetRawString())
		}
	}
	rsr, _ := getRSR(name)
	if rsr.Spec.Strategy != v1alpha1.StrategyRollout || rsr.Spec.HPA == nil || *rsr.Spec.HPA != (v1alpha1.HPATarget{Name: name, Resource: "cpu", AverageUtilization: 75}) ||
		rsr.Status.PreviousHPATarget == nil || *rsr.Status.PreviousHPATarget != 60 {
		t.Fatalf("RSR spec %+v hpa %+v previous %v", rsr.Spec.Strategy, rsr.Spec.HPA, rsr.Status.PreviousHPATarget)
	}
	eventually(t, 5*time.Second, "applied event", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied))
	var applied hubclient.Event
	for _, e := range eventsFor(name) {
		if e.Type == hubclient.EventApplied {
			applied = e
		}
	}
	if applied.Strategy != "rollout" || applied.Details["hpaTargetFrom"] != "60" || applied.Details["hpaTargetTo"] != "75" ||
		!strings.Contains(applied.Message, "target 60% -> 75%") {
		t.Fatalf("applied event %+v", applied)
	}

	// The other two stay blocked, with their reason.
	eventually(t, 10*time.Second, "out of bounds", wantBlockedMessage(bounded, "outside 40-90%"))
	eventually(t, 10*time.Second, "at maxReplicas", wantBlockedMessage(atMax, apply.HPAProblemMaxReplicas))
	if d := getDeploy(t, bounded); cpuReq(d) != "500m" {
		t.Fatalf("bounded workload changed: %s", cpuReq(d))
	}

	// Argo CD (or a user) resets the target: drift repair restores it.
	if err := argoApply(t, hpaApply(name, 60), true); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "target re-asserted", wantHPATarget(t, name, 75))

	applied0, _ := getRSR(name) // as applied, for the rollback check below

	// Leaving auto-patch releases the HPA too; the values stay.
	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationMode: "audit"})
	eventually(t, 20*time.Second, "released", wantPhase(name, v1alpha1.PhaseReleased))
	eventually(t, 10*time.Second, "HPA released", func() error {
		if apply.HPAOwned(getHPA(t, name)) {
			return fmt.Errorf("HPA still owned")
		}
		return nil
	})
	if got := apply.HPATargetOf(getHPA(t, name), "cpu"); got != 75 {
		t.Fatalf("release changed the target: %d", got)
	}

	// A rollback of cpu restores the previous target (health watch). Released, the agent
	// no longer re-asserts the target, so the restore stays visible.
	rsr = applied0
	if err := apply.RestoreHPA(testCtx, k8s, k8s, rsr, []string{"memory"}); err != nil {
		t.Fatal(err)
	}
	if got := apply.HPATargetOf(getHPA(t, name), "cpu"); got != 75 {
		t.Fatalf("memory rollback changed the cpu target: %d", got)
	}
	if err := apply.RestoreHPA(testCtx, k8s, k8s, rsr, []string{"cpu"}); err != nil {
		t.Fatal(err)
	}
	if got := apply.HPATargetOf(getHPA(t, name), "cpu"); got != 60 {
		t.Fatalf("rollback target %d, want 60", got)
	}
}
