package controller

import (
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// The hub issues a new recommendation id on every run, also when the values do not
// change. Drift repair, rollback protection and the approve-once hold compare values, not ids.

// The next hub run in each case.
type nextRun struct {
	name     string
	newID    bool   // a new recommendation id (false: the same recommendation again)
	cpu      string // the next run's cpu request
	mem      string // the next run's memory request
	same     bool   // within the tolerance (5%) of the applied values
	cooldown bool   // the cooldown since the change still runs (otherwise a policy sets it to 0)
}

var nextRuns = []nextRun{
	{name: "same-id", cpu: "250m", mem: "200Mi", same: true},
	{name: "new-id", newID: true, cpu: "250m", mem: "200Mi", same: true},
	{name: "tolerance", newID: true, cpu: "260m", mem: "205Mi", same: true},
	{name: "outside", newID: true, cpu: "150m", mem: "150Mi"},
	{name: "outside-cooldown", newID: true, cpu: "150m", mem: "150Mi", cooldown: true},
}

// noCooldown sets the cooldown of one workload to 0 for the rest of the test.
func noCooldown(t *testing.T, name string) {
	t.Helper()
	p := &v1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "nocooldown-" + name},
		Spec: v1alpha1.RightsizingPolicySpec{Priority: 100, Selector: v1alpha1.PolicySelector{
			Workloads: []v1alpha1.NamespacedWorkloadRef{{Kind: v1alpha1.KindDeployment, Namespace: "default", Name: name}}},
			Guardrails: &v1alpha1.Guardrails{Cooldown: &metav1.Duration{}}},
	}
	if err := k8s.Create(testCtx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8s.Delete(testCtx, p) })
}

// nextRec is the hub's next run for r: a new id (or the same one) with the given values.
func nextRec(name string, prev hubclient.WorkloadRecommendation, run nextRun, n int) hubclient.WorkloadRecommendation {
	r := recFor(name, run.cpu, run.mem)
	r.ID = prev.ID
	if run.newID {
		r.ID = fmt.Sprintf("2c000000-0000-4000-8000-%012d", n)
	}
	return r
}

// newPod creates a pod of the Deployment with the template values, as the ReplicaSet would with
// the webhook down.
func newPod(t *testing.T, name, suffix string) *corev1.Pod {
	t.Helper()
	d := getDeploy(t, name)
	var rs appsv1.ReplicaSet
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name + "-rs"}, &rs); err != nil {
		t.Fatal(err)
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-" + suffix, Namespace: "default", Labels: d.Spec.Template.Labels,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr.To(true)}}},
		Spec: *d.Spec.Template.Spec.DeepCopy(),
	}
	p.Spec.NodeName = "node-1"
	if err := k8s.Create(testCtx, p); err != nil {
		t.Fatal(err)
	}
	setPodStatus(t, p.Name, func(pod *corev1.Pod) {
		pod.Status.Phase = corev1.PodRunning
		res := pod.Spec.Containers[0].Resources.DeepCopy()
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Image: "nginx:1.27", ImageID: "x", Ready: true,
			Started: ptr.To(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}, Resources: res}}
	})
	return p
}

// actuate makes the "kubelet" report the requested values of every pod of the workload.
func actuate(t *testing.T, name string) {
	t.Helper()
	var list corev1.PodList
	if err := k8s.List(testCtx, &list, client.InNamespace("default"), client.MatchingLabels{"app": name}); err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Items {
		setPodStatus(t, p.Name, func(pod *corev1.Pod) {
			pod.Status.ContainerStatuses[0].Resources = pod.Spec.Containers[0].Resources.DeepCopy()
		})
	}
}

func podCPU(t *testing.T, name, want string) func() error {
	return func() error {
		p := getPod(t, name)
		if got := p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != want {
			return fmt.Errorf("pod %s cpu request %s, want %s", name, got.String(), want)
		}
		return nil
	}
}

// TestDriftRepairAcrossIDs: an Applied in-place workload (cpu auto-patch). After the next hub
// run, a pod created without the webhook (template values) is still resized to the live values:
// for the same id, a new id with the same values or values within the tolerance (adopted, no
// event) and a new id blocked by the cooldown; a new id outside the tolerance is applied.
func TestDriftRepairAcrossIDs(t *testing.T) {
	needEnv(t)
	for i, run := range nextRuns {
		t.Run(run.name, func(t *testing.T) {
			name := "drift-" + run.name
			if !run.cooldown {
				noCooldown(t, name)
			}
			if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationCPUMode: "auto-patch"}, "500m", "256Mi"), false); err != nil {
				t.Fatal(err)
			}
			pod := makePod(t, getDeploy(t, name))
			r1 := recFor(name, "250m", "200Mi")
			r1.ID = fmt.Sprintf("1c000000-0000-4000-8000-%012d", i)
			setRec(r1)
			eventually(t, 20*time.Second, "pod resized", podCPU(t, pod.Name, "250m"))
			actuate(t, name)
			eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))

			r2 := nextRec(name, r1, run, i)
			setRec(r2)
			want := "250m"
			switch {
			case run.same:
				eventually(t, 10*time.Second, "next run recorded", func() error {
					rsr, err := getRSR(name)
					if err != nil {
						return err
					}
					if rsr.Status.AppliedRecommendationID != r2.ID || rsr.Spec.Source.RecommendationID != r2.ID ||
						rsr.Status.ObservedGeneration != rsr.Generation || rsr.Status.Phase != v1alpha1.PhaseApplied {
						return fmt.Errorf("phase %s applied %s source %s generation %d/%d", rsr.Status.Phase,
							rsr.Status.AppliedRecommendationID, rsr.Spec.Source.RecommendationID, rsr.Status.ObservedGeneration, rsr.Generation)
					}
					return nil
				})
			case run.cooldown:
				eventually(t, 10*time.Second, "blocked by the cooldown", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonCooldown))
			default:
				want = "150m"
				eventually(t, 20*time.Second, "pods resized to the new values", podCPU(t, pod.Name, want))
				actuate(t, name)
				eventually(t, 20*time.Second, "applied again", func() error {
					rsr, err := getRSR(name)
					if err != nil {
						return err
					}
					if rsr.Status.Phase != v1alpha1.PhaseApplied || rsr.Status.AppliedRecommendationID != r2.ID {
						return fmt.Errorf("phase %s applied %s", rsr.Status.Phase, rsr.Status.AppliedRecommendationID)
					}
					return nil
				})
			}

			// The webhook was down: a new pod with the template values. Drift repair resizes it.
			fresh := newPod(t, name, "fresh")
			eventually(t, 20*time.Second, "new pod resized to the live values", podCPU(t, fresh.Name, want))

			wantApplied := 1
			if !run.same && !run.cooldown {
				wantApplied = 2
			}
			eventually(t, 5*time.Second, "applied events", func() error {
				if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != wantApplied {
					return fmt.Errorf("applied events: %d, want %d", n, wantApplied)
				}
				return nil
			})
			wantSkipped := 0
			if run.same && run.newID {
				wantSkipped = 1
			}
			if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonNoSignificantChange); n != wantSkipped {
				t.Fatalf("skipped/NoSignificantChange events: %d, want %d", n, wantSkipped)
			}
			if run.cooldown {
				rsr, _ := getRSR(name)
				if rsr.Status.AppliedRecommendationID != r1.ID || rsr.Status.Phase != v1alpha1.PhaseApplied {
					t.Fatalf("blocked change recorded as applied: %s %s", rsr.Status.Phase, rsr.Status.AppliedRecommendationID)
				}
			}

			// Later runs with the same values: no event spam.
			if run.same && run.newID {
				r3 := r2
				r3.ID = fmt.Sprintf("3c000000-0000-4000-8000-%012d", i)
				setRec(r3)
				eventually(t, 10*time.Second, "third run adopted", func() error {
					rsr, err := getRSR(name)
					if err != nil {
						return err
					}
					if rsr.Status.AppliedRecommendationID != r3.ID {
						return fmt.Errorf("applied %s", rsr.Status.AppliedRecommendationID)
					}
					return nil
				})
				time.Sleep(time.Second)
				if n := countEvents(name, hubclient.EventSkipped, v1alpha1.ReasonNoSignificantChange); n != 1 {
					t.Fatalf("skipped re-emitted for the same values: %d", n)
				}
				if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 1 {
					t.Fatalf("applied events: %d", n)
				}
			}
		})
	}
}

// TestRollbackAcrossIDs: after a health-watch rollback the rolled-back values are never
// applied again, also under a new id or within the tolerance; different values are applied. The
// cooldown is 0 here, so only this check keeps the rolled-back values out.
func TestRollbackAcrossIDs(t *testing.T) {
	needEnv(t)
	runs := []nextRun{
		{name: "same-id", cpu: "250m", mem: "200Mi", same: true},
		{name: "new-id", newID: true, cpu: "250m", mem: "200Mi", same: true},
		{name: "tolerance", newID: true, cpu: "260m", mem: "205Mi", same: true},
		{name: "outside", newID: true, cpu: "400m", mem: "300Mi"},
	}
	for i, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			name := "rb-" + run.name
			noCooldown(t, name)
			if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi"), false); err != nil {
				t.Fatal(err)
			}
			r1 := recFor(name, "250m", "200Mi")
			r1.ID = fmt.Sprintf("4c000000-0000-4000-8000-%012d", i)
			setRec(r1)
			eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))

			// The health watch restores the previous values and records the rollback.
			rsr, _ := getRSR(name)
			var prev []v1alpha1.ContainerRecommendation
			for _, p := range rsr.Status.Previous {
				prev = append(prev, v1alpha1.ContainerRecommendation{ContainerResources: p})
			}
			eventually(t, 5*time.Second, "rollback recorded", func() error {
				rsr, err := getRSR(name)
				if err != nil {
					return err
				}
				now := metav1.Now()
				rsr.Status.Phase = v1alpha1.PhaseRolledBack
				rsr.Status.RolledBackAt = &now
				meta.SetStatusCondition(&rsr.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionHealthWatch, Status: metav1.ConditionFalse,
					Reason: v1alpha1.ReasonOOMKills, Message: "test"})
				return k8s.Status().Update(testCtx, rsr)
			})
			both := map[corev1.ResourceName]bool{corev1.ResourceCPU: true, corev1.ResourceMemory: true}
			if err := apply.ApplyTemplate(testCtx, k8s, getDeploy(t, name), v1alpha1.KindDeployment, prev, both, nil); err != nil {
				t.Fatal(err)
			}
			eventually(t, 5*time.Second, "template restored", func() error {
				if d := getDeploy(t, name); cpuReq(d) != "500m" {
					return fmt.Errorf("cpu %s", cpuReq(d))
				}
				return nil
			})

			r2 := nextRec(name, r1, run, i)
			setRec(r2)
			if !run.same {
				eventually(t, 20*time.Second, "applied", func() error {
					if d := getDeploy(t, name); cpuReq(d) != "400m" || memReq(d) != "300Mi" {
						return fmt.Errorf("template %s %s", cpuReq(d), memReq(d))
					}
					return wantPhase(name, v1alpha1.PhaseApplied)()
				})
				eventually(t, 5*time.Second, "second applied event", func() error {
					if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 2 {
						return fmt.Errorf("applied events: %d", n)
					}
					return nil
				})
				return
			}
			time.Sleep(2 * time.Second) // several pulls and reconciles
			if err := wantPhase(name, v1alpha1.PhaseRolledBack)(); err != nil {
				t.Fatal(err)
			}
			if d := getDeploy(t, name); cpuReq(d) != "500m" || memReq(d) != "256Mi" {
				t.Fatalf("rolled-back values applied again: %s %s", cpuReq(d), memReq(d))
			}
			if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 1 {
				t.Fatalf("applied events: %d", n)
			}
			rsr, _ = getRSR(name)
			if got := rsr.Spec.Containers[0].Requests[corev1.ResourceCPU]; got.String() != "250m" || rsr.Spec.Source.RecommendationID != r1.ID {
				t.Fatalf("the rolled-back spec was overwritten: cpu %s id %s", got.String(), rsr.Spec.Source.RecommendationID)
			}
		})
	}
}

// TestApprovedAcrossIDs: after an approve-once rollout the hold stays for the
// same id, a new id with the same values or within the tolerance, and ends for different values.
func TestApprovedAcrossIDs(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	runs := []nextRun{
		{name: "same-id", cpu: "250m", mem: "200Mi", same: true},
		{name: "new-id", newID: true, cpu: "250m", mem: "200Mi", same: true},
		{name: "tolerance", newID: true, cpu: "260m", mem: "205Mi", same: true},
		{name: "outside", newID: true, cpu: "150m", mem: "150Mi"},
	}
	for i, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			name := "appr-" + run.name
			r1 := approveAndApply(t, name, fmt.Sprintf("6c1d0000-0000-4000-8000-0000000002%02d", i))
			setRec(nextRec(name, r1, run, i))
			if !run.same {
				eventually(t, 10*time.Second, "hold ended", func() error {
					if o := owns(t, name); len(o) > 0 {
						return fmt.Errorf("owned %v", o)
					}
					return nil
				})
				return
			}
			time.Sleep(1500 * time.Millisecond)
			if o := owns(t, name); !o[corev1.ResourceCPU] || !o[corev1.ResourceMemory] {
				t.Fatalf("hold dropped: %v", o)
			}
			rsr, _ := getRSR(name)
			if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionApplied); c == nil || c.Reason != v1alpha1.ReasonApprovedOnce {
				t.Fatalf("applied condition %+v", c)
			}
			if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "200Mi" {
				t.Fatalf("values changed: %s %s", cpuReq(d), memReq(d))
			}
		})
	}
}

// TestApprovedMixedAdopt: cpu in auto-patch, memory approved once. A new id with the same values
// is adopted (cpu drift repair goes on) and the approved memory stays held.
func TestApprovedMixedAdopt(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "appr-mixed"
	noCooldown(t, name)
	r1 := recFor(name, "250m", "200Mi")
	r1.ID = "5c000000-0000-4000-8000-000000000001"
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{{ID: "6c1d0000-0000-4000-8000-000000000301", RecommendationID: r1.ID,
		Workload: hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: name}, ExpiresAt: time.Now().Add(time.Hour), ApprovedBy: "bob"}}})
	if err := argoApply(t, deploymentApply(name, map[string]string{v1alpha1.AnnotationCPUMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi"), false); err != nil {
		t.Fatal(err)
	}
	setRec(r1)
	eventually(t, 20*time.Second, "approved once", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonApprovedOnce))
	if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("not applied: %s %s", cpuReq(d), memReq(d))
	}

	r2 := r1
	r2.ID = "5c000000-0000-4000-8000-000000000002"
	setRec(r2)
	eventually(t, 10*time.Second, "adopted", func() error {
		rsr, err := getRSR(name)
		if err != nil {
			return err
		}
		if rsr.Status.AppliedRecommendationID != r2.ID {
			return fmt.Errorf("applied %s", rsr.Status.AppliedRecommendationID)
		}
		if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionApplied); c == nil || c.Reason != v1alpha1.ReasonApprovedOnce {
			return fmt.Errorf("applied condition %+v", c)
		}
		return nil
	})
	time.Sleep(time.Second)
	if o := owns(t, name); !o[corev1.ResourceCPU] || !o[corev1.ResourceMemory] {
		t.Fatalf("hold dropped after the adoption: %v", o)
	}
	// Argo CD (forced) reverts cpu: drift repair puts the live value back.
	git := deploymentApply(name, map[string]string{v1alpha1.AnnotationCPUMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, git, true); err != nil {
		t.Fatal(err)
	}
	annotate(t, &appsv1.Deployment{}, name, map[string]any{"test/poke": "1"})
	eventually(t, 10*time.Second, "cpu repaired", func() error {
		if d := getDeploy(t, name); cpuReq(d) != "250m" {
			return fmt.Errorf("cpu %s", cpuReq(d))
		}
		return nil
	})
	if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonApprovedOnce) + countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 1 {
		t.Fatalf("applied events: %d", n)
	}
}
