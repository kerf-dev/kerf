package capacity

import (
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// Karpenter v1 API (read only).
var (
	NodePoolGVK      = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePool"}
	NodePoolListGVK  = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePoolList"}
	NodeClaimListGVK = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodeClaimList"}
)

// NodePool is what the report needs of a Karpenter NodePool.
type NodePool struct {
	Name                string
	ConsolidationPolicy string
	ConsolidateAfter    string
	Budgets             []hubclient.CapacityDisruptionBudget
	InstanceTypes       []string
	CapacityTypes       []string
	// Replicas is spec.replicas of a static NodePool (nil = dynamic).
	Replicas *int
}

// Report returns the pool as reported to the hub.
func (p NodePool) Report() hubclient.CapacityNodePool {
	return hubclient.CapacityNodePool{
		Name: p.Name, Autoscaler: hubclient.AutoscalerKarpenter,
		Object:              &hubclient.CapacityObjectRef{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: p.Name},
		MinNodes:            p.Replicas,
		InstanceTypes:       p.InstanceTypes,
		CapacityTypes:       p.CapacityTypes,
		ConsolidationPolicy: p.ConsolidationPolicy,
		ConsolidateAfter:    p.ConsolidateAfter,
		Budgets:             p.Budgets,
	}
}

// ParseNodePool reads a karpenter.sh/v1 NodePool.
func ParseNodePool(u *unstructured.Unstructured) NodePool {
	p := NodePool{Name: u.GetName()}
	p.ConsolidationPolicy, _, _ = unstructured.NestedString(u.Object, "spec", "disruption", "consolidationPolicy")
	p.ConsolidateAfter = nestedScalar(u.Object, "spec", "disruption", "consolidateAfter")
	budgets, _, _ := unstructured.NestedSlice(u.Object, "spec", "disruption", "budgets")
	for _, b := range budgets {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		bud := hubclient.CapacityDisruptionBudget{Nodes: nestedScalar(m, "nodes")}
		bud.Schedule, _, _ = unstructured.NestedString(m, "schedule")
		bud.Duration = nestedScalar(m, "duration")
		bud.Reasons, _, _ = unstructured.NestedStringSlice(m, "reasons")
		if bud.Nodes == "" {
			bud.Nodes = "10%" // Karpenter's default
		}
		p.Budgets = append(p.Budgets, bud)
	}
	reqs, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "requirements")
	for _, r := range reqs {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		key, _, _ := unstructured.NestedString(m, "key")
		op, _, _ := unstructured.NestedString(m, "operator")
		vals, _, _ := unstructured.NestedStringSlice(m, "values")
		if op != "In" {
			continue
		}
		switch key {
		case LabelInstanceType:
			p.InstanceTypes = vals
		case LabelCapacityType:
			p.CapacityTypes = vals
		}
	}
	if v, ok, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "replicas"); ok {
		if n, ok := asInt(v); ok {
			p.Replicas = &n
		}
	}
	return p
}

// NodeClaim is what the report needs of a Karpenter NodeClaim.
type NodeClaim struct {
	Name     string
	NodePool string
	NodeName string
}

// ParseNodeClaim reads a karpenter.sh/v1 NodeClaim.
func ParseNodeClaim(u *unstructured.Unstructured) NodeClaim {
	c := NodeClaim{Name: u.GetName(), NodePool: u.GetLabels()[LabelKarpenterNodePool]}
	c.NodeName, _, _ = unstructured.NestedString(u.Object, "status", "nodeName")
	return c
}

// nestedScalar returns a string, integer or float field as a string.
func nestedScalar(obj map[string]any, fields ...string) string {
	v, ok, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case int:
		return x, true
	}
	return 0, false
}
