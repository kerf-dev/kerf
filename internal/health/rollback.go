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

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/apply"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
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

// rollback restores the previous values of the breached resources with the strategy that applied
// them, then marks the RSR RolledBack and emits rolled_back.
func (w *Watcher) rollback(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation, obj client.Object, b *breach, st *watchState) error {
	kind := rsr.Spec.TargetRef.Kind
	restored := RestoreTargets(rsr.Spec.Containers, rsr.Status.Previous, b.resources)
	strategy := rsr.Spec.Strategy
	recID := rsr.Status.AppliedRecommendationID
	log := w.Log.With("rsr", rsr.Namespace+"/"+rsr.Name, "reason", b.reason, "strategy", strategy)

	if strategy == v1alpha1.StrategyInPlace {
		own := map[corev1.ResourceName]bool{}
		for _, r := range b.resources {
			own[corev1.ResourceName(r)] = true
		}
		pods, err := workload.Pods(ctx, w.Reader, obj)
		if err != nil {
			return err
		}
		for i := range pods {
			if _, err := apply.Resize(ctx, w.Client, &pods[i], restored, own, recID); err != nil {
				w.emitFailed(rsr, obj, st, fmt.Errorf("resize pod %s back: %w", pods[i].Name, err))
				return err
			}
		}
	} else {
		// Re-apply everything we own (SSA would delete owned fields left out), with the breached
		// resources back at their previous values.
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
			w.emitFailed(rsr, obj, st, fmt.Errorf("restore pod template: %w", err))
			return err
		}
	}

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
	rollbacks.WithLabelValues(b.reason, string(strategy)).Inc()
	log.WarnContext(ctx, "rolled back", "message", b.message, "resources", b.resources)
	if w.Emitter != nil {
		w.Emitter.Emit(rsr, obj, hubclient.Event{
			Type: hubclient.EventRolledBack, Reason: b.reason, RecommendationID: recID, Strategy: string(strategy),
			Resources: b.resources, Containers: changes(rsr.Spec.Containers, restored, b.resources),
			Message: b.message, Details: b.details,
		})
	}
	return nil
}

// emitFailed emits failed RollbackError once per watch (the rollback itself is retried).
func (w *Watcher) emitFailed(rsr *v1alpha1.RightsizingRecommendation, obj client.Object, st *watchState, err error) {
	if w.Emitter == nil || st.failed {
		return
	}
	st.failed = true
	w.Emitter.Emit(rsr, obj, hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonRollbackError,
		RecommendationID: rsr.Status.AppliedRecommendationID, Strategy: string(rsr.Spec.Strategy),
		Message: err.Error(), Details: map[string]string{"error": err.Error()}})
}
