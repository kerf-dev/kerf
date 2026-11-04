// Package apply writes recommended values to the cluster.
//
//   - rollout: server-side apply of container resources on the pod template under field manager
//     resource-optimizer with force (Argo CD's controller owns the fields first, D-006);
//   - release: drop resource-optimizer's managedFields entry without deleting any field, so the
//     values stay until Argo CD (or anyone) restores them;
//   - in-place: resize running pods through the pods/resize subresource (D-007).
package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
)

// FieldManager is the SSA field manager of every write to workloads (D-006).
const FieldManager = v1alpha1.FieldManager

// releaseManager is used for the release patch itself; it ends up owning nothing.
const releaseManager = FieldManager + "-release"

// TemplateConfig builds the apply configuration that owns the given resources of the targets on
// the pod template, plus our annotations. Only resources in own are included.
func TemplateConfig(obj client.Object, kind v1alpha1.WorkloadKind, targets []v1alpha1.ContainerRecommendation,
	own map[corev1.ResourceName]bool, annotations map[string]string) (*unstructured.Unstructured, error) {
	gvk, ok := workload.Kinds[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	spec := workload.PodSpec(obj)
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvk)
	u.SetName(obj.GetName())
	u.SetNamespace(obj.GetNamespace())
	if len(annotations) > 0 {
		u.SetAnnotations(annotations)
	}
	var containers, inits []any
	for _, t := range targets {
		c, found := workload.FindContainer(spec, t.Name)
		if !found {
			continue
		}
		res := map[string]any{}
		reqs, lims := map[string]any{}, map[string]any{}
		for r := range own {
			if q, ok := t.Requests[r]; ok {
				reqs[string(r)] = q.String()
			}
			if q, ok := t.Limits[r]; ok {
				lims[string(r)] = q.String()
			}
		}
		if len(reqs) == 0 && len(lims) == 0 {
			continue
		}
		if len(reqs) > 0 {
			res["requests"] = reqs
		}
		if len(lims) > 0 {
			res["limits"] = lims
		}
		entry := map[string]any{"name": t.Name, "resources": res}
		if c.Init {
			inits = append(inits, entry)
		} else {
			containers = append(containers, entry)
		}
	}
	path := workload.TemplatePath(kind)
	if len(containers) > 0 {
		if err := unstructured.SetNestedSlice(u.Object, containers, append(path, "containers")...); err != nil {
			return nil, err
		}
	}
	if len(inits) > 0 {
		if err := unstructured.SetNestedSlice(u.Object, inits, append(path, "initContainers")...); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// ApplyTemplate server-side-applies targets for the resources in own, forcing ownership.
//
// SSA cannot remove a field another manager owns by omitting it, so a limit that must be removed
// is first taken over with its current value and then dropped in a second apply.
func ApplyTemplate(ctx context.Context, c client.Client, obj client.Object, kind v1alpha1.WorkloadKind,
	targets []v1alpha1.ContainerRecommendation, own map[corev1.ResourceName]bool, annotations map[string]string) error {
	spec := workload.PodSpec(obj)
	var takeover []v1alpha1.ContainerRecommendation
	for _, t := range targets {
		cur, ok := workload.FindContainer(spec, t.Name)
		if !ok {
			continue
		}
		needs := false
		tt := *t.DeepCopy()
		for _, r := range t.RemoveLimits {
			if !own[r] {
				continue
			}
			if q, has := cur.Resources.Limits[r]; has {
				if tt.Limits == nil {
					tt.Limits = corev1.ResourceList{}
				}
				tt.Limits[r] = q
				needs = true
			}
		}
		if needs {
			takeover = append(takeover, tt)
		}
	}
	if len(takeover) > 0 {
		merged := mergeTargets(targets, takeover)
		u, err := TemplateConfig(obj, kind, merged, own, annotations)
		if err != nil {
			return err
		}
		if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("take over limits: %w", err)
		}
	}
	u, err := TemplateConfig(obj, kind, targets, own, annotations)
	if err != nil {
		return err
	}
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(FieldManager), client.ForceOwnership)
}

func mergeTargets(base, over []v1alpha1.ContainerRecommendation) []v1alpha1.ContainerRecommendation {
	out := make([]v1alpha1.ContainerRecommendation, 0, len(base))
	for _, b := range base {
		for _, o := range over {
			if o.Name == b.Name {
				b = o
				break
			}
		}
		out = append(out, b)
	}
	return out
}

// OwnedResources returns the resources (cpu, memory) whose requests or limits resource-optimizer
// owns on the pod template, read from managedFields.
func OwnedResources(obj client.Object) map[corev1.ResourceName]bool {
	out := map[corev1.ResourceName]bool{}
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager != FieldManager || mf.FieldsV1 == nil {
			continue
		}
		var tree map[string]any
		if err := json.Unmarshal(mf.FieldsV1.GetRawBytes(), &tree); err != nil {
			continue
		}
		walkResources(tree, false, out)
	}
	return out
}

func walkResources(node map[string]any, inResources bool, out map[corev1.ResourceName]bool) {
	for k, v := range node {
		child, _ := v.(map[string]any)
		switch {
		case inResources && (k == "f:requests" || k == "f:limits"):
			for rk := range child {
				switch rk {
				case "f:cpu":
					out[corev1.ResourceCPU] = true
				case "f:memory":
					out[corev1.ResourceMemory] = true
				}
			}
		case k == "f:resources":
			walkResources(child, true, out)
		case child != nil:
			walkResources(child, false, out)
		}
	}
}

// Release removes resource-optimizer's managedFields entries and our annotations from the
// workload in one JSON patch, guarded by resourceVersion. No resource field is deleted: values
// stay as they are, now owned by nobody, so Argo CD's next sync restores the Git values (D-006).
// It returns false if there was nothing to release.
func Release(ctx context.Context, c client.Client, obj client.Object) (bool, error) {
	var keep []metav1.ManagedFieldsEntry
	dropped := false
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager == FieldManager {
			dropped = true
			continue
		}
		keep = append(keep, mf)
	}
	ours := []string{v1alpha1.AnnotationManaged, v1alpha1.AnnotationPreviousResources, v1alpha1.AnnotationRecommendationID}
	ann := obj.GetAnnotations()
	var removeAnn []string
	for _, k := range ours {
		if _, ok := ann[k]; ok {
			removeAnn = append(removeAnn, k)
		}
	}
	if !dropped && len(removeAnn) == 0 {
		return false, nil
	}
	ops := []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": obj.GetResourceVersion()}}
	if dropped {
		if len(keep) == 0 {
			// An empty list does not reset managedFields; a list with one empty entry does.
			ops = append(ops, map[string]any{"op": "replace", "path": "/metadata/managedFields", "value": []any{map[string]any{}}})
		} else {
			ops = append(ops, map[string]any{"op": "replace", "path": "/metadata/managedFields", "value": keep})
		}
	}
	for _, k := range removeAnn {
		ops = append(ops, map[string]any{"op": "remove", "path": "/metadata/annotations/" + escapeJSONPointer(k)})
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return false, err
	}
	if err := c.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, b), client.FieldOwner(releaseManager)); err != nil {
		return false, err
	}
	return true, nil
}

func escapeJSONPointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
