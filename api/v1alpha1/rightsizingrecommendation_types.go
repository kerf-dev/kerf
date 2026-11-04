package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase is the lifecycle phase of a RightsizingRecommendation.
// +kubebuilder:validation:Enum=Pending;Applying;Applied;Blocked;RolledBack;Released
type Phase string

const (
	// PhasePending: recommendation received, not acted on yet (or mode is audit).
	PhasePending Phase = "Pending"
	// PhaseApplying: a change is in progress (resize submitted / rollout progressing).
	PhaseApplying Phase = "Applying"
	// PhaseApplied: the recommended values are live. The webhook only injects in this phase.
	PhaseApplied Phase = "Applied"
	// PhaseBlocked: a guardrail prevents the change; see the Blocked condition for the reason.
	PhaseBlocked Phase = "Blocked"
	// PhaseRolledBack: the health watch reverted the change (WO-08).
	PhaseRolledBack Phase = "RolledBack"
	// PhaseReleased: the workload left auto-patch and field ownership was released (D-006).
	PhaseReleased Phase = "Released"
)

// Condition types on RightsizingRecommendation.status.conditions.
const (
	// ConditionPolicyResolved is True when the effective policy was resolved.
	ConditionPolicyResolved = "PolicyResolved"
	// ConditionApplied is True when the recommended values are live.
	ConditionApplied = "Applied"
	// ConditionBlocked is True when a guardrail blocks the change. Reason is one of the Reason* constants.
	ConditionBlocked = "Blocked"
	// ConditionHealthWatch is True while the health watch after a change is running (WO-08).
	ConditionHealthWatch = "HealthWatch"
)

// ContainerRecommendation is the recommended resources for one container.
type ContainerRecommendation struct {
	ContainerResources `json:",inline"`
	// RemoveLimits lists resources whose limit is to be removed (e.g. cpu with limits: remove).
	// +optional
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=cpu;memory
	// +listType=set
	RemoveLimits []corev1.ResourceName `json:"removeLimits,omitempty"`
}

// RecommendationSource identifies the hub recommendation a RightsizingRecommendation was built from.
type RecommendationSource struct {
	// RecommendationID is the hub's recommendation id (hub API: WorkloadRecommendation.id).
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	RecommendationID string `json:"recommendationID"`
	// GeneratedAt is when the hub generated the recommendation.
	// +required
	GeneratedAt metav1.Time `json:"generatedAt"`
	// Confidence (0-1, decimal string) as reported by the hub. Informational.
	// +optional
	// +kubebuilder:validation:MaxLength=8
	Confidence string `json:"confidence,omitempty"`
	// RiskFlags as reported by the hub. Informational.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=set
	RiskFlags []RiskFlag `json:"riskFlags,omitempty"`
}

// RightsizingRecommendationSpec is the change the agent wants for one workload.
// Written only by the agent.
type RightsizingRecommendationSpec struct {
	// TargetRef is the workload, in the same namespace.
	// +required
	TargetRef TargetRef `json:"targetRef"`
	// Containers are the recommended values, after guardrails (clamping, floors) were applied.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=name
	Containers []ContainerRecommendation `json:"containers"`
	// Strategy currently in effect. After an in-place fallback the agent sets this to rollout,
	// which also stops the webhook from injecting values.
	// +required
	Strategy Strategy `json:"strategy"`
	// Source is the hub recommendation this was built from.
	// +required
	Source RecommendationSource `json:"source"`
}

// EffectivePolicy is the result of policy resolution for a workload (D-005).
type EffectivePolicy struct {
	// Policy is the name of the winning RightsizingPolicy, empty if none matched.
	// +optional
	Policy string `json:"policy,omitempty"`
	// CPUMode is the effective CPU mode.
	// +required
	CPUMode Mode `json:"cpuMode"`
	// CPUModeSource is the precedence level CPUMode came from.
	// +required
	CPUModeSource ModeSource `json:"cpuModeSource"`
	// MemoryMode is the effective memory mode.
	// +required
	MemoryMode Mode `json:"memoryMode"`
	// MemoryModeSource is the precedence level MemoryMode came from.
	// +required
	MemoryModeSource ModeSource `json:"memoryModeSource"`
	// Strategy is the effective configured strategy (before any fallback).
	// +required
	Strategy Strategy `json:"strategy"`
	// Profile is the effective recommender profile.
	// +required
	Profile Profile `json:"profile"`
}

// RightsizingRecommendationStatus is the observed state of a RightsizingRecommendation.
type RightsizingRecommendationStatus struct {
	// ObservedGeneration is the spec generation the status refers to.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase of the recommendation.
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// Effective is the resolved policy for the workload.
	// +optional
	Effective *EffectivePolicy `json:"effective,omitempty"`
	// Previous holds the values that were live before the most recent apply. Used by rollback.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=name
	Previous []ContainerResources `json:"previous,omitempty"`
	// AppliedAt is when the most recent change became live.
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
	// AppliedRecommendationID is the hub recommendation id that is live.
	// +optional
	AppliedRecommendationID string `json:"appliedRecommendationID,omitempty"`
	// RolledBackAt is when the most recent rollback happened (cooldown for decreases starts here).
	// +optional
	RolledBackAt *metav1.Time `json:"rolledBackAt,omitempty"`
	// Conditions: PolicyResolved, Applied, Blocked, HealthWatch.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RightsizingRecommendation is the agent's record of the change for one workload. One per
// workload, in the workload's namespace, written only by the agent.
//
// Naming: by convention metadata.name equals the workload name; if two workloads of different
// kinds share a name, the later one is named "<name>-<lowercase kind>". Consumers (webhook,
// health watch) must therefore look recommendations up by spec.targetRef (cache index
// IndexTargetRef), never by name.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rsr,categories=resource-optimizer
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.targetRef.kind`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.strategy`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.status.effective.cpuMode`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.status.effective.memoryMode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RightsizingRecommendation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec RightsizingRecommendationSpec `json:"spec"`
	// +optional
	Status RightsizingRecommendationStatus `json:"status,omitempty"`
}

// RightsizingRecommendationList contains a list of RightsizingRecommendation.
//
// +kubebuilder:object:root=true
type RightsizingRecommendationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RightsizingRecommendation `json:"items"`
}
