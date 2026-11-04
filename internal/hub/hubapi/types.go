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
)

// AllRiskFlags lists the RiskFlag enum in contract order.
var AllRiskFlags = []string{FlagThrottled, FlagOOMKilled, FlagRestarting, FlagHPACoupled, FlagSidecar, FlagShortLived, FlagLowConfidence}

// WorkloadKinds lists the WorkloadKind enum.
var WorkloadKinds = []string{KindDeployment, KindStatefulSet, KindDaemonSet, KindCronJob}

// Modes lists the Mode enum.
var Modes = []string{ModeOff, ModeAudit, ModeAutoPatch}

// Phases lists the Phase enum.
var Phases = []string{"Pending", "Applying", "Applied", "Blocked", "RolledBack", "Released"}

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
}

// ContainerExplanation explains why the values were recommended.
type ContainerExplanation struct {
	Profile      string               `json:"profile"`
	StrategyName string               `json:"strategyName"`
	Window       string               `json:"window"`
	CPU          *ResourceExplanation `json:"cpu,omitempty"`
	Memory       *ResourceExplanation `json:"memory,omitempty"`
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

// WorkloadStatusBatch is the body of POST .../workload-status.
type WorkloadStatusBatch struct {
	AgentVersion string           `json:"agentVersion,omitempty"`
	Items        []WorkloadStatus `json:"items"`
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
