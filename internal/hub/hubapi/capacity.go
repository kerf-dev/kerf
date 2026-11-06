package hubapi

import "time"

// Node autoscaler kinds (NodeAutoscalerKind).
const (
	AutoscalerKarpenter = "karpenter"
	AutoscalerCA        = "cluster-autoscaler"
	AutoscalerNone      = "none"
)

// NodeAutoscalerKinds lists the NodeAutoscalerKind enum.
var NodeAutoscalerKinds = []string{AutoscalerKarpenter, AutoscalerCA, AutoscalerNone}

// Reasons a pod blocks the removal of its node (CapacityPodReason).
const (
	PodReasonDoNotDisrupt     = "do-not-disrupt"
	PodReasonSafeToEvictFalse = "safe-to-evict-false"
	PodReasonLocalStorage     = "local-storage"
)

// CapacityPodReasons lists the CapacityPodReason enum.
var CapacityPodReasons = []string{PodReasonDoNotDisrupt, PodReasonSafeToEvictFalse, PodReasonLocalStorage}

// Consolidation blocker types (BlockerType).
const (
	BlockerPDB                 = "pdb-zero-disruptions"
	BlockerDoNotDisruptPod     = "do-not-disrupt-pod"
	BlockerDoNotDisruptNode    = "do-not-disrupt-node"
	BlockerSafeToEvictFalse    = "safe-to-evict-false"
	BlockerLocalStorage        = "local-storage"
	BlockerNodePoolMinimum     = "node-pool-minimum"
	BlockerConsolidationPolicy = "consolidation-policy"
	BlockerBudgetZero          = "disruption-budget-zero"
	BlockerScaleDownDisabled   = "scale-down-disabled"
)

// BlockerTypes lists the BlockerType enum.
var BlockerTypes = []string{
	BlockerPDB, BlockerDoNotDisruptPod, BlockerDoNotDisruptNode, BlockerSafeToEvictFalse, BlockerLocalStorage,
	BlockerNodePoolMinimum, BlockerConsolidationPolicy, BlockerBudgetZero, BlockerScaleDownDisabled,
}

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

// ClusterCapacity is the response of GET /v1/clusters/{cluster}/capacity.
type ClusterCapacity struct {
	Cluster         string                 `json:"cluster"`
	ReportedAt      *time.Time             `json:"reportedAt,omitempty"`
	ObservedAt      *time.Time             `json:"observedAt,omitempty"`
	Autoscalers     []CapacityAutoscaler   `json:"autoscalers"`
	Totals          CapacityTotals         `json:"totals"`
	Estimate        *NodeEstimate          `json:"estimate,omitempty"`
	Pools           []CapacityPoolEstimate `json:"pools"`
	Blockers        []ConsolidationBlocker `json:"blockers"`
	BlockersOmitted int                    `json:"blockersOmitted,omitempty"`
	Truncated       bool                   `json:"truncated,omitempty"`
}

// CapacityAmounts are CPU cores and memory bytes.
type CapacityAmounts struct {
	CPUCores    float64 `json:"cpuCores"`
	MemoryBytes float64 `json:"memoryBytes"`
}

// CapacityTotals are cluster-wide totals.
type CapacityTotals struct {
	Nodes          int             `json:"nodes"`
	Allocatable    CapacityAmounts `json:"allocatable"`
	Requested      CapacityAmounts `json:"requested"`
	RequestedAfter CapacityAmounts `json:"requestedAfter"`
}

// Estimate methods and price sources.
const (
	EstimateFFD       = "first-fit-decreasing"
	PriceSourceMetric = "metrics"
	PriceSourceRates  = "rates"
	PriceSourceMixed  = "mixed"
	PriceSourceNone   = "none"
)

// NodeEstimate is the cluster-wide node estimate.
type NodeEstimate struct {
	Method              string  `json:"method"`
	MethodURL           string  `json:"methodURL"`
	CurrentNodes        int     `json:"currentNodes"`
	EstimatedNodes      int     `json:"estimatedNodes"`
	CurrentMonthlyUSD   float64 `json:"currentMonthlyUSD"`
	EstimatedMonthlyUSD float64 `json:"estimatedMonthlyUSD"`
	SavingsMonthlyUSD   float64 `json:"savingsMonthlyUSD"`
	Recommendations     int     `json:"recommendations"`
	PriceSource         string  `json:"priceSource,omitempty"`
}

// CapacityPoolEstimate is the estimate of one node pool.
type CapacityPoolEstimate struct {
	Name                string                 `json:"name"`
	Autoscaler          string                 `json:"autoscaler"`
	Object              *CapacityObjectRef     `json:"object,omitempty"`
	Nodes               int                    `json:"nodes"`
	EstimatedNodes      int                    `json:"estimatedNodes"`
	MinNodes            *int                   `json:"minNodes,omitempty"`
	Allocatable         CapacityAmounts        `json:"allocatable"`
	Requested           CapacityAmounts        `json:"requested"`
	RequestedAfter      CapacityAmounts        `json:"requestedAfter"`
	DaemonSetOverhead   *CapacityAmounts       `json:"daemonSetOverhead,omitempty"`
	CurrentMonthlyUSD   float64                `json:"currentMonthlyUSD,omitempty"`
	EstimatedMonthlyUSD float64                `json:"estimatedMonthlyUSD,omitempty"`
	InstanceTypes       []CapacityInstanceType `json:"instanceTypes"`
	Unplaced            int                    `json:"unplaced,omitempty"`
	ConsolidationPolicy string                 `json:"consolidationPolicy,omitempty"`
}

// CapacityInstanceType is one instance type of a pool.
type CapacityInstanceType struct {
	InstanceType   string          `json:"instanceType"`
	CapacityType   string          `json:"capacityType,omitempty"`
	Nodes          int             `json:"nodes"`
	EstimatedNodes int             `json:"estimatedNodes"`
	Allocatable    CapacityAmounts `json:"allocatable"`
	HourlyUSD      float64         `json:"hourlyUSD,omitempty"`
	PriceSource    string          `json:"priceSource,omitempty"`
}

// ConsolidationBlocker is something that likely stops the node autoscaler from removing nodes.
type ConsolidationBlocker struct {
	Type       string            `json:"type"`
	Object     CapacityObjectRef `json:"object"`
	NodePool   string            `json:"nodePool,omitempty"`
	WorkloadID string            `json:"workloadId,omitempty"`
	Message    string            `json:"message"`
	Fix        string            `json:"fix"`
}
