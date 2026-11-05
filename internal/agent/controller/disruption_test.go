package controller

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

func wantBlocked(name, reason string) func() error {
	return func() error {
		r, err := getRSR(name)
		if err != nil {
			return err
		}
		c := meta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionBlocked)
		if r.Status.Phase != v1alpha1.PhaseBlocked || c == nil || c.Status != metav1.ConditionTrue || c.Reason != reason {
			return fmt.Errorf("phase %s, Blocked condition %+v", r.Status.Phase, c)
		}
		return nil
	}
}

// TestSingleReplicaRecreateBlocked: a single-replica Deployment with strategy Recreate is never
// rolled out (blocked SingleReplicaUnsafe, template untouched) until it opts in.
func TestSingleReplicaRecreateBlocked(t *testing.T) {
	needEnv(t)
	const name = "single-recreate"
	git := deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	git.Object["spec"].(map[string]any)["strategy"] = map[string]any{"type": "Recreate"}
	if err := argoApply(t, git, false); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(name, "250m", "200Mi"))

	eventually(t, 20*time.Second, "blocked", wantBlocked(name, v1alpha1.ReasonSingleReplicaUnsafe))
	eventually(t, 5*time.Second, "blocked event", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonSingleReplicaUnsafe))
	r, _ := getRSR(name)
	if c := meta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionBlocked); !strings.Contains(c.Message, "Recreate") ||
		!strings.Contains(c.Message, v1alpha1.AnnotationAllowDisruption) {
		t.Fatalf("Blocked message does not explain: %q", c.Message)
	}
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatalf("template patched despite the block: %s", cpuReq(d))
	}
	time.Sleep(time.Second)
	if n := countEvents(name, hubclient.EventBlocked, v1alpha1.ReasonSingleReplicaUnsafe); n != 1 {
		t.Fatalf("blocked event emitted %d times, want once (D-017)", n)
	}

	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationAllowDisruption: "true"})
	eventually(t, 20*time.Second, "applied after opt-in", wantPhase(name, v1alpha1.PhaseApplied))
	if d := getDeploy(t, name); cpuReq(d) != "250m" {
		t.Fatalf("template after opt-in: %s", cpuReq(d))
	}
}

// TestSingleReplicaFallbackRefused: an in-place resize of a single-replica Deployment that turns
// Infeasible does not fall back to an unsafe rollout: the resize is reverted and the RSR blocked.
func TestSingleReplicaFallbackRefused(t *testing.T) {
	needEnv(t)
	const name = "single-fallback"
	git := deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch"}, "500m", "256Mi")
	git.Object["spec"].(map[string]any)["strategy"] = map[string]any{"type": "Recreate"}
	if err := argoApply(t, git, false); err != nil {
		t.Fatal(err)
	}
	pod := makePod(t, getDeploy(t, name))
	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 20*time.Second, "pod resized", func() error {
		if got := getPod(t, pod.Name).Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
	setPodStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodResizePending, Status: corev1.ConditionTrue,
			Reason: corev1.PodReasonInfeasible, Message: "Node didn't have enough capacity", LastTransitionTime: metav1.Now()})
	})
	eventually(t, 20*time.Second, "blocked", wantBlocked(name, v1alpha1.ReasonSingleReplicaUnsafe))
	eventually(t, 5*time.Second, "blocked event", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonSingleReplicaUnsafe))
	if n := countEvents(name, hubclient.EventFallback, v1alpha1.ReasonResizeInfeasible); n != 0 {
		t.Fatalf("fallback emitted although refused: %d", n)
	}
	eventually(t, 10*time.Second, "pending resize reverted", func() error {
		if got := getPod(t, pod.Name).Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "500m" {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
	time.Sleep(time.Second)
	r, _ := getRSR(name)
	if r.Spec.Strategy != v1alpha1.StrategyRollout || r.Status.Phase != v1alpha1.PhaseBlocked {
		t.Fatalf("strategy %s phase %s: the refusal must stick", r.Spec.Strategy, r.Status.Phase)
	}
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatalf("template patched: %s", cpuReq(d))
	}
}

func getRSRKind(kind v1alpha1.WorkloadKind, name string) (*v1alpha1.RightsizingRecommendation, error) {
	var list v1alpha1.RightsizingRecommendationList
	if err := k8s.List(testCtx, &list, client.InNamespace("default")); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.TargetRef.Name == name && list.Items[i].Spec.TargetRef.Kind == kind {
			return &list.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no RSR for %s/%s", kind, name)
}

// stsController emulates the StatefulSet controller for an OnDelete StatefulSet in envtest:
// it publishes the update revision and recreates deleted pods from the current template, Ready.
type stsController struct {
	t       *testing.T
	sts     *appsv1.StatefulSet
	mu      sync.Mutex
	evicted []string
	// zeroReady is set if all pods were ever missing or not Ready at once.
	zeroReady bool
}

func (c *stsController) podName(i int) string { return fmt.Sprintf("%s-%d", c.sts.Name, i) }

func (c *stsController) createPod(i int, rev string, spec corev1.PodSpec) {
	c.t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: c.podName(i), Namespace: "default",
			Labels: map[string]string{"app": c.sts.Name, appsv1.ControllerRevisionHashLabelKey: rev},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: c.sts.Name,
				UID: c.sts.UID, Controller: ptr.To(true)}}},
		Spec: spec, // no nodeName: deletion is immediate without a kubelet
	}
	if err := k8s.Create(testCtx, p); err != nil {
		c.t.Errorf("create %s: %v", p.Name, err)
		return
	}
	setPodStatus(c.t, p.Name, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodRunning
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
	})
}

// run publishes the update revision once the template changed and recreates missing pods.
func (c *stsController) run(stop <-chan struct{}, replicas int) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(50 * time.Millisecond):
		}
		var sts appsv1.StatefulSet
		if err := k8s.Get(testCtx, client.ObjectKeyFromObject(c.sts), &sts); err != nil {
			continue
		}
		rev := fmt.Sprintf("rev-%d", sts.Generation)
		if sts.Status.ObservedGeneration != sts.Generation {
			sts.Status.ObservedGeneration, sts.Status.UpdateRevision, sts.Status.Replicas = sts.Generation, rev, int32(replicas)
			_ = k8s.Status().Update(testCtx, &sts)
		}
		ready := 0
		for i := 0; i < replicas; i++ {
			var p corev1.Pod
			err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: c.podName(i)}, &p)
			if apierrors.IsNotFound(err) {
				c.mu.Lock()
				c.evicted = append(c.evicted, c.podName(i))
				c.mu.Unlock()
				c.createPod(i, rev, *sts.Spec.Template.Spec.DeepCopy())
				continue
			}
			if err == nil && p.DeletionTimestamp == nil {
				for _, cond := range p.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						ready++
					}
				}
			}
		}
		if ready == 0 {
			c.mu.Lock()
			c.zeroReady = true
			c.mu.Unlock()
		}
	}
}

func stsReq(sts *appsv1.StatefulSet) string {
	q := sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	return q.String()
}

// TestStatefulSetOnDelete: rollout patches the template of an OnDelete StatefulSet, then the
// agent evicts the outdated pods highest ordinal first, one at a time, waiting for the replaced
// pod to be Ready; a PodDisruptionBudget that refuses an eviction blocks the roll until it allows it.
func TestStatefulSetOnDelete(t *testing.T) {
	needEnv(t)
	const name = "db-ondelete"
	const replicas = 2
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			Annotations: map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(replicas)), ServiceName: name,
			Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			Template:       corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: spec},
		},
	}
	if err := k8s.Create(testCtx, sts); err != nil {
		t.Fatal(err)
	}
	// A PDB that allows one disruption by spec, but whose status (set by hand: there is no
	// disruption controller in envtest) does not allow any yet: evictions are refused with 429.
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: ptr.To(intstr.FromInt32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}},
	}
	if err := k8s.Create(testCtx, pdb); err != nil {
		t.Fatal(err)
	}
	setPDB := func(allowed int32) {
		eventually(t, 5*time.Second, "PDB status", func() error {
			var cur policyv1.PodDisruptionBudget
			if err := k8s.Get(testCtx, client.ObjectKeyFromObject(pdb), &cur); err != nil {
				return err
			}
			cur.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: cur.Generation, DisruptionsAllowed: allowed,
				CurrentHealthy: replicas, DesiredHealthy: replicas - 1, ExpectedPods: replicas}
			return k8s.Status().Update(testCtx, &cur)
		})
	}
	setPDB(0)
	ctl := &stsController{t: t, sts: sts}
	for i := 0; i < replicas; i++ {
		ctl.createPod(i, "rev-1", spec)
	}
	sts.Status = appsv1.StatefulSetStatus{ObservedGeneration: sts.Generation, UpdateRevision: "rev-1", CurrentRevision: "rev-1", Replicas: replicas}
	if err := k8s.Status().Update(testCtx, sts); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { ctl.run(stop, replicas); close(done) }()
	defer func() { close(stop); <-done }()

	r := recFor(name, "250m", "200Mi")
	r.Workload.Kind = "StatefulSet"
	setRec(r)

	eventually(t, 20*time.Second, "template patched", func() error {
		var cur appsv1.StatefulSet
		if err := k8s.Get(testCtx, client.ObjectKeyFromObject(sts), &cur); err != nil {
			return err
		}
		if stsReq(&cur) != "250m" {
			return fmt.Errorf("template cpu %s", stsReq(&cur))
		}
		return nil
	})
	eventually(t, 20*time.Second, "eviction refused by the PDB", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonPodDisruptionBudget))
	rsr, err := getRSRKind(v1alpha1.KindStatefulSet, name)
	if err != nil {
		t.Fatal(err)
	}
	if rsr.Status.Phase != v1alpha1.PhaseApplied {
		t.Fatalf("phase %s", rsr.Status.Phase)
	}
	if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionBlocked); c == nil || c.Reason != v1alpha1.ReasonPodDisruptionBudget {
		t.Fatalf("Blocked condition does not explain the refused eviction: %+v", c)
	}
	if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionApplied); c == nil || !strings.Contains(c.Message, "0 of 2 pod(s) updated") {
		t.Fatalf("Applied condition does not show progress: %+v", c)
	}

	// The disruption controller allows one disruption.
	setPDB(1)
	// After the first eviction the API server lowers disruptionsAllowed; restore it once the pod is back.
	go func() {
		for i := 0; i < 200; i++ {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			var cur policyv1.PodDisruptionBudget
			if err := k8s.Get(testCtx, client.ObjectKeyFromObject(pdb), &cur); err != nil || cur.Status.DisruptionsAllowed > 0 {
				continue
			}
			cur.Status.DisruptionsAllowed, cur.Status.DisruptedPods = 1, nil
			_ = k8s.Status().Update(testCtx, &cur)
		}
	}()

	eventually(t, 30*time.Second, "all pods replaced", func() error {
		for i := 0; i < replicas; i++ {
			var p corev1.Pod
			if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: ctl.podName(i)}, &p); err != nil {
				return err
			}
			if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
				return fmt.Errorf("%s cpu %s", p.Name, got.String())
			}
		}
		return nil
	})
	ctl.mu.Lock()
	evicted, zero := append([]string(nil), ctl.evicted...), ctl.zeroReady
	ctl.mu.Unlock()
	if strings.Join(evicted, ",") != name+"-1,"+name+"-0" {
		t.Fatalf("eviction order %v, want highest ordinal first, each pod once", evicted)
	}
	if zero {
		t.Fatal("there was a moment without a Ready pod")
	}
	eventually(t, 10*time.Second, "roll finished", func() error {
		rsr, err := getRSRKind(v1alpha1.KindStatefulSet, name)
		if err != nil {
			return err
		}
		c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionApplied)
		b := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionBlocked)
		if rsr.Status.Phase != v1alpha1.PhaseApplied || c == nil || strings.Contains(c.Message, "OnDelete") || b == nil || b.Status != metav1.ConditionFalse {
			return fmt.Errorf("phase %s applied %+v blocked %+v", rsr.Status.Phase, c, b)
		}
		return nil
	})
	if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 1 {
		t.Fatalf("applied events: %d", n)
	}
}
