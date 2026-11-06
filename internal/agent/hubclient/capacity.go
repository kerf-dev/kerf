package hubclient

import "time"

// Node capacity report (WorkloadStatusBatch.capacity): the mirror of the hub's CapacityReport.

// Node autoscaler kinds (NodeAutoscalerKind).
const (
	AutoscalerKarpenter = "karpenter"
	AutoscalerCA        = "cluster-autoscaler"
	AutoscalerNone      = "none"
)

// Reasons a pod blocks the removal of its node (CapacityPodReason).
const (
	PodReasonDoNotDisrupt     = "do-not-disrupt"
	PodReasonSafeToEvictFalse = "safe-to-evict-false"
	PodReasonLocalStorage     = "local-storage"
)

// CapacityReport is the agent's read-only snapshot of nodes, node pools and consolidation
// blockers (WorkloadStatusBatch.capacity; nil = unchanged).
type CapacityReport struct {
	ObservedAt  time.Time            `json:"observedAt"`
	Autoscalers []CapacityAutoscaler `json:"autoscalers,omitempty"`
	NodePools   []CapacityNodePool   `json:"nodePools"`
	Nodes       []CapacityNode       `json:"nodes"`
	PodGroups   []CapacityPodGroup   `json:"podGroups"`
	PDBs        []CapacityPDB        `json:"pdbs,omitempty"`
	Pods        []CapacityPod        `json:"pods,omitempty"`
	Truncated   bool                 `json:"truncated,omitempty"`
}

// CapacityObjectRef is a Kubernetes object; Namespace is empty for cluster-scoped objects.
type CapacityObjectRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// CapacityAutoscaler is a node autoscaler found in the cluster.
type CapacityAutoscaler struct {
	Kind          string             `json:"kind"`
	Object        *CapacityObjectRef `json:"object,omitempty"`
	Status        string             `json:"status,omitempty"`
	ScaleDown     string             `json:"scaleDown,omitempty"`
	LastProbeTime *time.Time         `json:"lastProbeTime,omitempty"`
}

// CapacityNodePool is a Karpenter NodePool, a Cluster Autoscaler node group or a node pool label.
type CapacityNodePool struct {
	Name                string                     `json:"name"`
	Autoscaler          string                     `json:"autoscaler"`
	Object              *CapacityObjectRef         `json:"object,omitempty"`
	MinNodes            *int                       `json:"minNodes,omitempty"`
	MaxNodes            *int                       `json:"maxNodes,omitempty"`
	InstanceTypes       []string                   `json:"instanceTypes,omitempty"`
	CapacityTypes       []string                   `json:"capacityTypes,omitempty"`
	ConsolidationPolicy string                     `json:"consolidationPolicy,omitempty"`
	ConsolidateAfter    string                     `json:"consolidateAfter,omitempty"`
	Budgets             []CapacityDisruptionBudget `json:"budgets,omitempty"`
}

// CapacityDisruptionBudget is a Karpenter disruption budget.
type CapacityDisruptionBudget struct {
	Nodes    string   `json:"nodes"`
	Schedule string   `json:"schedule,omitempty"`
	Duration string   `json:"duration,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}

// CapacityNode is one node.
type CapacityNode struct {
	Name              string         `json:"name"`
	NodePool          string         `json:"nodePool,omitempty"`
	InstanceType      string         `json:"instanceType,omitempty"`
	CapacityType      string         `json:"capacityType,omitempty"`
	Zone              string         `json:"zone,omitempty"`
	Allocatable       ResourceValues `json:"allocatable"`
	Unschedulable     bool           `json:"unschedulable,omitempty"`
	DoNotDisrupt      bool           `json:"doNotDisrupt,omitempty"`
	ScaleDownDisabled bool           `json:"scaleDownDisabled,omitempty"`
}

// CapacityPodGroup is Count scheduled pods of one owner, node pool and shape.
type CapacityPodGroup struct {
	Owner        *CapacityObjectRef   `json:"owner,omitempty"`
	Workload     *WorkloadRef         `json:"workload,omitempty"`
	NodePool     string               `json:"nodePool"`
	DaemonSet    bool                 `json:"daemonSet,omitempty"`
	Count        int                  `json:"count"`
	Containers   []ContainerResources `json:"containers"`
	InitRequests *ResourceValues      `json:"initRequests,omitempty"`
	Overhead     *ResourceValues      `json:"overhead,omitempty"`
}

// CapacityPDB is a PodDisruptionBudget that allows no disruption.
type CapacityPDB struct {
	Namespace          string       `json:"namespace"`
	Name               string       `json:"name"`
	DisruptionsAllowed int          `json:"disruptionsAllowed"`
	CurrentHealthy     int          `json:"currentHealthy,omitempty"`
	DesiredHealthy     int          `json:"desiredHealthy,omitempty"`
	ExpectedPods       int          `json:"expectedPods,omitempty"`
	Workload           *WorkloadRef `json:"workload,omitempty"`
}

// CapacityPod is a pod that blocks the removal of its node.
type CapacityPod struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Node      string       `json:"node,omitempty"`
	NodePool  string       `json:"nodePool,omitempty"`
	Workload  *WorkloadRef `json:"workload,omitempty"`
	Reasons   []string     `json:"reasons"`
}
