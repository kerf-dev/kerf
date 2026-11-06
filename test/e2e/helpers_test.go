//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// fieldManager is the agent's server-side-apply field manager.
const fieldManager = "kerf"

// ownsCPURequest reports whether the agent's field manager owns
// spec.template.spec.containers[*].resources.requests.cpu.
func ownsCPURequest(mf []metav1.ManagedFieldsEntry) bool {
	for _, e := range mf {
		if e.Manager != fieldManager || e.FieldsV1 == nil {
			continue
		}
		var f map[string]any
		if json.Unmarshal(e.FieldsV1.GetRawBytes(), &f) != nil {
			continue
		}
		containers := dig(f, "f:spec", "f:template", "f:spec", "f:containers")
		for k, v := range containers {
			if strings.HasPrefix(k, "k:") && dig(asMap(v), "f:resources", "f:requests", "f:cpu") != nil {
				return true
			}
		}
	}
	return false
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func dig(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		if m == nil {
			return nil
		}
		m = asMap(m[p])
	}
	return m
}

func managers(mf []metav1.ManagedFieldsEntry) string {
	var s []string
	for _, e := range mf {
		s = append(s, fmt.Sprintf("%s(%s)", e.Manager, e.Operation))
	}
	return "managers: " + strings.Join(s, ", ")
}

func replicaSets(t *testing.T, cl cluster, ns, app string) int {
	t.Helper()
	var list appsv1.ReplicaSetList
	must(t, cl.C.List(ctx(t), &list, client.InNamespace(ns), client.MatchingLabels{"app": app}))
	return len(list.Items)
}

// hpa attaches a CPU-utilization HPA (such workloads are hpa-coupled).
func hpa(t *testing.T, cl cluster, ns, name string, target int32) {
	t.Helper()
	min := int32(2)
	h := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: name},
			MinReplicas:    &min,
			MaxReplicas:    4,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
				Name: "cpu", Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &target}}}},
		},
	}
	must(t, cl.C.Create(ctx(t), h))
}

func toYAML(obj any) ([]byte, error) {
	b, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	// Drop empty status/creationTimestamp noise; Argo CD doesn't need it.
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "status")
	if md := asMap(m["metadata"]); md != nil {
		delete(md, "creationTimestamp")
	}
	if tpl := dig(m, "spec", "template", "metadata"); tpl != nil {
		delete(tpl, "creationTimestamp")
	}
	return yaml.Marshal(m)
}
