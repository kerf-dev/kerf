// Package hubclient is the agent side of the hub API (api/openapi/hub.yaml, tag "agent").
//
// The types mirror the OpenAPI schemas one to one; spec_test.go checks every struct against the
// spec (property names, required fields), so a contract change breaks the build of this package's
// tests instead of failing silently at runtime.
package hubclient

import (
	"time"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

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
	"RecommendationPricing":   RecommendationPricing{},
	"HPACoordination":         HPACoordination{},
	"RecommendationList":      RecommendationList{},
	"EffectiveModes":          EffectiveModes{},
	"WorkloadStatus":          WorkloadStatus{},
	"WorkloadStatusBatch":     WorkloadStatusBatch{},
	"JVMContainer":            JVMContainer{},
	"ContainerChange":         ContainerChange{},
	"Event":                   Event{},
	"EventBatch":              EventBatch{},
	"BatchResult":             BatchResult{},
	"Problem":                 Problem{},

	// Admin area (WO-19, D-022/D-024).
	"ClusterConfig":       ClusterConfig{},
	"ConfigPolicy":        ConfigPolicy{},
	"ConfigFlags":         ConfigFlags{},
	"ConfigApproval":      ConfigApproval{},
	"PolicyInventoryItem": PolicyInventoryItem{},

	// RightsizingPolicy spec as sent in ConfigPolicy.spec: the CRD types (api/v1alpha1).
	"RightsizingPolicySpec": v1alpha1.RightsizingPolicySpec{},
	"PolicySelector":        v1alpha1.PolicySelector{},
	"NamespacedWorkloadRef": v1alpha1.NamespacedWorkloadRef{},
	"CPUPolicy":             v1alpha1.CPUPolicy{},
	"MemoryPolicy":          v1alpha1.MemoryPolicy{},
	"ApplyPolicy":           v1alpha1.ApplyPolicy{},
	"TimeWindow":            v1alpha1.TimeWindow{},
	"Guardrails":            v1alpha1.Guardrails{},
	"RollbackPolicy":        v1alpha1.RollbackPolicy{},
	"HPAPolicy":             v1alpha1.HPAPolicy{},
	"HPATargetBounds":       v1alpha1.HPATargetBounds{},
	"JVMPolicy":             v1alpha1.JVMPolicy{},
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
	// Pricing are the rates behind MonthlySavingsUSD (D-016); informational, unused by the agent.
	Pricing *RecommendationPricing `json:"pricing,omitempty"`
	// HPA is the HPA coordination of hpa-coupled workloads (WO-11).
	HPA *HPACoordination `json:"hpa,omitempty"`
}

// HPACoordination is the hub's HPA target recommendation (WO-11). Without RecommendedTarget
// (SkipReason set) the requests of Metric must not change.
type HPACoordination struct {
	Name              string `json:"name"`
	Metric            string `json:"metric"`
	CurrentTarget     int32  `json:"currentTarget"`
	RecommendedTarget int32  `json:"recommendedTarget,omitempty"`
	MinReplicas       *int32 `json:"minReplicas,omitempty"`
	MaxReplicas       *int32 `json:"maxReplicas,omitempty"`
	SkipReason        string `json:"skipReason,omitempty"`
}

// RecommendationPricing are the hourly rates the hub used for savings (D-016).
type RecommendationPricing struct {
	Source           string  `json:"source"`
	CPUCoreHourUSD   float64 `json:"cpuCoreHourUSD"`
	MemoryGiBHourUSD float64 `json:"memoryGiBHourUSD"`
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
	Workload WorkloadRef `json:"workload"`
	Policy   string      `json:"policy,omitempty"`
	// Override is the hub workload-override policy layered on top of Policy (D-022).
	Override         string               `json:"override,omitempty"`
	Modes            EffectiveModes       `json:"modes"`
	Strategy         string               `json:"strategy,omitempty"`
	Settings         *ValueSettings       `json:"settings,omitempty"`
	Phase            string               `json:"phase"`
	Reason           string               `json:"reason,omitempty"`
	Message          string               `json:"message,omitempty"`
	RecommendationID string               `json:"recommendationID,omitempty"`
	Live             []ContainerResources `json:"live,omitempty"`
	AppliedAt        *time.Time           `json:"appliedAt,omitempty"`
	NextWindow       *time.Time           `json:"nextWindow,omitempty"`
	ObservedAt       *time.Time           `json:"observedAt,omitempty"`
	// JVM lists the detected JVM containers and their heap settings (WO-12).
	JVM []JVMContainer `json:"jvm,omitempty"`
}

// JVMContainer are the heap settings of one JVM container (WO-12). Empty means "not set".
type JVMContainer struct {
	Name             string   `json:"name"`
	DetectedBy       []string `json:"detectedBy"`
	MaxHeap          string   `json:"maxHeap,omitempty"`
	InitialHeap      string   `json:"initialHeap,omitempty"`
	MaxRAMPercentage float64  `json:"maxRAMPercentage,omitempty"`
	MaxRAM           string   `json:"maxRAM,omitempty"`
	Unresolved       []string `json:"unresolved,omitempty"`
}

// WorkloadStatusBatch is the body of POST workload-status.
type WorkloadStatusBatch struct {
	AgentVersion string           `json:"agentVersion,omitempty"`
	Items        []WorkloadStatus `json:"items"`
	// ConfigVersion is the ClusterConfig.configVersion the agent last materialised (D-022).
	ConfigVersion string `json:"configVersion,omitempty"`
	// PolicyInventory: nil = unchanged since the last report (omitted); non-nil, even empty =
	// the complete inventory. Send it with full batches.
	PolicyInventory []PolicyInventoryItem `json:"policyInventory,omitempty"`
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

// Admin area (WO-19, docs/contracts/admin.md).

// ClusterConfig is the response of GET /v1/clusters/{cluster}/config. Policies is the complete set
// of hub-managed policies: the agent makes the CRs labelled LabelManagedBy=ManagedByHub equal to
// it (create, update, delete) and never touches other CRs.
type ClusterConfig struct {
	Cluster       string           `json:"cluster"`
	ConfigVersion string           `json:"configVersion"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	PolicySource  string           `json:"policySource"`
	Policies      []ConfigPolicy   `json:"policies"`
	Flags         ConfigFlags      `json:"flags"`
	Approvals     []ConfigApproval `json:"approvals"`
}

// ConfigPolicy is one hub-managed policy. The CR gets name Name, label
// v1alpha1.LabelManagedBy=v1alpha1.ManagedByHub and annotations AnnotationHubPolicyID=ID,
// AnnotationHubVersion=Version, AnnotationHubPolicyType=Type. Validate Spec with
// api/v1alpha1/validate.HubPolicy before materialising it.
type ConfigPolicy struct {
	ID       string                         `json:"id"`
	Name     string                         `json:"name"`
	Type     string                         `json:"type"`
	Version  int                            `json:"version"`
	Priority int32                          `json:"priority"`
	Spec     v1alpha1.RightsizingPolicySpec `json:"spec"`
}

// ConfigFlags are the cluster flags. Paused behaves like the cluster-wide freeze (reason
// v1alpha1.ReasonPaused); the agent keeps the last value it received while the hub is unreachable
// and does not evaluate ResumeAt.
type ConfigFlags struct {
	Paused      bool       `json:"paused"`
	PauseScope  string     `json:"pauseScope,omitempty"`
	PauseReason string     `json:"pauseReason,omitempty"`
	PausedAt    *time.Time `json:"pausedAt,omitempty"`
	ResumeAt    *time.Time `json:"resumeAt,omitempty"`
}

// ConfigApproval is an approve-once grant (D-024): apply RecommendationID of Workload once, as if
// its audit resources were auto-patch, with all guardrails, if it is still the latest
// recommendation and ExpiresAt has not passed; then emit applied/ApprovedOnce with
// details approvalId and approvedBy.
type ConfigApproval struct {
	ID               string      `json:"id"`
	Workload         WorkloadRef `json:"workload"`
	RecommendationID string      `json:"recommendationID"`
	ExpiresAt        time.Time   `json:"expiresAt"`
	ApprovedBy       string      `json:"approvedBy"`
	ApprovedAt       time.Time   `json:"approvedAt"`
}

// PolicyInventoryItem is one RightsizingPolicy CR on the cluster (or a hub policy the agent could
// not materialise, Generation 0, Valid false).
type PolicyInventoryItem struct {
	Name             string                          `json:"name"`
	Source           string                          `json:"source"`
	Priority         int32                           `json:"priority"`
	Generation       int64                           `json:"generation"`
	Valid            bool                            `json:"valid"`
	Message          string                          `json:"message,omitempty"`
	HubPolicyID      string                          `json:"hubPolicyId,omitempty"`
	HubVersion       int                             `json:"hubVersion,omitempty"`
	HubPolicyType    string                          `json:"hubPolicyType,omitempty"`
	MatchedWorkloads *int32                          `json:"matchedWorkloads,omitempty"`
	Spec             *v1alpha1.RightsizingPolicySpec `json:"spec,omitempty"`
}

// Policy inventory sources (PolicyInventoryItem.source).
const (
	PolicySourceHub = "hub"
	PolicySourceGit = "git"
)

// Pause scopes (ConfigFlags.pauseScope).
const (
	PauseScopeCluster = "cluster"
	PauseScopeFleet   = "fleet"
)

// Event detail keys of the admin area (docs/contracts/events.md).
const (
	DetailApprovalID = "approvalId"
	DetailApprovedBy = "approvedBy"
	DetailPauseScope = "pauseScope"
)
