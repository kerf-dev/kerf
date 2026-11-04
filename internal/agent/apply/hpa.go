package apply

// HPA coordination (WO-11, lifts D-011).
//
// An HPA on a resource's utilization divides the pods' usage by the sum of their requests and
// scales so that the ratio stays at its target: desired = ceil(replicas x utilization / target).
// Changing the requests alone therefore changes when it scales. The agent changes the requests
// and the target together, target' = target x sum(requests before) / sum(requests after), so
// sum(requests) x target and with it the scaling behaviour stay the same. The target is written
// with server-side apply under field manager resource-optimizer (Argo CD ignores the fields that
// manager owns, D-006) and restored by a rollback; releasing the workload releases the HPA too.

import (
	"context"
	"fmt"
	"math"
	"slices"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// HPA problems: why an HPA that scales on a utilization cannot be coordinated.
const (
	HPAProblemMetrics     = "the HPA scales on more than one utilization metric or on other metrics too"
	HPAProblemContainer   = "the HPA scales on a per-container (ContainerResource) utilization"
	HPAProblemMaxReplicas = "the HPA is at maxReplicas"
	HPAProblemNoRequest   = "a container has no request of the HPA's resource"
)

// HPACoupling is how an HPA couples to the workload's requests.
type HPACoupling struct {
	HPA *autoscalingv2.HorizontalPodAutoscaler
	// Coupled: the HPA scales on CPU or memory utilization (D-011).
	Coupled bool
	// Resource is the utilization resource and Target its averageUtilization (when coordinatable).
	Resource corev1.ResourceName
	Target   int32
	// Problem is set when a coupled HPA cannot be coordinated.
	Problem string
}

// Coordinatable reports whether requests of Resource may change together with Target.
func (c HPACoupling) Coordinatable() bool { return c.Coupled && c.Problem == "" && c.Resource != "" }

// FindHPA returns the HPA whose scale target is the workload, nil if there is none.
func FindHPA(ctx context.Context, r client.Reader, ns string, kind v1alpha1.WorkloadKind, name string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := r.List(ctx, &hpas, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	for i := range hpas.Items {
		h := &hpas.Items[i]
		if h.Spec.ScaleTargetRef.Kind == string(kind) && h.Spec.ScaleTargetRef.Name == name {
			return h, nil
		}
	}
	return nil, nil
}

// Coupling analyses an HPA (nil: not coupled).
func Coupling(h *autoscalingv2.HorizontalPodAutoscaler) HPACoupling {
	out := HPACoupling{HPA: h}
	if h == nil {
		return out
	}
	var util []autoscalingv2.MetricSpec
	other := 0
	for _, m := range h.Spec.Metrics {
		var name corev1.ResourceName
		var target autoscalingv2.MetricTarget
		switch {
		case m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil:
			name, target = m.Resource.Name, m.Resource.Target
		case m.Type == autoscalingv2.ContainerResourceMetricSourceType && m.ContainerResource != nil:
			name, target = m.ContainerResource.Name, m.ContainerResource.Target
		default:
			other++
			continue
		}
		if (name == corev1.ResourceCPU || name == corev1.ResourceMemory) && target.Type == autoscalingv2.UtilizationMetricType {
			util = append(util, m)
		} else {
			other++
		}
	}
	if len(util) == 0 {
		return out
	}
	out.Coupled = true
	m := util[0]
	switch {
	case len(util) > 1 || other > 0:
		out.Problem = HPAProblemMetrics
	case m.Type == autoscalingv2.ContainerResourceMetricSourceType:
		out.Problem = HPAProblemContainer
	case m.Resource.Target.AverageUtilization == nil:
		out.Problem = HPAProblemMetrics
	default:
		out.Resource, out.Target = m.Resource.Name, *m.Resource.Target.AverageUtilization
	}
	if out.Problem == "" && h.Status.CurrentReplicas >= h.Spec.MaxReplicas {
		out.Problem = HPAProblemMaxReplicas
	}
	return out
}

// RequestSum is the sum of the requests of res over the pod's app containers, with the values in
// targets overriding the live ones. ok is false when a container has no request of res (the HPA
// cannot compute a utilization then).
func RequestSum(spec *corev1.PodSpec, targets []v1alpha1.ContainerRecommendation, res corev1.ResourceName) (float64, bool) {
	sum := 0.0
	for _, c := range spec.Containers {
		q, ok := c.Resources.Requests[res]
		for _, t := range targets {
			if t.Name == c.Name {
				if tq, has := t.Requests[res]; has {
					q, ok = tq, true
				}
			}
		}
		if !ok || q.IsZero() {
			return 0, false
		}
		sum += q.AsApproximateFloat64()
	}
	return sum, len(spec.Containers) > 0
}

// NewHPATarget returns the target that keeps sum(requests) x target constant when the requests
// sum changes from before to after.
func NewHPATarget(target int32, before, after float64) int32 {
	if after <= 0 {
		return target
	}
	return int32(math.Round(float64(target) * before / after))
}

// HPATargetOf returns the live averageUtilization target of res (0 if none).
func HPATargetOf(h *autoscalingv2.HorizontalPodAutoscaler, res corev1.ResourceName) int32 {
	for _, m := range h.Spec.Metrics {
		if m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil && m.Resource.Name == res &&
			m.Resource.Target.Type == autoscalingv2.UtilizationMetricType && m.Resource.Target.AverageUtilization != nil {
			return *m.Resource.Target.AverageUtilization
		}
	}
	return 0
}

// ApplyHPATarget sets the averageUtilization target of res with server-side apply, forcing
// ownership. spec.metrics is an atomic list, so the whole list is applied: the HPA's other
// metrics (a coordinatable HPA has none) would be kept as they are.
func ApplyHPATarget(ctx context.Context, c client.Client, h *autoscalingv2.HorizontalPodAutoscaler, res corev1.ResourceName, target int32) error {
	var metrics []any
	found := false
	for _, m := range h.Spec.Metrics {
		mm, err := toMap(m)
		if err != nil {
			return err
		}
		if m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil && m.Resource.Name == res &&
			m.Resource.Target.Type == autoscalingv2.UtilizationMetricType {
			mm = map[string]any{"type": string(autoscalingv2.ResourceMetricSourceType), "resource": map[string]any{
				"name": string(res), "target": map[string]any{"type": string(autoscalingv2.UtilizationMetricType), "averageUtilization": int64(target)},
			}}
			found = true
		}
		metrics = append(metrics, mm)
	}
	if !found {
		return fmt.Errorf("HPA %s has no %s utilization metric", h.Name, res)
	}
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(autoscalingv2.SchemeGroupVersion.WithKind("HorizontalPodAutoscaler"))
	u.SetName(h.Name)
	u.SetNamespace(h.Namespace)
	if err := unstructured.SetNestedSlice(u.Object, metrics, "spec", "metrics"); err != nil {
		return err
	}
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(FieldManager), client.ForceOwnership)
}

func toMap(m autoscalingv2.MetricSpec) (map[string]any, error) {
	return runtime.DefaultUnstructuredConverter.ToUnstructured(&m)
}

// HPAOwned reports whether resource-optimizer owns fields of the HPA.
func HPAOwned(h *autoscalingv2.HorizontalPodAutoscaler) bool {
	for _, mf := range h.GetManagedFields() {
		if mf.Manager == FieldManager {
			return true
		}
	}
	return false
}

// RestoreHPA sets the HPA target of rsr back to status.previousHPATarget when one of resources
// (the resources a rollback restores) is the HPA's resource. It does nothing for RSRs without an
// HPA target, and when the HPA is gone.
func RestoreHPA(ctx context.Context, c client.Client, r client.Reader, rsr *v1alpha1.RightsizingRecommendation, resources []string) error {
	t := rsr.Spec.HPA
	prev := rsr.Status.PreviousHPATarget
	if t == nil || prev == nil || !slices.Contains(resources, string(t.Resource)) {
		return nil
	}
	var h autoscalingv2.HorizontalPodAutoscaler
	if err := r.Get(ctx, client.ObjectKey{Namespace: rsr.Namespace, Name: t.Name}, &h); err != nil {
		return client.IgnoreNotFound(err)
	}
	if HPATargetOf(&h, t.Resource) == *prev {
		return nil
	}
	return ApplyHPATarget(ctx, c, &h, t.Resource, *prev)
}
