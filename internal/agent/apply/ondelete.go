package apply

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StatefulSets with updateStrategy OnDelete (WO-13): a template change only reaches a pod when
// the pod is deleted. After the rollout strategy patched the template, the agent replaces the
// outdated pods itself, highest ordinal first (the order the StatefulSet controller uses), at most
// maxDeletes at a time, and only while every pod is Ready and has been for the settle time. Pods
// are evicted (never deleted directly), so PodDisruptionBudgets are respected.

// IsOnDelete reports whether obj is a StatefulSet with updateStrategy OnDelete.
func IsOnDelete(obj client.Object) bool {
	sts, ok := obj.(*appsv1.StatefulSet)
	return ok && sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType
}

// RollPlan is the next step of an OnDelete roll.
type RollPlan struct {
	// Outdated are the pods that do not run the current template yet, highest ordinal first.
	Outdated []string
	// Evict are the pods to evict now (a prefix of Outdated, at most maxDeletes).
	Evict []*corev1.Pod
	// Waiting explains why no pod is evicted now (empty when Evict is set or nothing is outdated).
	Waiting string
	// Total is the number of pods the StatefulSet should have.
	Total int
}

// Done reports whether every pod runs the current template.
func (p RollPlan) Done() bool { return len(p.Outdated) == 0 }

// Progress is a short description, e.g. "1 of 2 pod(s) updated".
func (p RollPlan) Progress() string {
	return fmt.Sprintf("%d of %d pod(s) updated", max(p.Total-len(p.Outdated), 0), p.Total)
}

// PlanOnDelete decides which pods of an OnDelete StatefulSet to evict now.
func PlanOnDelete(sts *appsv1.StatefulSet, pods []corev1.Pod, now time.Time, settle time.Duration, maxDeletes int) RollPlan {
	plan := RollPlan{Total: 1}
	if sts.Spec.Replicas != nil {
		plan.Total = int(*sts.Spec.Replicas)
	}
	if maxDeletes < 1 {
		maxDeletes = 1
	}
	if sts.Status.ObservedGeneration < sts.Generation || sts.Status.UpdateRevision == "" {
		plan.Waiting = "waiting for the StatefulSet controller to observe the new template"
		return plan
	}
	if minReady := time.Duration(sts.Spec.MinReadySeconds) * time.Second; minReady > settle {
		settle = minReady
	}

	var outdated []*corev1.Pod
	var notReady, settling []string
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil {
			notReady = append(notReady, p.Name+" (terminating)")
			continue
		}
		ready, since := podReady(p)
		switch {
		case !ready:
			notReady = append(notReady, p.Name)
		case now.Sub(since) < settle:
			settling = append(settling, p.Name)
		}
		if p.Labels[appsv1.ControllerRevisionHashLabelKey] != sts.Status.UpdateRevision && !resourcesMatch(&sts.Spec.Template.Spec, &p.Spec) {
			outdated = append(outdated, p)
		}
	}
	slices.SortFunc(outdated, func(a, b *corev1.Pod) int { return ordinal(b.Name) - ordinal(a.Name) })
	for _, p := range outdated {
		plan.Outdated = append(plan.Outdated, p.Name)
	}
	present := len(pods) - countTerminating(pods)
	switch {
	case len(outdated) == 0 && len(notReady) == 0 && present >= plan.Total:
		return plan
	case len(notReady) > 0:
		plan.Waiting = "waiting for pod(s) to become Ready: " + strings.Join(notReady, ", ")
	case present < plan.Total:
		plan.Waiting = fmt.Sprintf("waiting for the StatefulSet to recreate pods (%d of %d present)", present, plan.Total)
	case len(settling) > 0:
		plan.Waiting = fmt.Sprintf("waiting %s after Ready (health watch) for: %s", settle, strings.Join(settling, ", "))
	}
	if plan.Waiting != "" || len(outdated) == 0 {
		return plan
	}
	plan.Evict = outdated[:min(maxDeletes, len(outdated))]
	return plan
}

func countTerminating(pods []corev1.Pod) int {
	n := 0
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			n++
		}
	}
	return n
}

func podReady(p *corev1.Pod) (bool, time.Time) {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue, c.LastTransitionTime.Time
		}
	}
	return false, time.Time{}
}

// ordinal is the StatefulSet ordinal of a pod name ("<sts>-<n>"), -1 if there is none.
func ordinal(name string) int {
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return -1
	}
	n, err := strconv.Atoi(name[i+1:])
	if err != nil {
		return -1
	}
	return n
}

// resourcesMatch reports whether the pod's containers carry the template's cpu and memory
// requests and limits.
func resourcesMatch(tmpl, pod *corev1.PodSpec) bool {
	match := func(want, got []corev1.Container) bool {
		for _, w := range want {
			var g *corev1.Container
			for i := range got {
				if got[i].Name == w.Name {
					g = &got[i]
				}
			}
			if g == nil {
				return false
			}
			for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				if !sameQ(w.Resources.Requests, g.Resources.Requests, r) || !sameQ(w.Resources.Limits, g.Resources.Limits, r) {
					return false
				}
			}
		}
		return true
	}
	return match(tmpl.Containers, pod.Containers) && match(tmpl.InitContainers, pod.InitContainers)
}

func sameQ(a, b corev1.ResourceList, r corev1.ResourceName) bool {
	qa, okA := a[r]
	qb, okB := b[r]
	return okA == okB && (!okA || qa.Cmp(qb) == 0)
}

// EvictTimeout bounds one eviction call: the API server holds an eviction while the disruption
// controller has not observed a matching PodDisruptionBudget yet.
const EvictTimeout = 10 * time.Second

// Evict evicts a pod through the eviction API, so PodDisruptionBudgets are respected. The UID
// precondition makes sure a recreated pod of the same name is never evicted by mistake.
func Evict(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	uid := pod.UID
	ev := &policyv1.Eviction{
		ObjectMeta:    metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}},
	}
	cctx, cancel := context.WithTimeout(ctx, EvictTimeout)
	defer cancel()
	err := c.SubResource("eviction").Create(cctx, pod, ev)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: eviction did not complete within %s (PodDisruptionBudget not observed yet?)", errEvictionTimeout, EvictTimeout)
	}
	return err
}

var errEvictionTimeout = errors.New("eviction timed out")

// EvictionRefused reports whether an eviction was refused by a PodDisruptionBudget (or held by
// the API server until it timed out); the pod should be retried later.
func EvictionRefused(err error) bool {
	return apierrors.IsTooManyRequests(err) || errors.Is(err, errEvictionTimeout) || apierrors.IsTimeout(err)
}
