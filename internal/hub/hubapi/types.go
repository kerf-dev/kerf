// Package hubapi holds the Go types of the hub HTTP API (api/openapi/hub.yaml).
//
// The types are hand-written to mirror the OpenAPI schemas one to one; field names and JSON tags
// must match the contract. Agents (WO-06) may import this package to talk to the hub.
package hubapi

import (
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// Enumerations from the contract.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindCronJob     = "CronJob"

	ModeOff       = "off"
	ModeAudit     = "audit"
	ModeAutoPatch = "auto-patch"

	SourceGlobalDefault = "global-default"

	PhasePending = "Pending"
	PhaseBlocked = "Blocked"

	ProfileDev              = "dev"
	ProfileProduction       = "production"
	ProfileHighAvailability = "high-availability"

	CPULimitsKeep   = "keep"
	CPULimitsRemove = "remove"
	CPULimitsRatio  = "ratio"

	RoleAgent = "agent"
	RoleUI    = "ui"
	RoleAdmin = "admin"
	// RolePricing is the pricing service's token: metrics bindings and the datasource query proxy
	// only (D-026).
	RolePricing = "pricing"
)

// Risk flags (RiskFlag enum).
const (
	FlagThrottled     = "throttled"
	FlagOOMKilled     = "oom-killed"
	FlagRestarting    = "restarting"
	FlagHPACoupled    = "hpa-coupled"
	FlagSidecar       = "sidecar"
	FlagShortLived    = "short-lived"
	FlagLowConfidence = "low-confidence"
	// FlagJVMFixedHeap: JVM with a fixed -Xmx; memory is audit-only (WO-12).
	FlagJVMFixedHeap = "jvm-fixed-heap"
)

// AllRiskFlags lists the RiskFlag enum in contract order.
var AllRiskFlags = []string{FlagThrottled, FlagOOMKilled, FlagRestarting, FlagHPACoupled, FlagSidecar, FlagShortLived, FlagLowConfidence, FlagJVMFixedHeap}

// WorkloadKinds lists the WorkloadKind enum.
var WorkloadKinds = []string{KindDeployment, KindStatefulSet, KindDaemonSet, KindCronJob}

// Modes lists the Mode enum.
var Modes = []string{ModeOff, ModeAudit, ModeAutoPatch}

// Phases lists the Phase enum.
var Phases = []string{"Pending", "Applying", "Applied", "Blocked", "RolledBack", "Released", "Deferred"}

// EventTypes lists the EventType enum.
var EventTypes = []string{"applied", "skipped", "blocked", "fallback", "rolled_back", "released", "failed"}

// Strategies lists the Strategy enum.
var Strategies = []string{"in-place", "rollout", "deferred"}

// ModeSources lists the ModeSource enum.
var ModeSources = []string{"global-default", "cluster-default", "policy", "annotation"}

// ClusterNameRE is the ClusterName pattern.
var ClusterNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ResourceValues maps resource name to Kubernetes quantity. A missing key means "not set".
type ResourceValues struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// IsZero reports whether no value is set.
func (r ResourceValues) IsZero() bool { return r.CPU == "" && r.Memory == "" }

// Resources is requests and limits.
type Resources struct {
	Requests *ResourceValues `json:"requests,omitempty"`
	Limits   *ResourceValues `json:"limits,omitempty"`
}

// ContainerResources is the resources of one container.
type ContainerResources struct {
	Name     string          `json:"name"`
	Requests *ResourceValues `json:"requests,omitempty"`
	Limits   *ResourceValues `json:"limits,omitempty"`
}

// WorkloadRef identifies a workload inside a cluster.
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

// Savings is estimated monthly savings in USD.
type Savings struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Total  float64 `json:"total"`
}

// Add returns s + o.
func (s Savings) Add(o Savings) Savings {
	return Savings{CPU: s.CPU + o.CPU, Memory: s.Memory + o.Memory, Total: s.Total + o.Total}
}

// UsageStats are per-pod usage statistics over the window.
type UsageStats struct {
	P50 string `json:"p50,omitempty"`
	P95 string `json:"p95,omitempty"`
	P99 string `json:"p99,omitempty"`
	Max string `json:"max,omitempty"`
}

// ResourceExplanation explains one resource's recommendation.
type ResourceExplanation struct {
	Statistic string     `json:"statistic"`
	Observed  UsageStats `json:"observed"`
	Base      string     `json:"base,omitempty"`
	Margin    float64    `json:"margin"`
	RoundedTo string     `json:"roundedTo,omitempty"`
	LimitRule string     `json:"limitRule,omitempty"`
	Notes     []string   `json:"notes,omitempty"`
	// JVM is set on the memory explanation of JVM containers (WO-12).
	JVM *JVMExplanation `json:"jvm,omitempty"`
}

// ContainerExplanation explains why the values were recommended.
type ContainerExplanation struct {
	Profile           string               `json:"profile"`
	StrategyName      string               `json:"strategyName"`
	Window            string               `json:"window"`
	CPU               *ResourceExplanation `json:"cpu,omitempty"`
	Memory            *ResourceExplanation `json:"memory,omitempty"`
	ConfidenceFactors *ConfidenceFactors   `json:"confidenceFactors,omitempty"`
}

// ConfidenceFactor is one weighted term of a container's confidence.
type ConfidenceFactor struct {
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
}

// ConfidenceFactors explain a container's confidence: sum of score x weight (D-016).
type ConfidenceFactors struct {
	Coverage   ConfidenceFactor `json:"coverage"`
	Samples    ConfidenceFactor `json:"samples"`
	Variance   ConfidenceFactor `json:"variance"`
	Confidence float64          `json:"confidence"`
}

// ContainerRecommendation is the recommendation for one container.
type ContainerRecommendation struct {
	Name              string                `json:"name"`
	Init              bool                  `json:"init,omitempty"`
	Current           Resources             `json:"current"`
	Recommended       Resources             `json:"recommended"`
	RemoveLimits      []string              `json:"removeLimits,omitempty"`
	RiskFlags         []string              `json:"riskFlags,omitempty"`
	MonthlySavingsUSD *Savings              `json:"monthlySavingsUSD,omitempty"`
	Explanation       *ContainerExplanation `json:"explanation,omitempty"`
}

// DataCoverage describes how much usage data backs a recommendation.
type DataCoverage struct {
	Ratio       float64    `json:"ratio"`
	Window      string     `json:"window"`
	Covered     string     `json:"covered,omitempty"`
	FirstSample *time.Time `json:"firstSample,omitempty"`
	LastSample  *time.Time `json:"lastSample,omitempty"`
	Samples     int        `json:"samples"`
}

// WorkloadRecommendation is the latest recommendation for a workload.
type WorkloadRecommendation struct {
	ID                string                    `json:"id"`
	Workload          WorkloadRef               `json:"workload"`
	GeneratedAt       time.Time                 `json:"generatedAt"`
	StrategyName      string                    `json:"strategyName,omitempty"`
	Settings          ValueSettings             `json:"settings"`
	AverageReplicas   float64                   `json:"averageReplicas"`
	Containers        []ContainerRecommendation `json:"containers"`
	Confidence        float64                   `json:"confidence"`
	RiskFlags         []string                  `json:"riskFlags"`
	MonthlySavingsUSD Savings                   `json:"monthlySavingsUSD"`
	DataCoverage      DataCoverage              `json:"dataCoverage"`
	Pricing           *RecommendationPricing    `json:"pricing,omitempty"`
	// HPA is the HPA coordination (WO-11) of hpa-coupled workloads.
	HPA *HPACoordination `json:"hpa,omitempty"`
}

// Pricing sources (RecommendationPricing.source).
const (
	PricingSourceMetrics = "metrics"
	PricingSourceDefault = "default"
)

// RecommendationPricing are the rates used for a recommendation's savings (D-016).
type RecommendationPricing struct {
	Source           string  `json:"source"`
	CPUCoreHourUSD   float64 `json:"cpuCoreHourUSD"`
	MemoryGiBHourUSD float64 `json:"memoryGiBHourUSD"`
	// RateSources counts the cluster's priced nodes by where their rate came from (source metrics).
	RateSources []PricingRateSource `json:"rateSources,omitempty"`
	// CURPeriod is the usage the effective rates were computed from, when a node has source cur.
	CURPeriod *PricingPeriod `json:"curPeriod,omitempty"`
}

// Rate sources of the pricing service (label `source` of kerf_*_hourly_cost_usd).
const (
	RateSourceCUR    = "cur"
	RateSourceList   = "list"
	RateSourceSpot   = "spot"
	RateSourceStatic = "static"
)

// PricingRateSource is the number of nodes priced from one rate source.
type PricingRateSource struct {
	Source string `json:"source"`
	Nodes  int    `json:"nodes"`
}

// PricingPeriod bounds the usage effective rates were computed from.
type PricingPeriod struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// WithoutExplanations returns a copy whose containers carry no explanation.
func (r WorkloadRecommendation) WithoutExplanations() WorkloadRecommendation {
	cs := make([]ContainerRecommendation, len(r.Containers))
	for i, c := range r.Containers {
		c.Explanation = nil
		cs[i] = c
	}
	r.Containers = cs
	return r
}

// RecommendationList is the response of GET /v1/clusters/{cluster}/recommendations.
type RecommendationList struct {
	Cluster  string                   `json:"cluster"`
	Items    []WorkloadRecommendation `json:"items"`
	Continue string                   `json:"continue,omitempty"`
}

// EffectiveModes are the effective CPU and memory modes.
type EffectiveModes struct {
	CPU          string `json:"cpu"`
	Memory       string `json:"memory"`
	CPUSource    string `json:"cpuSource,omitempty"`
	MemorySource string `json:"memorySource,omitempty"`
}

// DefaultModes are the modes of a workload the agent never reported (D-005).
func DefaultModes() EffectiveModes {
	return EffectiveModes{CPU: ModeAudit, Memory: ModeAudit, CPUSource: SourceGlobalDefault, MemorySource: SourceGlobalDefault}
}

// WorkloadStatus is the status of one workload as reported by the agent.
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
	// JVM lists the JVM containers the agent detected (WO-12).
	JVM []JVMContainer `json:"jvm,omitempty"`
}

// WorkloadStatusBatch is the body of POST .../workload-status.
type WorkloadStatusBatch struct {
	AgentVersion string           `json:"agentVersion,omitempty"`
	Items        []WorkloadStatus `json:"items"`
	// ConfigVersion is the cluster config the agent last materialised (D-022).
	ConfigVersion string `json:"configVersion,omitempty"`
	// PolicyInventory is nil when unchanged; non-nil (possibly empty) is the full inventory.
	PolicyInventory []PolicyInventoryItem `json:"policyInventory,omitempty"`
	// Capabilities and Checks are the agent self-checks (D-025): nil = unchanged.
	Capabilities []AgentCapability `json:"capabilities,omitempty"`
	Checks       []ReadinessCheck  `json:"checks,omitempty"`
	// Components are the Kerf components the agent found in its cluster (WO-33, webhook pods):
	// nil = unchanged, empty = none.
	Components []AgentComponent `json:"components,omitzero"`
}

// ContainerChange is a change of one container's resources.
type ContainerChange struct {
	Name string     `json:"name"`
	From *Resources `json:"from,omitempty"`
	To   *Resources `json:"to,omitempty"`
}

// Event is an audit event sent by the agent.
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

// EventBatch is the body of POST .../events.
type EventBatch struct {
	Items []Event `json:"items"`
}

// BatchResult is the response of the batch endpoints.
type BatchResult struct {
	Accepted   int  `json:"accepted"`
	Duplicates *int `json:"duplicates,omitempty"`
}

// AuditEvent is a stored event.
type AuditEvent struct {
	Event
	Cluster    string    `json:"cluster"`
	ReceivedAt time.Time `json:"receivedAt"`
	WorkloadID string    `json:"workloadId,omitempty"`
}

// AuditEventList is the response of GET /v1/audit.
type AuditEventList struct {
	Items    []AuditEvent `json:"items"`
	Continue string       `json:"continue,omitempty"`
}

// ModeCounts counts workloads per mode.
type ModeCounts struct {
	Off       int `json:"off"`
	Audit     int `json:"audit"`
	AutoPatch int `json:"auto-patch"`
}

// Inc increments the counter of mode m.
func (c *ModeCounts) Inc(m string) {
	switch m {
	case ModeOff:
		c.Off++
	case ModeAutoPatch:
		c.AutoPatch++
	default:
		c.Audit++
	}
}

// ResourceTotals are sums over workloads.
type ResourceTotals struct {
	Requested   float64 `json:"requested"`
	Used        float64 `json:"used"`
	Recommended float64 `json:"recommended"`
}

// WorkloadsByMode counts workloads per CPU and memory mode.
type WorkloadsByMode struct {
	CPU    ModeCounts `json:"cpu"`
	Memory ModeCounts `json:"memory"`
}

// Summary holds totals over a set of workloads.
type Summary struct {
	Workloads            int             `json:"workloads"`
	WorkloadsByMode      WorkloadsByMode `json:"workloadsByMode"`
	WorkloadsByRiskFlag  map[string]int  `json:"workloadsByRiskFlag"`
	WorkloadsByPhase     map[string]int  `json:"workloadsByPhase,omitempty"`
	CPU                  ResourceTotals  `json:"cpu"`
	Memory               ResourceTotals  `json:"memory"`
	MonthlySavingsUSD    Savings         `json:"monthlySavingsUSD"`
	LastRecommenderRunAt *time.Time      `json:"lastRecommenderRunAt,omitempty"`
}

// ClusterSummary is a Summary for one cluster.
type ClusterSummary struct {
	Summary
	Cluster string `json:"cluster"`
}

// FleetSummary is the response of GET /v1/summary.
type FleetSummary struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Fleet       Summary          `json:"fleet"`
	Clusters    []ClusterSummary `json:"clusters"`
}

// ClusterCounts are per-cluster workload counts.
type ClusterCounts struct {
	Workloads          int `json:"workloads"`
	WithRecommendation int `json:"withRecommendation"`
	AutoPatch          int `json:"autoPatch"`
	Blocked            int `json:"blocked"`
}

// ClusterInfo describes a cluster.
type ClusterInfo struct {
	Name            string        `json:"name"`
	Description     string        `json:"description,omitempty"`
	CreatedAt       *time.Time    `json:"createdAt,omitempty"`
	LastHeartbeatAt *time.Time    `json:"lastHeartbeatAt,omitempty"`
	AgentVersion    string        `json:"agentVersion,omitempty"`
	AgentStatus     string        `json:"agentStatus"`
	Counts          ClusterCounts `json:"counts"`
	// Admin area (D-022).
	Environment  string           `json:"environment,omitempty"`
	Owner        string           `json:"owner,omitempty"`
	PolicySource string           `json:"policySource,omitempty"`
	Paused       *bool            `json:"paused,omitempty"`
	Automation   *Automation      `json:"automation,omitempty"`
	ConfigSync   *AgentConfigSync `json:"configSync,omitempty"`
	// Readiness (D-025).
	Readiness *ReadinessSummary `json:"readiness,omitempty"`
}

// ClusterList is the response of GET /v1/clusters.
type ClusterList struct {
	Items []ClusterInfo `json:"items"`
}

// WorkloadRow is one row of GET /v1/workloads.
type WorkloadRow struct {
	ID                string                    `json:"id"`
	Cluster           string                    `json:"cluster"`
	Namespace         string                    `json:"namespace"`
	Kind              string                    `json:"kind"`
	Name              string                    `json:"name"`
	Modes             EffectiveModes            `json:"modes"`
	Phase             string                    `json:"phase"`
	RecommendationID  string                    `json:"recommendationID,omitempty"`
	GeneratedAt       *time.Time                `json:"generatedAt,omitempty"`
	Containers        []ContainerRecommendation `json:"containers,omitempty"`
	Confidence        *float64                  `json:"confidence,omitempty"`
	RiskFlags         []string                  `json:"riskFlags,omitempty"`
	MonthlySavingsUSD *Savings                  `json:"monthlySavingsUSD,omitempty"`
}

// WorkloadList is the response of GET /v1/workloads.
type WorkloadList struct {
	Items    []WorkloadRow `json:"items"`
	Total    int           `json:"total"`
	Continue string        `json:"continue,omitempty"`
}

// FacetCount is one value of a facet.
type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// WorkloadFacets is the response of GET /v1/workloads/facets.
type WorkloadFacets struct {
	Total      int          `json:"total"`
	Clusters   []FacetCount `json:"clusters"`
	Namespaces []FacetCount `json:"namespaces"`
	Kinds      []FacetCount `json:"kinds"`
	Modes      []FacetCount `json:"modes"`
	RiskFlags  []FacetCount `json:"riskFlags"`
}

// WorkloadDetail is the response of GET /v1/workloads/{id}.
type WorkloadDetail struct {
	ID             string                  `json:"id"`
	Cluster        string                  `json:"cluster"`
	Namespace      string                  `json:"namespace"`
	Kind           string                  `json:"kind"`
	Name           string                  `json:"name"`
	Modes          EffectiveModes          `json:"modes"`
	Phase          string                  `json:"phase"`
	FirstSeenAt    *time.Time              `json:"firstSeenAt,omitempty"`
	LastSeenAt     *time.Time              `json:"lastSeenAt,omitempty"`
	Recommendation *WorkloadRecommendation `json:"recommendation,omitempty"`
	Status         *WorkloadStatus         `json:"status,omitempty"`
	// Approval is the latest approve-once grant (D-024).
	Approval *Approval `json:"approval,omitempty"`
	// Override is the workload's hub override policy (D-022).
	Override *AdminPolicy `json:"override,omitempty"`
}

// Point is [unix seconds, value].
type Point [2]float64

// ContainerSeries are the time series of one container.
type ContainerSeries struct {
	Name           string  `json:"name"`
	Usage          []Point `json:"usage"`
	UsageAvg       []Point `json:"usageAvg"`
	Request        []Point `json:"request"`
	Limit          []Point `json:"limit"`
	Recommendation []Point `json:"recommendation"`
}

// UsageSeries is the response of GET /v1/workloads/{id}/usage.
type UsageSeries struct {
	WorkloadID string            `json:"workloadId"`
	Resource   string            `json:"resource"`
	Unit       string            `json:"unit"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Step       string            `json:"step"`
	Containers []ContainerSeries `json:"containers"`
}

// WorkloadHistory is the response of GET /v1/workloads/{id}/history.
type WorkloadHistory struct {
	WorkloadID      string                   `json:"workloadId"`
	Recommendations []WorkloadRecommendation `json:"recommendations"`
	Events          []AuditEvent             `json:"events"`
	// ConfigChanges targeting this workload (override, approval), newest first.
	ConfigChanges []ConfigChange `json:"configChanges,omitempty"`
}

// SavingsPoint is one point of the savings trend.
type SavingsPoint struct {
	Time   time.Time `json:"time"`
	CPU    float64   `json:"cpu"`
	Memory float64   `json:"memory"`
	Total  float64   `json:"total"`
}

// SavingsSeries is one series of the savings trend.
type SavingsSeries struct {
	Cluster string         `json:"cluster,omitempty"`
	Points  []SavingsPoint `json:"points"`
}

// SavingsTrend is the response of GET /v1/savings/trend.
type SavingsTrend struct {
	From   time.Time       `json:"from"`
	To     time.Time       `json:"to"`
	Step   string          `json:"step"`
	Series []SavingsSeries `json:"series"`
}

// Problem is an RFC 9457 problem document.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

// WorkloadKey identifies a workload across the fleet.
type WorkloadKey struct {
	Cluster   string
	Namespace string
	Kind      string
	Name      string
}

// ID returns the workload id `<cluster>/<namespace>/<kind>/<name>`.
func (k WorkloadKey) ID() string {
	return k.Cluster + "/" + k.Namespace + "/" + k.Kind + "/" + k.Name
}

// Ref returns the in-cluster reference.
func (k WorkloadKey) Ref() WorkloadRef {
	return WorkloadRef{Namespace: k.Namespace, Kind: k.Kind, Name: k.Name}
}

// EscapedID returns the id URL-encoded as one path segment.
func (k WorkloadKey) EscapedID() string { return url.PathEscape(k.ID()) }

var workloadIDRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)/([^/]+)/(Deployment|StatefulSet|DaemonSet|CronJob)/([^/]+)$`)

// ParseWorkloadID parses `<cluster>/<namespace>/<kind>/<name>`.
func ParseWorkloadID(id string) (WorkloadKey, error) {
	m := workloadIDRE.FindStringSubmatch(id)
	if m == nil {
		return WorkloadKey{}, fmt.Errorf("invalid workload id %q: want <cluster>/<namespace>/<kind>/<name>", id)
	}
	return WorkloadKey{Cluster: m[1], Namespace: m[3], Kind: m[4], Name: m[5]}, nil
}

// ValidKind reports whether k is a WorkloadKind.
func ValidKind(k string) bool { return contains(WorkloadKinds, k) }

// ValidRiskFlag reports whether f is a RiskFlag.
func ValidRiskFlag(f string) bool { return contains(AllRiskFlags, f) }

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Contains reports whether list holds v.
func Contains(list []string, v string) bool { return contains(list, v) }

// RecomputeAccepted is the response of POST /v1/admin/recompute (D-019).
type RecomputeAccepted struct {
	RunID       string    `json:"runId"`
	Cluster     string    `json:"cluster,omitempty"`
	RequestedAt time.Time `json:"requestedAt"`
	Coalesced   bool      `json:"coalesced"`
}

// HPACoordination is WorkloadRecommendation.hpa (WO-11).
type HPACoordination struct {
	Name              string `json:"name"`
	Metric            string `json:"metric"`
	CurrentTarget     int    `json:"currentTarget"`
	RecommendedTarget int    `json:"recommendedTarget,omitempty"`
	MinReplicas       *int   `json:"minReplicas,omitempty"`
	MaxReplicas       *int   `json:"maxReplicas,omitempty"`
	SkipReason        string `json:"skipReason,omitempty"`
}

// HPA skip reasons (HPACoordination.skipReason).
const (
	HPASkipMaxReplicas      = "max-replicas-reached"
	HPASkipUnsupported      = "unsupported-metrics"
	HPASkipMissingRequest   = "missing-request"
	HPASkipTargetOutOfRange = "target-out-of-bounds"
)
