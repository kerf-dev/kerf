package capacity

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// MaxBlockers caps the blockers listed; the rest are counted.
const MaxBlockers = 200

// consolidationReasons are the Karpenter disruption reasons that are consolidation.
var consolidationReasons = []string{"Underutilized", "Empty"}

// Blockers lists what likely stops the node autoscaler from removing nodes. needed is the packed
// node count per pool (Result.Needed); workloadID returns the Kerf workload ID of a workload
// the hub knows ("" = unknown). The list is ordered by type, then object; it is capped at
// MaxBlockers and the number left out is returned.
func Blockers(rep hubapi.CapacityReport, needed map[string]int, workloadID func(hubapi.WorkloadRef) string) ([]hubapi.ConsolidationBlocker, int) {
	out := []hubapi.ConsolidationBlocker{}
	wid := func(w *hubapi.WorkloadRef) string {
		if w == nil || workloadID == nil {
			return ""
		}
		return workloadID(*w)
	}
	poolKind := map[string]string{}
	for _, p := range rep.NodePools {
		poolKind[p.Name] = p.Autoscaler
	}
	present := map[string]bool{}
	for _, a := range rep.Autoscalers {
		present[a.Kind] = true
	}
	for _, k := range poolKind {
		present[k] = true
	}
	// applies reports whether an annotation of the given autoscaler matters on a pool: the pool's
	// own autoscaler, or, for nodes without a known autoscaler, whether that autoscaler runs in
	// the cluster at all.
	applies := func(pool, kind string) bool {
		switch k := poolKind[pool]; k {
		case hubapi.AutoscalerKarpenter, hubapi.AutoscalerCA:
			return k == kind
		}
		return present[kind]
	}

	for _, b := range rep.PDBs {
		out = append(out, hubapi.ConsolidationBlocker{
			Type:       hubapi.BlockerPDB,
			Object:     hubapi.CapacityObjectRef{APIVersion: "policy/v1", Kind: "PodDisruptionBudget", Namespace: b.Namespace, Name: b.Name},
			WorkloadID: wid(b.Workload),
			Message: fmt.Sprintf("Allows no disruption now (%d of %d pods healthy, %d needed): nodes running these pods cannot be drained.",
				b.CurrentHealthy, b.ExpectedPods, b.DesiredHealthy),
			Fix: "Lower minAvailable or raise maxUnavailable so one pod can be evicted, or add a replica.",
		})
	}
	for _, p := range rep.Pods {
		obj := hubapi.CapacityObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: p.Namespace, Name: p.Name}
		on := "its node"
		if p.Node != "" {
			on = "node " + p.Node
		}
		for _, r := range p.Reasons {
			b := hubapi.ConsolidationBlocker{Object: obj, NodePool: p.NodePool, WorkloadID: wid(p.Workload)}
			switch r {
			case hubapi.PodReasonDoNotDisrupt:
				if !applies(p.NodePool, hubapi.AutoscalerKarpenter) {
					continue
				}
				b.Type = hubapi.BlockerDoNotDisruptPod
				b.Message = "Has karpenter.sh/do-not-disrupt=true: Karpenter does not consolidate " + on + " while the pod runs."
				b.Fix = "Remove the annotation, or keep it only on pods that must finish (e.g. long jobs)."
			case hubapi.PodReasonSafeToEvictFalse:
				if !applies(p.NodePool, hubapi.AutoscalerCA) {
					continue
				}
				b.Type = hubapi.BlockerSafeToEvictFalse
				b.Message = "Has cluster-autoscaler.kubernetes.io/safe-to-evict=false: the Cluster Autoscaler does not remove " + on + "."
				b.Fix = "Remove the annotation if the pod can be rescheduled."
			case hubapi.PodReasonLocalStorage:
				// Only the Cluster Autoscaler keeps nodes for local storage; Karpenter evicts these pods.
				if !applies(p.NodePool, hubapi.AutoscalerCA) {
					continue
				}
				b.Type = hubapi.BlockerLocalStorage
				b.Message = "Uses local storage (emptyDir or hostPath): the Cluster Autoscaler does not remove " + on + "."
				b.Fix = "Annotate the pod with cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes listing the volumes, or safe-to-evict=true if the data can be lost."
			default:
				continue
			}
			out = append(out, b)
		}
	}
	for _, n := range rep.Nodes {
		obj := hubapi.CapacityObjectRef{APIVersion: "v1", Kind: "Node", Name: n.Name}
		if n.DoNotDisrupt && applies(n.NodePool, hubapi.AutoscalerKarpenter) {
			out = append(out, hubapi.ConsolidationBlocker{
				Type: hubapi.BlockerDoNotDisruptNode, Object: obj, NodePool: n.NodePool,
				Message: "Has karpenter.sh/do-not-disrupt=true: Karpenter never consolidates this node.",
				Fix:     "Remove the annotation from the node: kubectl annotate node " + n.Name + " karpenter.sh/do-not-disrupt-",
			})
		}
		if n.ScaleDownDisabled && applies(n.NodePool, hubapi.AutoscalerCA) {
			out = append(out, hubapi.ConsolidationBlocker{
				Type: hubapi.BlockerScaleDownDisabled, Object: obj, NodePool: n.NodePool,
				Message: "Has cluster-autoscaler.kubernetes.io/scale-down-disabled=true: the Cluster Autoscaler never removes this node.",
				Fix:     "Remove the annotation: kubectl annotate node " + n.Name + " cluster-autoscaler.kubernetes.io/scale-down-disabled-",
			})
		}
	}
	nodesIn := map[string]int{}
	for _, n := range rep.Nodes {
		nodesIn[n.NodePool]++
	}
	for _, p := range rep.NodePools {
		if nodesIn[p.Name] == 0 {
			continue
		}
		obj := poolObject(p)
		if p.Autoscaler == hubapi.AutoscalerKarpenter {
			if p.ConsolidationPolicy == "WhenEmpty" {
				out = append(out, hubapi.ConsolidationBlocker{
					Type: hubapi.BlockerConsolidationPolicy, Object: obj, NodePool: p.Name,
					Message: "consolidationPolicy is WhenEmpty: Karpenter removes only empty nodes, so underused nodes stay.",
					Fix:     "Set spec.disruption.consolidationPolicy: WhenEmptyOrUnderutilized.",
				})
			}
			for _, b := range p.Budgets {
				if !zeroBudget(b.Nodes) || !blocksConsolidation(b.Reasons) {
					continue
				}
				msg := "A disruption budget allows 0 nodes: Karpenter does not consolidate this pool."
				if b.Schedule != "" {
					msg = fmt.Sprintf("A disruption budget allows 0 nodes on schedule %q for %s: Karpenter does not consolidate this pool then.", b.Schedule, orDash(b.Duration))
				}
				out = append(out, hubapi.ConsolidationBlocker{
					Type: hubapi.BlockerBudgetZero, Object: obj, NodePool: p.Name, Message: msg,
					Fix: "Raise spec.disruption.budgets[].nodes (e.g. \"10%\") or narrow its schedule or reasons.",
				})
			}
		}
		if p.MinNodes != nil {
			if need, ok := needed[p.Name]; ok && *p.MinNodes > need {
				what := "node group"
				fix := "Lower the node group's minimum size if the extra nodes are not needed."
				if p.Autoscaler == hubapi.AutoscalerKarpenter {
					what, fix = "static NodePool", "Lower spec.replicas if the extra nodes are not needed."
				}
				out = append(out, hubapi.ConsolidationBlocker{
					Type: hubapi.BlockerNodePoolMinimum, Object: obj, NodePool: p.Name,
					Message: fmt.Sprintf("The %s keeps at least %d nodes; the estimate needs %d.", what, *p.MinNodes, need),
					Fix:     fix,
				})
			}
		}
	}

	order := map[string]int{}
	for i, t := range hubapi.BlockerTypes {
		order[t] = i
	}
	slices.SortStableFunc(out, func(a, b hubapi.ConsolidationBlocker) int {
		if d := order[a.Type] - order[b.Type]; d != 0 {
			return d
		}
		return strings.Compare(objKey(a.Object), objKey(b.Object))
	})
	if len(out) > MaxBlockers {
		return out[:MaxBlockers], len(out) - MaxBlockers
	}
	return out, 0
}

func poolObject(p hubapi.CapacityNodePool) hubapi.CapacityObjectRef {
	if p.Object != nil {
		return *p.Object
	}
	switch p.Autoscaler {
	case hubapi.AutoscalerKarpenter:
		return hubapi.CapacityObjectRef{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: p.Name}
	case hubapi.AutoscalerCA:
		return hubapi.CapacityObjectRef{Kind: "NodeGroup", Name: p.Name}
	}
	return hubapi.CapacityObjectRef{Kind: "NodePool", Name: p.Name}
}

func zeroBudget(nodes string) bool {
	n := strings.TrimSpace(nodes)
	return n == "0" || n == "0%"
}

func blocksConsolidation(reasons []string) bool {
	if len(reasons) == 0 {
		return true
	}
	for _, r := range reasons {
		if slices.Contains(consolidationReasons, r) {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func objKey(o hubapi.CapacityObjectRef) string { return o.Namespace + "/" + o.Name }
