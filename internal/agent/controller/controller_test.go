package controller

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/apply"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

const argoManager = "argocd-controller"

// deploymentApply is the "Git" version of a Deployment, applied by Argo CD's manager.
func deploymentApply(name string, ann map[string]string, cpu, mem string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "default"},
		"spec": map[string]any{
			"replicas": int64(1),
			"selector": map[string]any{"matchLabels": map[string]any{"app": name}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": name}},
				"spec": map[string]any{"containers": []any{map[string]any{
					"name": "app", "image": "nginx:1.27",
					"resizePolicy": []any{
						map[string]any{"resourceName": "cpu", "restartPolicy": "NotRequired"},
						map[string]any{"resourceName": "memory", "restartPolicy": "NotRequired"},
					},
					"resources": map[string]any{
						"requests": map[string]any{"cpu": cpu, "memory": mem},
						"limits":   map[string]any{"cpu": "1", "memory": "512Mi"},
					},
				}}},
			},
		},
	}}
	if len(ann) > 0 {
		a := map[string]any{}
		for k, v := range ann {
			a[k] = v
		}
		u.Object["metadata"].(map[string]any)["annotations"] = a
	}
	return u
}

func argoApply(t *testing.T, u *unstructured.Unstructured, force bool) error {
	t.Helper()
	opts := []client.ApplyOption{client.FieldOwner(argoManager)}
	if force {
		opts = append(opts, client.ForceOwnership)
	}
	return k8s.Apply(testCtx, client.ApplyConfigurationFromUnstructured(u.DeepCopy()), opts...)
}

func recFor(name string, cpu, mem string) hubclient.WorkloadRecommendation {
	return hubclient.WorkloadRecommendation{
		Workload:   hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: name},
		Confidence: 0.95,
		Settings:   hubclient.ValueSettings{Profile: "production", CPULimits: "keep", MemoryLimitEqualsRequest: ptr.To(true)},
		Containers: []hubclient.ContainerRecommendation{{
			Name:        "app",
			Current:     hubclient.Resources{Requests: &hubclient.ResourceValues{CPU: "500m", Memory: "256Mi"}},
			Recommended: hubclient.Resources{Requests: &hubclient.ResourceValues{CPU: cpu, Memory: mem}},
		}},
	}
}

func getRSR(name string) (*v1alpha1.RightsizingRecommendation, error) {
	var list v1alpha1.RightsizingRecommendationList
	if err := k8s.List(testCtx, &list, client.InNamespace("default")); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.TargetRef.Name == name && list.Items[i].Spec.TargetRef.Kind == v1alpha1.KindDeployment {
			return &list.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no RSR for %s", name)
}

func wantPhase(name string, phase v1alpha1.Phase) func() error {
	return func() error {
		r, err := getRSR(name)
		if err != nil {
			return err
		}
		if r.Status.Phase != phase {
			return fmt.Errorf("phase %q, want %q (conditions %+v)", r.Status.Phase, phase, r.Status.Conditions)
		}
		return nil
	}
}

func eventsFor(name string) []hubclient.Event {
	var out []hubclient.Event
	for _, e := range stub.Events(testCluster) {
		if e.Workload.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func wantEvent(name, typ, reason string) func() error {
	return func() error {
		for _, e := range eventsFor(name) {
			if e.Type == typ && e.Reason == reason {
				return nil
			}
		}
		return fmt.Errorf("no %s/%s event for %s; have %v", typ, reason, name, eventsFor(name))
	}
}

func countEvents(name, typ, reason string) int {
	n := 0
	for _, e := range eventsFor(name) {
		if e.Type == typ && e.Reason == reason {
			n++
		}
	}
	return n
}

func getDeploy(t *testing.T, name string) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func managerOwns(d *appsv1.Deployment, manager string) (bool, string) {
	for _, mf := range d.ManagedFields {
		if mf.Manager == manager && mf.FieldsV1 != nil && strings.Contains(mf.FieldsV1.GetRawString(), `"f:resources"`) {
			return true, string(mf.Operation)
		}
	}
	return false, ""
}

func annotate(t *testing.T, kind client.Object, name string, ann map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
	kind.SetName(name)
	kind.SetNamespace("default")
	if err := k8s.Patch(testCtx, kind, client.RawPatch(types.MergePatchType, b), client.FieldOwner("kubectl-annotate")); err != nil {
		t.Fatal(err)
	}
}

func cpuReq(d *appsv1.Deployment) string {
	q := d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	return q.String()
}

func memReq(d *appsv1.Deployment) string {
	q := d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
	return q.String()
}

// TestRolloutFieldManagerAndRelease: rollout takes the fields from Argo CD's manager with
// force, release gives them up without deleting them, and Argo CD can then restore Git values
// without a conflict (D-006).
func TestRolloutFieldManagerAndRelease(t *testing.T) {
	needEnv(t)
	const name = "web-rollout"
	git := deploymentApply(name, map[string]string{
		v1alpha1.AnnotationMode:     "auto-patch",
		v1alpha1.AnnotationStrategy: "rollout",
	}, "500m", "256Mi")
	if err := argoApply(t, git, false); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(name, "250m", "200Mi"))

	eventually(t, 20*time.Second, "rollout applied", wantPhase(name, v1alpha1.PhaseApplied))
	d := getDeploy(t, name)
	if cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("template not patched: cpu %s mem %s", cpuReq(d), memReq(d))
	}
	if ok, op := managerOwns(d, v1alpha1.FieldManager); !ok || op != string(metav1.ManagedFieldsOperationApply) {
		t.Fatalf("resource-optimizer does not own resources via Apply (op %q)", op)
	}
	owned := apply.OwnedResources(d)
	if !owned[corev1.ResourceCPU] || !owned[corev1.ResourceMemory] {
		t.Fatalf("owned resources = %v", owned)
	}
	for _, mf := range d.ManagedFields {
		if mf.Manager == argoManager && strings.Contains(mf.FieldsV1.GetRawString(), `"f:requests":{"f:cpu"`) {
			t.Fatalf("argo still owns cpu request after forced apply: %s", mf.FieldsV1.GetRawString())
		}
	}
	if d.Annotations[v1alpha1.AnnotationManaged] != "true" || d.Annotations[v1alpha1.AnnotationPreviousResources] == "" {
		t.Fatalf("annotations: %v", d.Annotations)
	}
	var prev []v1alpha1.ContainerResources
	if err := json.Unmarshal([]byte(d.Annotations[v1alpha1.AnnotationPreviousResources]), &prev); err != nil || prev[0].Requests.Cpu().String() != "500m" {
		t.Fatalf("previous-resources = %s (%v)", d.Annotations[v1alpha1.AnnotationPreviousResources], err)
	}
	rsr, _ := getRSR(name)
	if rsr.Spec.Strategy != v1alpha1.StrategyRollout || rsr.Status.Previous[0].Requests.Cpu().String() != "500m" ||
		rsr.Status.Effective.CPUModeSource != v1alpha1.ModeSourceAnnotation {
		t.Fatalf("RSR: %+v", rsr)
	}
	eventually(t, 5*time.Second, "applied event", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied))
	ev := eventsFor(name)[0]
	if ev.Strategy != "rollout" || len(ev.Containers) != 1 || ev.Containers[0].To.Requests.CPU != "250m" || ev.Containers[0].From.Requests.CPU != "500m" {
		t.Fatalf("applied event: %+v", ev)
	}

	// Argo CD re-syncs (no force): our fields are untouched, no conflict on fields we don't own.
	if err := argoApply(t, git, false); err == nil {
		t.Log("argo sync without force succeeded (fields owned by us are ignored by Argo CD's diff, not by SSA)")
	}

	// Leave auto-patch: ownership is released, values stay, phase Released.
	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationMode: "audit"})
	eventually(t, 20*time.Second, "released", wantPhase(name, v1alpha1.PhaseReleased))
	d = getDeploy(t, name)
	if ok, _ := managerOwns(d, v1alpha1.FieldManager); ok {
		t.Fatal("resource-optimizer still owns resource fields after release")
	}
	for _, mf := range d.ManagedFields {
		if mf.Manager == v1alpha1.FieldManager {
			t.Fatalf("managedFields still has a %s entry: %+v", v1alpha1.FieldManager, mf)
		}
	}
	if cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("release must keep fields: cpu %s mem %s", cpuReq(d), memReq(d))
	}
	if _, ok := d.Annotations[v1alpha1.AnnotationManaged]; ok {
		t.Fatalf("managed annotation not removed: %v", d.Annotations)
	}
	eventually(t, 5*time.Second, "released event", wantEvent(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged))
	time.Sleep(time.Second)
	if n := countEvents(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged); n != 1 {
		t.Fatalf("released must be emitted exactly once, got %d", n)
	}

	// Argo CD restores Git values without force: no conflict because nobody else owns them.
	gitAudit := deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "audit", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, gitAudit, false); err != nil {
		t.Fatalf("argo restore conflicted after release: %v", err)
	}
	d = getDeploy(t, name)
	if cpuReq(d) != "500m" || memReq(d) != "256Mi" {
		t.Fatalf("argo restore: cpu %s mem %s", cpuReq(d), memReq(d))
	}
	// The agent must not fight the restore.
	time.Sleep(time.Second)
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatalf("agent re-patched a released workload: %s", cpuReq(d))
	}
}

// TestPartialRelease: memory leaves auto-patch, cpu stays owned.
func TestPartialRelease(t *testing.T) {
	needEnv(t)
	const name = "web-partial"
	if err := argoApply(t, deploymentApply(name, map[string]string{
		v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout",
	}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))

	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationMemoryMode: "audit"})
	eventually(t, 20*time.Second, "memory released", func() error {
		d := getDeploy(t, name)
		owned := apply.OwnedResources(d)
		if !owned[corev1.ResourceCPU] || owned[corev1.ResourceMemory] {
			return fmt.Errorf("owned = %v", owned)
		}
		if cpuReq(d) != "250m" || memReq(d) != "200Mi" {
			return fmt.Errorf("values changed: %s %s", cpuReq(d), memReq(d))
		}
		return nil
	})
	eventually(t, 5*time.Second, "released event", wantEvent(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged))
	if err := wantPhase(name, v1alpha1.PhaseApplied)(); err != nil {
		t.Fatal(err)
	}
}

// pod creates a pod owned by a ReplicaSet owned by the Deployment, as the controllers would.
func makePod(t *testing.T, d *appsv1.Deployment) *corev1.Pod {
	t.Helper()
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: d.Name + "-rs", Namespace: "default", Labels: d.Spec.Template.Labels,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: ptr.To(true)}}},
		Spec: appsv1.ReplicaSetSpec{Selector: d.Spec.Selector, Template: d.Spec.Template},
	}
	if err := k8s.Create(testCtx, rs); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: d.Name + "-pod", Namespace: "default", Labels: d.Spec.Template.Labels,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr.To(true)}}},
		Spec: *d.Spec.Template.Spec.DeepCopy(),
	}
	pod.Spec.NodeName = "node-1"
	if err := k8s.Create(testCtx, pod); err != nil {
		t.Fatal(err)
	}
	setPodStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodRunning
		res := p.Spec.Containers[0].Resources.DeepCopy()
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Image: "nginx:1.27", ImageID: "x", Ready: true,
			Started: ptr.To(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}, Resources: res}}
	})
	return pod
}

func setPodStatus(t *testing.T, name string, mut func(*corev1.Pod)) {
	t.Helper()
	eventually(t, 5*time.Second, "update pod status", func() error {
		var p corev1.Pod
		if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &p); err != nil {
			return err
		}
		mut(&p)
		return k8s.Status().Update(testCtx, &p)
	})
}

func getPod(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	var p corev1.Pod
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

// TestInPlace: pods are resized through pods/resize, the template is never touched, and the
// phase becomes Applied when the kubelet reports the new values.
func TestInPlace(t *testing.T) {
	needEnv(t)
	const name = "api-inplace"
	if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationCPUMode: "auto-patch"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	pod := makePod(t, getDeploy(t, name))
	setRec(recFor(name, "250m", "200Mi"))

	eventually(t, 20*time.Second, "pod resized", func() error {
		p := getPod(t, pod.Name)
		if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
	p := getPod(t, pod.Name)
	if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]; got.String() != "256Mi" {
		t.Fatalf("memory is audit and must not change: %s", got.String())
	}
	if p.Annotations[v1alpha1.AnnotationPreviousResources] == "" || p.Annotations[v1alpha1.AnnotationRecommendationID] == "" {
		t.Fatalf("pod annotations: %v", p.Annotations)
	}
	if err := wantPhase(name, v1alpha1.PhaseApplying)(); err != nil {
		t.Fatal(err)
	}

	// The kubelet actuates the resize.
	setPodStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].Resources = p.Spec.Containers[0].Resources.DeepCopy()
	})
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	d := getDeploy(t, name)
	if cpuReq(d) != "500m" {
		t.Fatalf("in-place must not touch the template: %s", cpuReq(d))
	}
	if ok, _ := managerOwns(d, v1alpha1.FieldManager); ok {
		t.Fatal("in-place must not own template fields")
	}
	eventually(t, 5*time.Second, "applied event", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied))
	for _, e := range eventsFor(name) {
		if e.Type == hubclient.EventApplied && (e.Strategy != "in-place" || !slices.Equal(e.Resources, []string{"cpu"})) {
			t.Fatalf("applied event: %+v", e)
		}
	}
	rsr, _ := getRSR(name)
	if rsr.Spec.Strategy != v1alpha1.StrategyInPlace || rsr.Status.AppliedRecommendationID == "" {
		t.Fatalf("RSR %+v", rsr)
	}

	// A new pod with template values (no webhook here) is resized to the applied values.
	if err := k8s.Delete(testCtx, getPod(t, pod.Name), client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	var rs appsv1.ReplicaSet
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name + "-rs"}, &rs); err != nil {
		t.Fatal(err)
	}
	fresh := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-pod2", Namespace: "default", Labels: d.Spec.Template.Labels,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr.To(true)}}},
		Spec: *d.Spec.Template.Spec.DeepCopy(),
	}
	fresh.Spec.NodeName = "node-1"
	if err := k8s.Create(testCtx, fresh); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "new pod resized to the applied values", func() error {
		p := getPod(t, fresh.Name)
		if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
	if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 1 {
		t.Fatalf("drift repair must not emit events; applied events: %d", n)
	}
}

// TestInPlaceFallback: an Infeasible resize switches the workload to rollout.
func TestInPlaceFallback(t *testing.T) {
	needEnv(t)
	const name = "batch-fallback"
	if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	pod := makePod(t, getDeploy(t, name))
	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 20*time.Second, "pod resized", func() error {
		p := getPod(t, pod.Name)
		if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
	setPodStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodResizePending, Status: corev1.ConditionTrue,
			Reason: corev1.PodReasonInfeasible, Message: "Node didn't have enough capacity", LastTransitionTime: metav1.Now()})
	})
	eventually(t, 20*time.Second, "fell back to rollout", func() error {
		r, err := getRSR(name)
		if err != nil {
			return err
		}
		if r.Spec.Strategy != v1alpha1.StrategyRollout || r.Status.Phase != v1alpha1.PhaseApplied {
			return fmt.Errorf("strategy %s phase %s", r.Spec.Strategy, r.Status.Phase)
		}
		return nil
	})
	d := getDeploy(t, name)
	if cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("template after fallback: %s %s", cpuReq(d), memReq(d))
	}
	if ok, _ := managerOwns(d, v1alpha1.FieldManager); !ok {
		t.Fatal("rollout after fallback must own the template fields")
	}
	eventually(t, 5*time.Second, "fallback event", wantEvent(name, hubclient.EventFallback, v1alpha1.ReasonResizeInfeasible))
	eventually(t, 5*time.Second, "applied event", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied))
	for _, e := range eventsFor(name) {
		if e.Type == hubclient.EventFallback && e.Strategy != "rollout" {
			t.Fatalf("fallback event strategy %q", e.Strategy)
		}
	}
	// Sticky: the next reconciles keep rollout and do not resize again.
	time.Sleep(time.Second)
	if r, _ := getRSR(name); r.Spec.Strategy != v1alpha1.StrategyRollout || r.Status.Phase != v1alpha1.PhaseApplied {
		t.Fatalf("fallback not sticky: %s %s", r.Spec.Strategy, r.Status.Phase)
	}
}

// TestBlockedAndSkippedOnce: guardrails in the cluster, with events emitted once.
func TestBlockedAndSkippedOnce(t *testing.T) {
	needEnv(t)
	// HPA-coupled.
	const hpaName = "hpa-coupled"
	if err := argoApply(t, deploymentApply(hpaName, map[string]string{v1alpha1.AnnotationMode: "auto-patch"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: hpaName, Namespace: "default"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: hpaName},
			MinReplicas:    ptr.To[int32](1), MaxReplicas: 3,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
				Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To[int32](70)}}}},
		},
	}
	if err := k8s.Create(testCtx, hpa); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(hpaName, "250m", "200Mi"))
	eventually(t, 20*time.Second, "blocked", wantPhase(hpaName, v1alpha1.PhaseBlocked))
	eventually(t, 5*time.Second, "blocked event", wantEvent(hpaName, hubclient.EventBlocked, v1alpha1.ReasonHPACoupled))

	// Audit (global default).
	const auditName = "audit-only"
	if err := argoApply(t, deploymentApply(auditName, nil, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(auditName, "250m", "200Mi"))
	eventually(t, 20*time.Second, "pending", wantPhase(auditName, v1alpha1.PhasePending))
	eventually(t, 5*time.Second, "skipped event", wantEvent(auditName, hubclient.EventSkipped, v1alpha1.ReasonModeAudit))
	if d := getDeploy(t, auditName); cpuReq(d) != "500m" {
		t.Fatal("audit workload changed")
	}
	rsr, _ := getRSR(auditName)
	if len(rsr.Spec.Containers) != 1 || rsr.Spec.Containers[0].Requests.Cpu().String() != "250m" {
		t.Fatalf("audit RSR should show the proposed values: %+v", rsr.Spec.Containers)
	}

	// Several pulls later, still one event each.
	time.Sleep(2 * time.Second)
	if n := countEvents(hpaName, hubclient.EventBlocked, v1alpha1.ReasonHPACoupled); n != 1 {
		t.Fatalf("blocked events: %d", n)
	}
	if n := countEvents(auditName, hubclient.EventSkipped, v1alpha1.ReasonModeAudit); n != 1 {
		t.Fatalf("skipped events: %d", n)
	}

	// Status reported to the hub.
	eventually(t, 5*time.Second, "hub status", func() error {
		st, ok := stub.Statuses(testCluster)["default/Deployment/"+auditName]
		if !ok {
			return fmt.Errorf("no status")
		}
		if st.Modes.CPU != "audit" || st.Modes.CPUSource != "global-default" || st.Phase != "Pending" || st.Settings == nil ||
			st.Settings.Profile != "production" || len(st.Live) != 1 || st.Live[0].Requests.CPU != "500m" {
			return fmt.Errorf("status %+v", st)
		}
		return nil
	})
}

// TestHubUnreachable: no hub, no new actions; nothing applied is reverted.
func TestHubUnreachable(t *testing.T) {
	needEnv(t)
	const name = "offline"
	if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	stub.SetDown(true)
	defer stub.SetDown(false)
	setRec(recFor(name, "250m", "200Mi"))
	time.Sleep(3 * time.Second) // > hub-stale-after (2s)
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatal("applied while the hub was down")
	}
	stub.SetDown(false)
	eventually(t, 20*time.Second, "applied after hub is back", wantPhase(name, v1alpha1.PhaseApplied))
	stub.SetDown(true)
	time.Sleep(3 * time.Second)
	if d := getDeploy(t, name); cpuReq(d) != "250m" {
		t.Fatalf("change reverted while offline: %s", cpuReq(d))
	}
	if err := wantPhase(name, v1alpha1.PhaseApplied)(); err != nil {
		t.Fatal(err)
	}
	stub.SetDown(false)
	eventually(t, 10*time.Second, "buffered events delivered", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied))
}

// TestPolicyCRD: a RightsizingPolicy turns auto-patch on; the RSR exposes the effective policy.
func TestPolicyCRD(t *testing.T) {
	needEnv(t)
	const name = "by-policy"
	u := deploymentApply(name, nil, "500m", "256Mi")
	u.SetLabels(map[string]string{"rightsize": "yes"})
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	p := &v1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "rollout-labelled"},
		Spec: v1alpha1.RightsizingPolicySpec{
			Priority: 10,
			Selector: v1alpha1.PolicySelector{MatchLabels: map[string]string{"rightsize": "yes"}},
			CPU:      &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch},
			Apply:    &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyRollout},
			Guardrails: &v1alpha1.Guardrails{
				MaxChangePercent: ptr.To[int32](20),
				MinCPU:           ptr.To(resource.MustParse("10m")),
			},
		},
	}
	if err := k8s.Create(testCtx, p); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k8s.Delete(testCtx, p) }()
	setRec(recFor(name, "100m", "200Mi"))
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	d := getDeploy(t, name)
	if cpuReq(d) != "400m" || memReq(d) != "256Mi" {
		t.Fatalf("expected cpu clamped to 400m (20%% step) and memory untouched: %s %s", cpuReq(d), memReq(d))
	}
	rsr, _ := getRSR(name)
	e := rsr.Status.Effective
	if e.Policy != "rollout-labelled" || e.CPUModeSource != v1alpha1.ModeSourcePolicy || e.MemoryModeSource != v1alpha1.ModeSourceGlobalDefault || e.Strategy != v1alpha1.StrategyRollout {
		t.Fatalf("effective %+v", e)
	}
}

// TestDedupD017: skipped/blocked are emitted once per (workload, type, reason) until the reason or
// the proposed values change; a new recommendation id alone does not re-emit.
func TestDedupD017(t *testing.T) {
	needEnv(t)
	const name = "dedup"
	if err := argoApply(t, deploymentApply(name, nil, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	r := recFor(name, "250m", "200Mi")
	r.ID = "11111111-1111-4111-a111-111111111111"
	setRec(r)
	eventually(t, 20*time.Second, "skipped", wantEvent(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit))

	r.ID = "22222222-2222-4222-a222-222222222222" // new hub run, same values
	setRec(r)
	eventually(t, 10*time.Second, "new id seen", func() error {
		rsr, err := getRSR(name)
		if err != nil || rsr.Spec.Source.RecommendationID != r.ID {
			return fmt.Errorf("rsr source not updated yet: %w", err)
		}
		return nil
	})
	time.Sleep(time.Second)
	if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit); n != 1 {
		t.Fatalf("same values under a new id re-emitted: %d events", n)
	}

	r = recFor(name, "255m", "200Mi") // hub jitter within the tolerance
	r.ID = "55555555-5555-4555-a555-555555555555"
	setRec(r)
	eventually(t, 10*time.Second, "jitter id seen", func() error {
		rsr, err := getRSR(name)
		if err != nil || rsr.Spec.Source.RecommendationID != r.ID {
			return fmt.Errorf("rsr source not updated yet: %w", err)
		}
		return nil
	})
	time.Sleep(time.Second)
	if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit); n != 1 {
		t.Fatalf("jitter within the tolerance re-emitted: %d events", n)
	}

	r = recFor(name, "300m", "200Mi") // values change
	r.ID = "33333333-3333-4333-a333-333333333333"
	setRec(r)
	eventually(t, 20*time.Second, "re-emitted on new values", func() error {
		if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit); n != 2 {
			return fmt.Errorf("%d events", n)
		}
		return nil
	})
}

// TestRolledBackStays: after a rollback (WO-08) the phase stays RolledBack while the next
// recommendation is blocked by the rollback cooldown.
func TestRolledBackStays(t *testing.T) {
	needEnv(t)
	const name = "rolled-back"
	if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	rsr, _ := getRSR(name)
	now := metav1.Now()
	rsr.Status.Phase = v1alpha1.PhaseRolledBack
	rsr.Status.RolledBackAt = &now
	rsr.Status.Conditions = append(rsr.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionHealthWatch, Status: metav1.ConditionFalse,
		Reason: v1alpha1.ReasonOOMKills, Message: "test", LastTransitionTime: now})
	if err := k8s.Status().Update(testCtx, rsr); err != nil {
		t.Fatal(err)
	}
	r := recFor(name, "240m", "150Mi")
	r.ID = "44444444-4444-4444-a444-444444444444"
	setRec(r)
	eventually(t, 20*time.Second, "blocked by cooldown", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonCooldown))
	if err := wantPhase(name, v1alpha1.PhaseRolledBack)(); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseInPlaceOnce: leaving auto-patch after an in-place change emits one released and one
// skipped event (the proposed values used to differ between the reconcile that released and the
// next one, so skipped came twice).
func TestReleaseInPlaceOnce(t *testing.T) {
	needEnv(t)
	const name = "release-inplace"
	if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationCPUMode: "auto-patch"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	pod := makePod(t, getDeploy(t, name))
	setRec(recFor(name, "100m", "200Mi")) // clamped to 250m (50% step)
	eventually(t, 20*time.Second, "pod resized", func() error {
		p := getPod(t, pod.Name)
		if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
	setPodStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].Resources = p.Spec.Containers[0].Resources.DeepCopy()
	})
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationCPUMode: nil})
	eventually(t, 20*time.Second, "released", wantPhase(name, v1alpha1.PhaseReleased))
	time.Sleep(2 * time.Second)
	if n := countEvents(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged); n != 1 {
		t.Fatalf("released events: %d", n)
	}
	if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit); n != 1 {
		t.Fatalf("skipped events after release: %d", n)
	}
	// An agent restart forgets the in-memory slots; the RSR status must stop the repeat.
	testReconciler.mu.Lock()
	testReconciler.last = nil
	testReconciler.mu.Unlock()
	annotate(t, &appsv1.Deployment{}, name, map[string]any{"test/poke": "1"}) // forces a reconcile
	time.Sleep(2 * time.Second)
	if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit); n != 1 {
		t.Fatalf("skipped re-emitted after a simulated restart: %d", n)
	}
}

// TestNonUUIDRecommendationID: a hand-made RSR with a non-UUID source id must not make the hub
// reject the whole status batch; the id is omitted.
func TestNonUUIDRecommendationID(t *testing.T) {
	needEnv(t)
	rep := NewReporter(nil, "test", 10, nil)
	k := wkey{"default", "Deployment", "hand-made"}
	rep.SetStatus(k, hubclient.WorkloadStatus{RecommendationID: "hand-made-1"})
	rep.SetStatus(wkey{"default", "Deployment", "ok"}, hubclient.WorkloadStatus{RecommendationID: "6f1c2a8e-0d7b-4c55-a0b1-8f9e2f0d1c3a"})
	rep.Event(hubclient.Event{Type: "skipped", Reason: "ModeAudit", RecommendationID: "not-a-uuid"})
	if got := rep.statuses[k].RecommendationID; got != "" {
		t.Fatalf("status kept %q", got)
	}
	if got := rep.statuses[wkey{"default", "Deployment", "ok"}].RecommendationID; got == "" {
		t.Fatal("valid id dropped")
	}
	if got := rep.PendingEvents()[0].RecommendationID; got != "" {
		t.Fatalf("event kept %q", got)
	}
}

func TestValuesClose(t *testing.T) {
	mk := func(cpu, mem string) []v1alpha1.ContainerRecommendation {
		l := corev1.ResourceList{}
		if cpu != "" {
			l[corev1.ResourceCPU] = resource.MustParse(cpu)
		}
		if mem != "" {
			l[corev1.ResourceMemory] = resource.MustParse(mem)
		}
		return []v1alpha1.ContainerRecommendation{{ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: l}}}
	}
	tests := []struct {
		a, b []v1alpha1.ContainerRecommendation
		want bool
	}{
		{mk("740m", "32Mi"), mk("745m", "32Mi"), true},
		{mk("740m", "32Mi"), mk("800m", "32Mi"), false},
		{mk("740m", "32Mi"), mk("740m", ""), false},
		{nil, nil, true},
		{mk("1", ""), mk("1000m", ""), true},
	}
	for i, tt := range tests {
		if got := valuesClose(tt.a, tt.b, 0.05); got != tt.want {
			t.Errorf("%d: got %v want %v", i, got, tt.want)
		}
	}
}
