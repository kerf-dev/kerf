package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Unset fields in a RightsizingPolicy mean "inherit from the next lower precedence level"
// (cluster default, then global default; D-005). The CRD therefore has no OpenAPI defaults:
// the agent fills in defaults during policy resolution. Documented global defaults are
// listed on each field as "Global default".

// RightsizingPolicySpec selects workloads and configures how they are rightsized.
type RightsizingPolicySpec struct {
	// Selector chooses the workloads this policy applies to. An empty selector matches every workload.
	// +optional
	Selector PolicySelector `json:"selector,omitempty"`

	// Priority decides between several matching policies: the highest priority wins; ties are
	// broken by policy name in ascending lexical order. Only the winning policy applies (fields are
	// not merged across policies).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
	Priority int32 `json:"priority,omitempty"`

	// Profile selects the recommender profile. Global default: production.
	// +optional
	Profile Profile `json:"profile,omitempty"`

	// CPU configures CPU handling.
	// +optional
	CPU *CPUPolicy `json:"cpu,omitempty"`

	// Memory configures memory handling.
	// +optional
	Memory *MemoryPolicy `json:"memory,omitempty"`

	// Apply configures how changes are applied.
	// +optional
	Apply *ApplyPolicy `json:"apply,omitempty"`

	// Guardrails limit what the agent may change.
	// +optional
	Guardrails *Guardrails `json:"guardrails,omitempty"`

	// HPA configures coordination with HorizontalPodAutoscalers. Without coordination, workloads with
	// a CPU/memory-utilization HPA stay audit-only (D-011).
	// +optional
	HPA *HPAPolicy `json:"hpa,omitempty"`

	// JVM configures JVM detection.
	// +optional
	JVM *JVMPolicy `json:"jvm,omitempty"`
}

// PolicySelector selects workloads. All set criteria must match (AND).
type PolicySelector struct {
	// Namespaces is a list of namespace globs (path.Match syntax, e.g. "team-*").
	// A workload matches if its namespace matches any entry. Empty means all namespaces.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	Namespaces []string `json:"namespaces,omitempty"`

	// ExcludeNamespaces is a list of namespace globs that never match, even if listed in namespaces.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// MatchLabels must all be present with equal values on the workload object (not the pod template).
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// Kinds restricts the workload kinds. Empty means all supported kinds.
	// +optional
	// +kubebuilder:validation:MaxItems=4
	// +listType=set
	Kinds []WorkloadKind `json:"kinds,omitempty"`

	// Workloads restricts the policy to the listed workloads: a workload matches if it equals any
	// entry. Empty means no restriction. Like every criterion it is combined with the others (AND).
	// The hub uses it for UI workload overrides (D-022). Agents older than this field ignore it,
	// so the hub also sets namespaces and kinds to narrow such a policy.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	Workloads []NamespacedWorkloadRef `json:"workloads,omitempty"`
}

// NamespacedWorkloadRef identifies one workload in the cluster.
type NamespacedWorkloadRef struct {
	// Kind of the workload.
	// +required
	Kind WorkloadKind `json:"kind"`
	// Namespace of the workload.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// Name of the workload.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// CPUPolicy configures CPU handling.
// +kubebuilder:validation:XValidation:rule="!has(self.limits) || self.limits != 'ratio' || has(self.limitRatio)",message="limitRatio is required when limits is ratio"
// +kubebuilder:validation:XValidation:rule="!has(self.limitRatio) || (has(self.limits) && self.limits == 'ratio')",message="limitRatio is only allowed when limits is ratio"
type CPUPolicy struct {
	// Mode for CPU. Global default: audit.
	// +optional
	Mode Mode `json:"mode,omitempty"`
	// Limits says how CPU limits are recommended. Global default: keep.
	// +optional
	Limits CPULimitPolicy `json:"limits,omitempty"`
	// LimitRatio is the CPU limit as a multiple of the request, as a decimal string >= 1 (e.g. "2" or "1.5").
	// Required when limits is ratio.
	// +optional
	// +kubebuilder:validation:Pattern=`^([1-9][0-9]*)(\.[0-9]{1,3})?$`
	LimitRatio string `json:"limitRatio,omitempty"`
}

// MemoryPolicy configures memory handling.
type MemoryPolicy struct {
	// Mode for memory. Global default: audit.
	// +optional
	Mode Mode `json:"mode,omitempty"`
	// LimitEqualsRequest sets the memory limit equal to the memory request. Global default: true.
	// +optional
	LimitEqualsRequest *bool `json:"limitEqualsRequest,omitempty"`
}

// ApplyPolicy configures how changes are applied (D-007).
type ApplyPolicy struct {
	// Strategy used to apply changes. Global default: in-place.
	// +optional
	Strategy Strategy `json:"strategy,omitempty"`
	// FallbackAfter is how long an in-place resize may stay Deferred before the agent falls back
	// to rollout. Infeasible resizes fall back immediately. Global default: 10m.
	// +optional
	FallbackAfter *metav1.Duration `json:"fallbackAfter,omitempty"`
	// MaintenanceWindows are the only times the deferred strategy may apply changes.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	MaintenanceWindows []TimeWindow `json:"maintenanceWindows,omitempty"`
	// FreezeWindows are times in which no strategy may apply changes, and no field ownership is
	// released. Rollbacks (health watch) are still allowed. A cluster-wide freeze configured on the
	// agent applies in addition to these and cannot be overridden by a policy.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	FreezeWindows []TimeWindow `json:"freezeWindows,omitempty"`
}

// TimeWindow is either recurring (schedule + duration) or one-off (start + end).
// +kubebuilder:validation:XValidation:rule="has(self.schedule) ? (has(self.duration) && !has(self.start) && !has(self.end)) : (has(self.start) && has(self.end) && !has(self.duration))",message="set either schedule and duration, or start and end"
type TimeWindow struct {
	// Schedule is a standard 5-field cron expression marking when a recurring window opens.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Schedule string `json:"schedule,omitempty"`
	// Duration of a recurring window.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`
	// Start of a one-off window.
	// +optional
	Start *metav1.Time `json:"start,omitempty"`
	// End of a one-off window.
	// +optional
	End *metav1.Time `json:"end,omitempty"`
	// TimeZone (IANA name) the schedule is evaluated in. Default: UTC.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	TimeZone string `json:"timeZone,omitempty"`
}

// Guardrails limit what the agent may change.
type Guardrails struct {
	// LearningPeriod is the minimum data coverage before a workload may be changed. Global default: 168h.
	// +optional
	LearningPeriod *metav1.Duration `json:"learningPeriod,omitempty"`
	// MinConfidence is the minimum recommendation confidence (0-1, decimal string) for a change.
	// Global default: "0.7".
	// +optional
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]{1,3})?|1(\.0{1,3})?)$`
	MinConfidence string `json:"minConfidence,omitempty"`
	// MaxChangePercent caps a single decrease step, in percent of the current value. Increases are
	// never capped. Global default: 50.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxChangePercent *int32 `json:"maxChangePercent,omitempty"`
	// MinCPU is the lowest CPU request the agent will set. Global default: 10m.
	// +optional
	MinCPU *resource.Quantity `json:"minCPU,omitempty"`
	// MinMemory is the lowest memory request the agent will set. Global default: 32Mi.
	// +optional
	MinMemory *resource.Quantity `json:"minMemory,omitempty"`
	// Cooldown is the minimum time between two changes, and after a rollback the time during which
	// further decreases of the rolled-back resource are blocked. Global default: 24h.
	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`
	// Rollback configures the health watch after each change.
	// +optional
	Rollback *RollbackPolicy `json:"rollback,omitempty"`
}

// RollbackPolicy configures the health watch after each change and the thresholds that trigger
// a rollback. Counts are per workload over watchDuration.
type RollbackPolicy struct {
	// WatchDuration is how long a workload is watched after a change. Global default: 30m.
	// +optional
	WatchDuration *metav1.Duration `json:"watchDuration,omitempty"`
	// OOMKills is the number of OOM kills that triggers a rollback. Global default: 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	OOMKills *int32 `json:"oomKills,omitempty"`
	// Restarts is the number of container restarts (any reason) that triggers a rollback. Global default: 3.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Restarts *int32 `json:"restarts,omitempty"`
	// ThrottlingRatio (0-1, decimal string) is the CFS throttled-periods ratio that triggers a CPU
	// rollback. Global default: "0.25".
	// +optional
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]{1,3})?|1(\.0{1,3})?)$`
	ThrottlingRatio string `json:"throttlingRatio,omitempty"`
}

// HPAPolicy configures coordination with HorizontalPodAutoscalers.
type HPAPolicy struct {
	// Coordinate changes requests and the HPA utilization target in one step. Global default: false.
	// +optional
	Coordinate *bool `json:"coordinate,omitempty"`
	// TargetBounds limits the HPA utilization target the agent may set.
	// +optional
	TargetBounds *HPATargetBounds `json:"targetBounds,omitempty"`
}

// HPATargetBounds are bounds for an HPA average utilization target, in percent.
// +kubebuilder:validation:XValidation:rule="!has(self.minUtilization) || !has(self.maxUtilization) || self.minUtilization <= self.maxUtilization",message="minUtilization must be <= maxUtilization"
type HPATargetBounds struct {
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MinUtilization *int32 `json:"minUtilization,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxUtilization *int32 `json:"maxUtilization,omitempty"`
}

// JVMPolicy configures JVM detection.
type JVMPolicy struct {
	// Detect JVM containers and treat their memory as heap-bound (memory changes need
	// RestartContainer or rollout, D-007). Global default: true.
	// +optional
	Detect *bool `json:"detect,omitempty"`
}

// RightsizingPolicyStatus is the observed state of a RightsizingPolicy.
type RightsizingPolicyStatus struct {
	// ObservedGeneration is the generation last processed by the agent.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// MatchedWorkloads is the number of workloads for which this policy is the winning policy.
	// +optional
	MatchedWorkloads int32 `json:"matchedWorkloads,omitempty"`
	// Conditions: Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RightsizingPolicy configures rightsizing for the workloads it selects. Cluster-scoped.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=rsp,categories=kerf
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.spec.cpu.mode`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.memory.mode`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.apply.strategy`
// +kubebuilder:printcolumn:name="Matched",type=integer,JSONPath=`.status.matchedWorkloads`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RightsizingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec RightsizingPolicySpec `json:"spec,omitempty"`
	// +optional
	Status RightsizingPolicyStatus `json:"status,omitempty"`
}

// RightsizingPolicyList contains a list of RightsizingPolicy.
//
// +kubebuilder:object:root=true
type RightsizingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RightsizingPolicy `json:"items"`
}
