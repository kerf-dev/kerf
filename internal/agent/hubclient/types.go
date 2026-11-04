// Package hubclient is the agent side of the hub API (api/openapi/hub.yaml, tag "agent").
//
// The types mirror the OpenAPI schemas one to one; spec_test.go checks every struct against the
// spec (property names, required fields), so a contract change breaks the build of this package's
// tests instead of failing silently at runtime.
package hubclient

import "time"

// Schema names used by spec_test.go to map Go types to components/schemas entries.
var schemaTypes = map[string]any{
	"ResourceValues":          ResourceValues{},
	"Resources":               Resources{},
	"ContainerResources":      ContainerResources{},
	"WorkloadRef":             WorkloadRef{},
	"ValueSettings":           ValueSettings{},
	"ContainerRecommendation": ContainerRecommendation{},
	"Savings":                 Savings{},
	"DataCoverage":            DataCoverage{},
	"WorkloadRecommendation":  WorkloadRecommendation{},
	"RecommendationList":      RecommendationList{},
	"EffectiveModes":          EffectiveModes{},
	"WorkloadStatus":          WorkloadStatus{},
	"WorkloadStatusBatch":     WorkloadStatusBatch{},
	"ContainerChange":         ContainerChange{},
	"Event":                   Event{},
	"EventBatch":              EventBatch{},
	"BatchResult":             BatchResult{},
	"Problem":                 Problem{},
}

// ResourceValues holds quantities per resource; empty means "not set".
type ResourceValues struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// IsZero reports whether no value is set.
func (v ResourceValues) IsZero() bool { return v.CPU == "" && v.Memory == "" }

// Resources are requests and limits.
type Resources struct {
	Requests *ResourceValues `json:"requests,omitempty"`
	Limits   *ResourceValues `json:"limits,omitempty"`
}

// ContainerResources are the resources of one named container.
type ContainerResources struct {
	Name     string          `json:"name"`
	Requests *ResourceValues `json:"requests,omitempty"`
	Limits   *ResourceValues `json:"limits,omitempty"`
}

// WorkloadRef identifies a workload in a cluster.
type WorkloadRef struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// ValueSettings are the policy settings that change recommended values.
type ValueSettings struct {
	Profile                  string `json:"profile,omitempty"`
	CPULimits                string `json:"cpuLimits,omitempty"`
	CPULimitRatio            string `json:"cpuLimitRatio,omitempty"`
	MemoryLimitEqualsRequest *bool  `json:"memoryLimitEqualsRequest,omitempty"`
}

// ContainerRecommendation is the recommendation for one container.
type ContainerRecommendation struct {
	Name              string    `json:"name"`
	Init              bool      `json:"init,omitempty"`
	Current           Resources `json:"current"`
	Recommended       Resources `json:"recommended"`
	RemoveLimits      []string  `json:"removeLimits,omitempty"`
	RiskFlags         []string  `json:"riskFlags,omitempty"`
	MonthlySavingsUSD *Savings  `json:"monthlySavingsUSD,omitempty"`
	// Explanation is only sent on UI endpoints; the agent ignores it.
	Explanation map[string]any `json:"explanation,omitempty"`
}

// Savings are estimated monthly savings in USD.
type Savings struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Total  float64 `json:"total"`
}

// DataCoverage describes how much usage data a recommendation is based on.
type DataCoverage struct {
	Ratio       float64    `json:"ratio"`
	Window      string     `json:"window"`
	Covered     string     `json:"covered,omitempty"`
	FirstSample *time.Time `json:"firstSample,omitempty"`
	LastSample  *time.Time `json:"lastSample,omitempty"`
	Samples     *int64     `json:"samples,omitempty"`
}

// WorkloadRecommendation is the hub's latest recommendation for one workload.
type WorkloadRecommendation struct {
	ID                string                    `json:"id"`
	Workload          WorkloadRef               `json:"workload"`
	GeneratedAt       time.Time                 `json:"generatedAt"`
	StrategyName      string                    `json:"strategyName,omitempty"`
	Settings          ValueSettings             `json:"settings"`
	AverageReplicas   *float64                  `json:"averageReplicas,omitempty"`
	Containers        []ContainerRecommendation `json:"containers"`
	Confidence        float64                   `json:"confidence"`
	RiskFlags         []string                  `json:"riskFlags"`
	MonthlySavingsUSD Savings                   `json:"monthlySavingsUSD"`
	DataCoverage      DataCoverage              `json:"dataCoverage"`
}

// RecommendationList is one page of recommendations.
type RecommendationList struct {
	Cluster  string                   `json:"cluster"`
	Items    []WorkloadRecommendation `json:"items"`
	Continue string                   `json:"continue,omitempty"`
}

// EffectiveModes are the effective modes and their sources.
type EffectiveModes struct {
	CPU          string `json:"cpu"`
	Memory       string `json:"memory"`
	CPUSource    string `json:"cpuSource,omitempty"`
	MemorySource string `json:"memorySource,omitempty"`
}

// WorkloadStatus is what the agent reports per workload.
type WorkloadStatus struct {
	Workload         WorkloadRef          `json:"workload"`
	Policy           string               `json:"policy,omitempty"`
	Modes            EffectiveModes       `json:"modes"`
	Strategy         string               `json:"strategy,omitempty"`
	Settings         *ValueSettings       `json:"settings,omitempty"`
	Phase            string               `json:"phase"`
	Reason           string               `json:"reason,omitempty"`
	Message          string               `json:"message,omitempty"`
	RecommendationID string               `json:"recommendationID,omitempty"`
	Live             []ContainerResources `json:"live,omitempty"`
	AppliedAt        *time.Time           `json:"appliedAt,omitempty"`
	ObservedAt       *time.Time           `json:"observedAt,omitempty"`
}

// WorkloadStatusBatch is the body of POST workload-status.
type WorkloadStatusBatch struct {
	AgentVersion string           `json:"agentVersion,omitempty"`
	Items        []WorkloadStatus `json:"items"`
}

// ContainerChange is the before/after of one container in an event.
type ContainerChange struct {
	Name string     `json:"name"`
	From *Resources `json:"from,omitempty"`
	To   *Resources `json:"to,omitempty"`
}

// Event is one audit event (docs/contracts/events.md).
type Event struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Time             time.Time         `json:"time"`
	Workload         WorkloadRef       `json:"workload"`
	RecommendationID string            `json:"recommendationID,omitempty"`
	Strategy         string            `json:"strategy,omitempty"`
	Resources        []string          `json:"resources,omitempty"`
	Containers       []ContainerChange `json:"containers,omitempty"`
	Reason           string            `json:"reason"`
	Message          string            `json:"message,omitempty"`
	Details          map[string]string `json:"details,omitempty"`
}

// EventBatch is the body of POST events.
type EventBatch struct {
	Items []Event `json:"items"`
}

// BatchResult is the response of the batch endpoints.
type BatchResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates,omitempty"`
}

// Problem is an RFC 9457 error body.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

// Event types (docs/contracts/events.md).
const (
	EventApplied    = "applied"
	EventSkipped    = "skipped"
	EventBlocked    = "blocked"
	EventFallback   = "fallback"
	EventRolledBack = "rolled_back"
	EventReleased   = "released"
	EventFailed     = "failed"
)

// MaxBatch is the maximum number of items per batch request.
const MaxBatch = 1000
