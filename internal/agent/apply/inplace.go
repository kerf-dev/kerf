package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
)

// ResizeState is the state of an in-place resize of one pod.
type ResizeState string

const (
	// ResizeNeeded: the pod spec does not carry the target values yet.
	ResizeNeeded ResizeState = "Needed"
	// ResizeInProgress: accepted, the kubelet is actuating it.
	ResizeInProgress ResizeState = "InProgress"
	// ResizeDeferred: the node cannot fit it right now (PodResizePending/Deferred).
	ResizeDeferred ResizeState = "Deferred"
	// ResizeInfeasible: the node can never fit it (PodResizePending/Infeasible).
	ResizeInfeasible ResizeState = "Infeasible"
	// ResizeDone: the running containers have the target values.
	ResizeDone ResizeState = "Done"
)

// PodResize is the observed resize state of one pod.
type PodResize struct {
	Pod     string
	State   ResizeState
	Since   time.Time
	Message string
}

// resizable reports whether a container can be resized in place (regular containers and native
// sidecars; finished init containers cannot).
func resizable(c workload.Container) bool { return !c.Init || c.Sidecar }

// desired returns the target requirements of a pod container: the targets' requests and limits
// for the given resources over the container's current values.
func desired(cur corev1.ResourceRequirements, t v1alpha1.ContainerRecommendation, own map[corev1.ResourceName]bool) corev1.ResourceRequirements {
	out := *cur.DeepCopy()
	for r := range own {
		if q, ok := t.Requests[r]; ok {
			if out.Requests == nil {
				out.Requests = corev1.ResourceList{}
			}
			out.Requests[r] = q
		}
		if q, ok := t.Limits[r]; ok {
			if out.Limits == nil {
				out.Limits = corev1.ResourceList{}
			}
			out.Limits[r] = q
		}
		// In-place resize cannot remove a limit; removals take effect on new pods (webhook) or
		// with the rollout strategy.
	}
	return out
}

func equalRes(a, b corev1.ResourceList, own map[corev1.ResourceName]bool) bool {
	for r := range own {
		qa, okA := a[r]
		qb, okB := b[r]
		if okA != okB || (okA && qa.Cmp(qb) != 0) {
			return false
		}
	}
	return true
}

func sameReq(a, b corev1.ResourceRequirements, own map[corev1.ResourceName]bool) bool {
	return equalRes(a.Requests, b.Requests, own) && equalRes(a.Limits, b.Limits, own)
}

// ObserveResize returns the resize state of a pod for the targets.
func ObserveResize(pod *corev1.Pod, targets []v1alpha1.ContainerRecommendation, own map[corev1.ResourceName]bool) PodResize {
	pr := PodResize{Pod: pod.Name, State: ResizeDone}
	for _, cond := range pod.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case corev1.PodResizePending:
			pr.Since, pr.Message = cond.LastTransitionTime.Time, cond.Message
			if cond.Reason == corev1.PodReasonInfeasible {
				pr.State = ResizeInfeasible
				return pr
			}
			pr.State = ResizeDeferred
		case corev1.PodResizeInProgress:
			if pr.State == ResizeDone {
				pr.State, pr.Since, pr.Message = ResizeInProgress, cond.LastTransitionTime.Time, cond.Message
			}
		}
	}
	statuses := map[string]corev1.ContainerStatus{}
	for _, s := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
		statuses[s.Name] = s
	}
	for _, t := range targets {
		c, ok := workload.FindContainer(&pod.Spec, t.Name)
		if !ok || !resizable(c) {
			continue
		}
		want := desired(c.Resources, t, own)
		if !sameReq(c.Resources, want, own) {
			pr.State = ResizeNeeded
			return pr
		}
		if pr.State != ResizeDone {
			continue
		}
		st, ok := statuses[t.Name]
		if !ok || st.Resources == nil {
			// The kubelet has not reported the actual resources yet.
			pr.State = ResizeInProgress
			continue
		}
		if !sameReq(*st.Resources, want, own) {
			pr.State = ResizeInProgress
		}
	}
	return pr
}

// Resize submits the targets for one pod through the pods/resize subresource. It first records
// the previous values in the pod's previous-resources annotation. It returns false if the pod
// already carries the targets.
func Resize(ctx context.Context, c client.Client, pod *corev1.Pod, targets []v1alpha1.ContainerRecommendation,
	own map[corev1.ResourceName]bool, recID string) (bool, error) {
	type ctr struct {
		Name      string                      `json:"name"`
		Resources corev1.ResourceRequirements `json:"resources"`
	}
	var containers, inits []ctr
	var previous []v1alpha1.ContainerResources
	for _, t := range targets {
		cur, ok := workload.FindContainer(&pod.Spec, t.Name)
		if !ok || !resizable(cur) {
			continue
		}
		want := desired(cur.Resources, t, own)
		if sameReq(cur.Resources, want, own) {
			continue
		}
		previous = append(previous, workload.ToAPI(cur.Name, cur.Resources))
		entry := ctr{Name: t.Name, Resources: onlyCPUMem(want)}
		if cur.Init {
			inits = append(inits, entry)
		} else {
			containers = append(containers, entry)
		}
	}
	if len(containers) == 0 && len(inits) == 0 {
		return false, nil
	}

	if pod.Annotations[v1alpha1.AnnotationRecommendationID] != recID {
		meta := map[string]any{"metadata": map[string]any{"annotations": map[string]string{
			v1alpha1.AnnotationPreviousResources: workload.PreviousJSON(previous),
			v1alpha1.AnnotationRecommendationID:  recID,
		}}}
		b, _ := json.Marshal(meta)
		if err := c.Patch(ctx, pod, client.RawPatch(types.MergePatchType, b), client.FieldOwner(FieldManager)); err != nil {
			return false, fmt.Errorf("annotate pod %s: %w", pod.Name, err)
		}
	}

	spec := map[string]any{}
	if len(containers) > 0 {
		spec["containers"] = containers
	}
	if len(inits) > 0 {
		spec["initContainers"] = inits
	}
	b, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return false, err
	}
	if err := c.SubResource("resize").Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, b), client.FieldOwner(FieldManager)); err != nil {
		return false, err
	}
	return true, nil
}

func onlyCPUMem(r corev1.ResourceRequirements) corev1.ResourceRequirements {
	pick := func(l corev1.ResourceList) corev1.ResourceList {
		out := corev1.ResourceList{}
		for _, k := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if q, ok := l[k]; ok {
				out[k] = q
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return corev1.ResourceRequirements{Requests: pick(r.Requests), Limits: pick(r.Limits)}
}

// NodeSupportsInPlace reports whether in-place resize may be used on a node (D-007): not for
// memory changes on nodes with swap (memory accounting differs; CPU is unaffected), and not on
// nodes matching one of the unsupported label selectors (e.g. nodes whose kubelet runs a static
// CPU or Memory Manager policy, which the API does not expose).
func NodeSupportsInPlace(node *corev1.Node, unsupported []map[string]string, memoryChange bool) (bool, string) {
	if sw := node.Status.NodeInfo.Swap; memoryChange && sw != nil && sw.Capacity != nil && *sw.Capacity > 0 {
		return false, fmt.Sprintf("node %s has swap (%s); in-place memory changes are not used there", node.Name, resource.NewQuantity(*sw.Capacity, resource.BinarySI))
	}
	for _, sel := range unsupported {
		if len(sel) == 0 {
			continue
		}
		match := true
		for k, v := range sel {
			if got, ok := node.Labels[k]; !ok || (v != "" && got != v) {
				match = false
				break
			}
		}
		if match {
			return false, fmt.Sprintf("node %s matches unsupported-node selector %v", node.Name, sel)
		}
	}
	return true, ""
}
