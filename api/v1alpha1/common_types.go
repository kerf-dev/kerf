package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// Mode controls what resource-optimizer does for one resource (CPU or memory) of a workload (D-005).
// +kubebuilder:validation:Enum=off;audit;auto-patch
type Mode string

const (
	// ModeOff: no recommendation is acted on or reported as actionable.
	ModeOff Mode = "off"
	// ModeAudit: recommendations are computed and reported, nothing is changed. Global default.
	ModeAudit Mode = "audit"
	// ModeAutoPatch: the agent applies recommendations, subject to guardrails.
	ModeAutoPatch Mode = "auto-patch"

	// DefaultMode is the global default mode (lowest precedence, D-005).
	DefaultMode = ModeAudit
)

// ModeSource says which precedence level produced an effective mode (D-005).
// +kubebuilder:validation:Enum=global-default;cluster-default;policy;annotation
type ModeSource string

const (
	ModeSourceGlobalDefault  ModeSource = "global-default"
	ModeSourceClusterDefault ModeSource = "cluster-default"
	ModeSourcePolicy         ModeSource = "policy"
	ModeSourceAnnotation     ModeSource = "annotation"
)

// Strategy is how a change is applied to a workload (D-007).
// +kubebuilder:validation:Enum=in-place;rollout;deferred
type Strategy string

const (
	// StrategyInPlace resizes running pods via the resize subresource; the webhook covers new pods.
	StrategyInPlace Strategy = "in-place"
	// StrategyRollout patches the pod template (rolling restart).
	StrategyRollout Strategy = "rollout"
	// StrategyDeferred patches only inside maintenance windows (phase 3, WO-10).
	StrategyDeferred Strategy = "deferred"

	// DefaultStrategy is used when no policy or annotation sets one.
	DefaultStrategy = StrategyInPlace
)

// WorkloadKind is a workload kind resource-optimizer manages.
// +kubebuilder:validation:Enum=Deployment;StatefulSet;DaemonSet;CronJob
type WorkloadKind string

const (
	KindDeployment  WorkloadKind = "Deployment"
	KindStatefulSet WorkloadKind = "StatefulSet"
	KindDaemonSet   WorkloadKind = "DaemonSet"
	KindCronJob     WorkloadKind = "CronJob"
)

// Profile selects the recommender's window, percentiles, headroom and buffer (WO-04).
// +kubebuilder:validation:Enum=dev;production;high-availability
type Profile string

const (
	ProfileDev              Profile = "dev"
	ProfileProduction       Profile = "production"
	ProfileHighAvailability Profile = "high-availability"

	// DefaultProfile is used when no policy sets one.
	DefaultProfile = ProfileProduction
)

// CPULimitPolicy says how CPU limits are recommended.
// +kubebuilder:validation:Enum=keep;remove;ratio
type CPULimitPolicy string

const (
	// CPULimitKeep leaves the current CPU limit unchanged. Default.
	CPULimitKeep CPULimitPolicy = "keep"
	// CPULimitRemove removes the CPU limit.
	CPULimitRemove CPULimitPolicy = "remove"
	// CPULimitRatio sets the CPU limit to request x limitRatio.
	CPULimitRatio CPULimitPolicy = "ratio"
)

// RiskFlag is a property of a recommendation the hub computed (WO-04). Values match the hub API.
type RiskFlag string

const (
	RiskThrottled     RiskFlag = "throttled"
	RiskOOMKilled     RiskFlag = "oom-killed"
	RiskRestarting    RiskFlag = "restarting"
	RiskHPACoupled    RiskFlag = "hpa-coupled"
	RiskSidecar       RiskFlag = "sidecar"
	RiskShortLived    RiskFlag = "short-lived"
	RiskLowConfidence RiskFlag = "low-confidence"
)

// TargetRef identifies the workload a RightsizingRecommendation belongs to.
// The workload is always in the same namespace as the RightsizingRecommendation.
type TargetRef struct {
	// APIVersion of the workload, e.g. apps/v1 or batch/v1.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// Kind of the workload.
	// +required
	Kind WorkloadKind `json:"kind"`
	// Name of the workload.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ContainerResources are the requests and limits of one container. Only cpu and memory are used.
type ContainerResources struct {
	// Name of the container (regular or init/sidecar container).
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Requests for cpu and/or memory. A missing key means "unchanged / not set".
	// +optional
	// +kubebuilder:validation:MaxProperties=2
	// +kubebuilder:validation:XValidation:rule="self.all(k, k == 'cpu' || k == 'memory')",message="only cpu and memory are supported"
	Requests corev1.ResourceList `json:"requests,omitempty"`
	// Limits for cpu and/or memory. A missing key means "unchanged / not set".
	// +optional
	// +kubebuilder:validation:MaxProperties=2
	// +kubebuilder:validation:XValidation:rule="self.all(k, k == 'cpu' || k == 'memory')",message="only cpu and memory are supported"
	Limits corev1.ResourceList `json:"limits,omitempty"`
}
