package hubapi

import "time"

// Realized savings (GET /v1/savings/realized, GET /v1/workloads/{id}/savings/realized,
// GET /v1/savings/realized/node-cost). Realized amounts are never added to the estimated
// Savings of the recommendations: they have their own types.

// Realized-savings bucket sizes.
const (
	RealizedStepDay   = "day"
	RealizedStepMonth = "month"
	NodeCostStepHour  = "hour"
	NodeCostStepDay   = "day"
	// RelationCorrelated is the only NodeCostTrend.relation: the node cost and the released
	// requests are drawn side by side; Kerf does not claim one caused the other.
	RelationCorrelated = "correlated"
)

// RealizedAmount is realized USD: released requests x running replicas x node rates of the time.
type RealizedAmount struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Total  float64 `json:"total"`
}

// ReleasedRequests are requests released by applied changes, times the replicas that ran.
type ReleasedRequests struct {
	CPUCores    float64 `json:"cpuCores"`
	MemoryBytes float64 `json:"memoryBytes"`
}

// RealizedPoint is one bucket of a realized series.
type RealizedPoint struct {
	Time   time.Time `json:"time"`
	CPU    float64   `json:"cpu"`
	Memory float64   `json:"memory"`
	Total  float64   `json:"total"`
	// Released is the average released requests over the bucket.
	Released ReleasedRequests `json:"released"`
}

// RealizedCoverage counts the workload-hours of the range by how they were computed.
type RealizedCoverage struct {
	WorkloadHours               int `json:"workloadHours"`
	ReplicasFromRecommendations int `json:"replicasFromRecommendations"`
	WithoutReplicas             int `json:"withoutReplicas"`
	DefaultPricing              int `json:"defaultPricing"`
}

// RealizedSavings is the realized-savings read of a scope (fleet, clusters, namespaces or one workload).
type RealizedSavings struct {
	Since           *time.Time       `json:"since,omitempty"`
	ComputedThrough *time.Time       `json:"computedThrough,omitempty"`
	From            time.Time        `json:"from"`
	To              time.Time        `json:"to"`
	Step            string           `json:"step"`
	Total           RealizedAmount   `json:"total"`
	InRange         RealizedAmount   `json:"inRange"`
	ReleasedNow     ReleasedRequests `json:"releasedNow"`
	Points          []RealizedPoint  `json:"points"`
	Coverage        RealizedCoverage `json:"coverage"`
}

// NodeCostPoint is one bucket of a cluster's node cost trend.
type NodeCostPoint struct {
	Time                 time.Time        `json:"time"`
	NodeCostUSDPerHour   *float64         `json:"nodeCostUSDPerHour,omitempty"`
	Nodes                *float64         `json:"nodes,omitempty"`
	CPURequestedRatio    *float64         `json:"cpuRequestedRatio,omitempty"`
	MemoryRequestedRatio *float64         `json:"memoryRequestedRatio,omitempty"`
	Released             ReleasedRequests `json:"released"`
}

// NodeCostTrend is a cluster's hourly node cost and requested/allocatable ratio next to the
// requests Kerf released.
type NodeCostTrend struct {
	Cluster         string          `json:"cluster"`
	From            time.Time       `json:"from"`
	To              time.Time       `json:"to"`
	Step            string          `json:"step"`
	Relation        string          `json:"relation"`
	ComputedThrough *time.Time      `json:"computedThrough,omitempty"`
	Points          []NodeCostPoint `json:"points"`
}
