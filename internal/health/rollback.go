package health

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

var allResources = []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory}

func findPrev(previous []v1alpha1.ContainerResources, name string) v1alpha1.ContainerResources {
	for _, p := range previous {
		if p.Name == name {
			return p
		}
	}
	return v1alpha1.ContainerResources{Name: name}
}

func qEqual(a, b corev1.ResourceList, r corev1.ResourceName) bool {
	qa, okA := a[r]
	qb, okB := b[r]
	return okA == okB && (!okA || qa.Cmp(qb) == 0)
}

// containerChanged reports whether the target changed resource r of a container compared to previous.
func containerChanged(prev v1alpha1.ContainerResources, t v1alpha1.ContainerRecommendation, r corev1.ResourceName) bool {
	if _, ok := t.Requests[r]; ok && !qEqual(prev.Requests, t.Requests, r) {
		return true
	}
	if _, ok := t.Limits[r]; ok && !qEqual(prev.Limits, t.Limits, r) {
		return true
	}
	if slices.Contains(t.RemoveLimits, r) {
		if _, had := prev.Limits[r]; had {
			return true
		}
	}
	return false
}

// ChangedResources returns the resources ("cpu", "memory") the applied change modified.
func ChangedResources(previous []v1alpha1.ContainerResources, targets []v1alpha1.ContainerRecommendation) []string {
	var out []string
	for _, r := range allResources {
		if len(changedContainers(previous, targets, string(r))) > 0 {
			out = append(out, string(r))
		}
	}
	return out
}

func changedContainers(previous []v1alpha1.ContainerResources, targets []v1alpha1.ContainerRecommendation, res string) []string {
	var out []string
	for _, t := range targets {
		if containerChanged(findPrev(previous, t.Name), t, corev1.ResourceName(res)) {
			out = append(out, t.Name)
		}
	}
	return out
}

// RestoreTargets returns the targets with the given resources set back to the previous values.
// A limit the change added is removed again (RemoveLimits); one it removed comes back.
func RestoreTargets(targets []v1alpha1.ContainerRecommendation, previous []v1alpha1.ContainerResources, resources []string) []v1alpha1.ContainerRecommendation {
	out := make([]v1alpha1.ContainerRecommendation, 0, len(targets))
	for _, t := range targets {
		t = *t.DeepCopy()
		prev := findPrev(previous, t.Name)
		for _, rs := range resources {
			r := corev1.ResourceName(rs)
			t.RemoveLimits = slices.DeleteFunc(t.RemoveLimits, func(x corev1.ResourceName) bool { return x == r })
			restore := func(dst *corev1.ResourceList, src corev1.ResourceList, isLimit bool) {
				if q, ok := src[r]; ok {
					if *dst == nil {
						*dst = corev1.ResourceList{}
					}
					(*dst)[r] = q
					return
				}
				if _, had := (*dst)[r]; had {
					delete(*dst, r)
					if isLimit {
						t.RemoveLimits = append(t.RemoveLimits, r)
					}
				}
			}
			restore(&t.Requests, prev.Requests, false)
			restore(&t.Limits, prev.Limits, true)
		}
		out = append(out, t)
	}
	return out
}

func values(l corev1.ResourceList, resources []string) *hubclient.ResourceValues {
	v := &hubclient.ResourceValues{}
	for _, r := range resources {
		q, ok := l[corev1.ResourceName(r)]
		if !ok {
			continue
		}
		if r == "cpu" {
			v.CPU = q.String()
		} else {
			v.Memory = q.String()
		}
	}
	if v.IsZero() {
		return nil
	}
	return v
}

func changes(from, to []v1alpha1.ContainerRecommendation, resources []string) []hubclient.ContainerChange {
	var out []hubclient.ContainerChange
	for i := range from {
		f, t := from[i], to[i]
		same := true
		for _, r := range resources {
			rn := corev1.ResourceName(r)
			if !qEqual(f.Requests, t.Requests, rn) || !qEqual(f.Limits, t.Limits, rn) {
				same = false
			}
		}
		if same {
			continue
		}
		out = append(out, hubclient.ContainerChange{Name: f.Name,
			From: &hubclient.Resources{Requests: values(f.Requests, resources), Limits: values(f.Limits, resources)},
			To:   &hubclient.Resources{Requests: values(t.Requests, resources), Limits: values(t.Limits, resources)}})
	}
	return out
}

// rollback records the decision first (phase RolledBack, so the agent stops keeping the applied
// values live and the webhook stops injecting them), then restores the previous values of the
// breached resources with the strategy that applied them and emits rolled_back. If the restore
// fails, the reconcile of the RolledBack RSR retries it (finishRollback).
func (w *Watcher) rollback(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation, obj client.Object, b *breach) error {
	now := metav1.NewTime(w.now())
	key := client.ObjectKeyFromObject(rsr)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := w.Client.Get(ctx, key, rsr); err != nil {
			return err
		}
		rsr.Status.Phase = v1alpha1.PhaseRolledBack
		rsr.Status.RolledBackAt = &now
		meta.SetStatusCondition(&rsr.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionHealthWatch,
			Status: metav1.ConditionFalse, Reason: b.reason, Message: b.message})
		meta.SetStatusCondition(&rsr.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionApplied,
			Status: metav1.ConditionFalse, Reason: b.reason, Message: "rolled back: " + b.message})
		return w.Client.Status().Update(ctx, rsr)
	})
	if err != nil {
		return fmt.Errorf("mark rolled back: %w", err)
	}
	rollbacks.WithLabelValues(b.reason, string(rsr.Spec.Strategy)).Inc()
	w.Log.WarnContext(ctx, "rolling back", "rsr", key.String(), "reason", b.reason, "strategy", rsr.Spec.Strategy,
		"message", b.message, "resources", b.resources)
	return w.restore(ctx, rsr, obj, b)
}

// rolledBackResources are the resources a rollback with this reason restores.
func rolledBackResources(rsr *v1alpha1.RightsizingRecommendation, reason string) []string {
	if r := ResourcesForReason(reason); r != nil {
		return r
	}
	return ChangedResources(rsr.Status.Previous, rsr.Spec.Containers)
}

// finishRollback re-runs the restore of a RolledBack RSR whose restore has not been confirmed
// (it failed, or the agent restarted in between). It does nothing once the live values match.
func (w *Watcher) finishRollback(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation) error {
	c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionHealthWatch)
	if rsr.Status.RolledBackAt == nil || c == nil || c.Status != metav1.ConditionFalse ||
		(c.Reason != v1alpha1.ReasonOOMKills && c.Reason != v1alpha1.ReasonRestarts && c.Reason != v1alpha1.ReasonThrottling) {
		return nil
	}
	at := rsr.Status.RolledBackAt.Time
	w.state.mu.Lock()
	done := w.state.restored[rsr.UID].Equal(at)
	w.state.mu.Unlock()
	if done {
		return nil
	}
	obj, err := workload.New(rsr.Spec.TargetRef.Kind)
	if err != nil {
		return nil //nolint:nilerr // not a workload kind we manage
	}
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: rsr.Namespace, Name: rsr.Spec.TargetRef.Name}, obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	b := &breach{reason: c.Reason, resources: rolledBackResources(rsr, c.Reason), message: c.Message}
	if err := apply.RestoreHPA(ctx, w.Client, w.Reader, rsr, b.resources); err != nil { // WO-11; no-op once restored
		return err
	}
	restored := RestoreTargets(rsr.Spec.Containers, rsr.Status.Previous, b.resources)
	live, err := w.liveMatches(ctx, rsr, obj, restored, b.resources)
	if err != nil {
		return err
	}
	if live {
		w.markRestored(rsr)
		return nil
	}
	return w.restore(ctx, rsr, obj, b)
}

func (w *Watcher) markRestored(rsr *v1alpha1.RightsizingRecommendation) {
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	w.state.restored[rsr.UID] = rsr.Status.RolledBackAt.Time
	delete(w.state.restoreFailed, rsr.UID)
}

func ownSet(resources []string) map[corev1.ResourceName]bool {
	own := map[corev1.ResourceName]bool{}
	for _, r := range resources {
		own[corev1.ResourceName(r)] = true
	}
	return own
}

// liveMatches reports whether the workload already runs the restored values.
func (w *Watcher) liveMatches(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation, obj client.Object,
	restored []v1alpha1.ContainerRecommendation, resources []string) (bool, error) {
	own := ownSet(resources)
	if rsr.Spec.Strategy == v1alpha1.StrategyInPlace {
		pods, err := workload.Pods(ctx, w.Reader, obj)
		if err != nil {
			return false, err
		}
		for i := range pods {
			if apply.ObserveResize(&pods[i], restored, own).State == apply.ResizeNeeded {
				return false, nil
			}
		}
		return true, nil
	}
	spec := workload.PodSpec(obj)
	for _, t := range restored {
		c, ok := workload.FindContainer(spec, t.Name)
		if !ok {
			continue
		}
		for r := range own {
			if !qEqual(c.Resources.Requests, t.Requests, r) {
				return false, nil
			}
			if _, want := t.Limits[r]; want && !qEqual(c.Resources.Limits, t.Limits, r) {
				return false, nil
			}
			if _, has := c.Resources.Limits[r]; has && slices.Contains(t.RemoveLimits, r) {
				return false, nil
			}
		}
	}
	return true, nil
}

// restore writes the previous values and emits rolled_back once it succeeded.
func (w *Watcher) restore(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation, obj client.Object, b *breach) error {
	kind := rsr.Spec.TargetRef.Kind
	restored := RestoreTargets(rsr.Spec.Containers, rsr.Status.Previous, b.resources)
	strategy := rsr.Spec.Strategy
	recID := rsr.Status.AppliedRecommendationID

	if strategy == v1alpha1.StrategyInPlace {
		own := ownSet(b.resources)
		pods, err := workload.Pods(ctx, w.Reader, obj)
		if err != nil {
			return err
		}
		for i := range pods {
			if _, err := apply.Resize(ctx, w.Client, &pods[i], restored, own, recID); err != nil {
				w.emitFailed(rsr, obj, fmt.Errorf("resize pod %s back: %w", pods[i].Name, err))
				return err
			}
		}
	} else {
		// Re-apply everything we own (SSA would delete owned fields left out), with the breached
		// resources back at their previous values. Read the workload uncached: the cache does not
		// necessarily hold its managedFields.
		if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		own := apply.OwnedResources(obj)
		for _, r := range b.resources {
			own[corev1.ResourceName(r)] = true
		}
		ann := map[string]string{v1alpha1.AnnotationManaged: "true"}
		for _, k := range []string{v1alpha1.AnnotationRecommendationID, v1alpha1.AnnotationPreviousResources} {
			if v, ok := obj.GetAnnotations()[k]; ok {
				ann[k] = v
			}
		}
		if err := apply.ApplyTemplate(ctx, w.Client, obj, kind, restored, own, ann); err != nil {
			w.emitFailed(rsr, obj, fmt.Errorf("restore pod template: %w", err))
			return err
		}
		// The HPA target changed together with the requests goes back with them (WO-11).
		if err := apply.RestoreHPA(ctx, w.Client, w.Reader, rsr, b.resources); err != nil {
			w.emitFailed(rsr, obj, fmt.Errorf("restore HPA target: %w", err))
			return err
		}
	}
	w.markRestored(rsr)
	w.Log.WarnContext(ctx, "rolled back", "rsr", rsr.Namespace+"/"+rsr.Name, "reason", b.reason, "strategy", strategy)
	if w.Emitter != nil {
		w.Emitter.Emit(rsr, obj, hubclient.Event{
			Type: hubclient.EventRolledBack, Reason: b.reason, RecommendationID: recID, Strategy: string(strategy),
			Resources: b.resources, Containers: changes(rsr.Spec.Containers, restored, b.resources),
			Message: b.message, Details: b.details,
		})
	}
	return nil
}

// emitFailed emits failed RollbackError once per rollback (the restore itself is retried).
func (w *Watcher) emitFailed(rsr *v1alpha1.RightsizingRecommendation, obj client.Object, err error) {
	rollbackErrors.Inc()
	at := rsr.Status.RolledBackAt.Time
	w.state.mu.Lock()
	seen := w.state.restoreFailed[rsr.UID].Equal(at)
	w.state.restoreFailed[rsr.UID] = at
	w.state.mu.Unlock()
	if w.Emitter == nil || seen {
		return
	}
	w.Emitter.Emit(rsr, obj, hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonRollbackError,
		RecommendationID: rsr.Status.AppliedRecommendationID, Strategy: string(rsr.Spec.Strategy),
		Message: err.Error(), Details: map[string]string{"error": err.Error()}})
}
