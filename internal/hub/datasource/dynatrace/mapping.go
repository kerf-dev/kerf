package dynatrace

import "github.com/kerf-dev/kerf/internal/hub/hubapi"

// Dynatrace metric keys and dimensions Kerf reads.
const (
	KeyCPUUsage          = "dt.kubernetes.container.cpu_usage"
	KeyMemoryWorkingSet  = "dt.kubernetes.container.memory_working_set"
	KeyRequestsCPU       = "dt.kubernetes.container.requests_cpu"
	KeyRequestsMemory    = "dt.kubernetes.container.requests_memory"
	KeyLimitsCPU         = "dt.kubernetes.container.limits_cpu"
	KeyLimitsMemory      = "dt.kubernetes.container.limits_memory"
	KeyThrottlingRatio   = "dt.containers.cpu.throttling_ratio"
	KeyRestarts          = "dt.kubernetes.container.restarts"
	KeyOOMKills          = "dt.kubernetes.container.oom_kills"
	KeyNodeCPUAlloc      = "dt.kubernetes.node.cpu_allocatable"
	KeyNodeMemoryAlloc   = "dt.kubernetes.node.memory_allocatable"
	SmartscapeNodeLabels = "smartscapeNodes K8S_NODE labels"

	DimCluster   = "k8s.cluster.name"
	DimNamespace = "k8s.namespace.name"
	DimKind      = "k8s.workload.kind"
	DimWorkload  = "k8s.workload.name"
	DimPod       = "k8s.pod.name"
	DimContainer = "k8s.container.name"
	DimNode      = "k8s.node.name"
)

// containerDims are the dimensions of one container series.
var containerDims = []string{DimNamespace, DimKind, DimWorkload, DimPod, DimContainer}

// workloadContainerDims group container series by workload and container (pods aggregated).
var workloadContainerDims = []string{DimNamespace, DimKind, DimWorkload, DimContainer}

// Input maps one series Kerf needs (by its Prometheus name) to where a Dynatrace environment
// holds it.
type Input struct {
	// Input is the Prometheus series (or family) Kerf reads elsewhere.
	Input string
	// Source names the Dynatrace metric keys or field; "" = no equivalent.
	Source string
	// Keys are the metric keys whose series are counted (all must have series to be found);
	// empty with a Source = a check of its own (node labels, workload dimensions).
	Keys    []string
	UsedFor string
	Note    string
}

// Inputs is the mapping table, in the order the Test result and the docs show it.
var Inputs = []Input{
	{Input: "container_cpu_usage_seconds_total", Source: KeyCPUUsage, Keys: []string{KeyCPUUsage}, UsedFor: "CPU usage",
		Note: "a gauge (no rate needed); converted from the unit the API reports (millicores) to cores"},
	{Input: "container_memory_working_set_bytes", Source: KeyMemoryWorkingSet, Keys: []string{KeyMemoryWorkingSet}, UsedFor: "memory usage and running pods"},
	{Input: "kube_pod_container_resource_requests", Source: KeyRequestsCPU + ", " + KeyRequestsMemory, Keys: []string{KeyRequestsCPU, KeyRequestsMemory}, UsedFor: "current requests and savings"},
	{Input: "kube_pod_container_resource_limits", Source: KeyLimitsCPU + ", " + KeyLimitsMemory, Keys: []string{KeyLimitsCPU, KeyLimitsMemory}, UsedFor: "current limits",
		Note: "no series when no container sets a limit"},
	{Input: "kube_pod_owner, kube_replicaset_owner, kube_job_owner", Source: DimKind + ", " + DimWorkload + " dimensions", UsedFor: "the workload of each container",
		Note: "Dynatrace resolves the workload itself (deployment, statefulset, daemonset, cronjob)"},
	{Input: "container_cpu_cfs_throttled_periods_total, container_cpu_cfs_periods_total", Source: KeyThrottlingRatio, Keys: []string{KeyThrottlingRatio}, UsedFor: "throttling guardrail",
		Note: "a derived ratio: Dynatrace has no CFS period counters; recommendations say so"},
	{Input: "kube_pod_container_status_restarts_total", Source: KeyRestarts, Keys: []string{KeyRestarts}, UsedFor: "restart guardrail"},
	{Input: "container_oom_events_total", Source: KeyOOMKills, Keys: []string{KeyOOMKills}, UsedFor: "OOM guardrail",
		Note: "may have no series while no container was OOM-killed"},
	{Input: "kube_node_labels", Source: SmartscapeNodeLabels, UsedFor: "node pricing (instance type, capacity type, zone, region)",
		Note: "read from Smartscape, not a metric; karpenter.sh/capacity-type and eks.amazonaws.com/capacityType are both read"},
	{Input: "kube_node_status_allocatable", Source: KeyNodeCPUAlloc + ", " + KeyNodeMemoryAlloc, Keys: []string{KeyNodeCPUAlloc, KeyNodeMemoryAlloc}, UsedFor: "cost split per vCPU and GiB"},
	{Input: "kube_horizontalpodautoscaler_*", UsedFor: "HPA coupling", Note: "Dynatrace has no HPA metrics: HPA coupling is not detected"},
	{Input: "kube_pod_init_container_*", UsedFor: "init and native sidecar containers", Note: "init containers are not told apart from app containers"},
	{Input: "kube_pod_container_info", UsedFor: "container images (JVM heap detection)", Note: "images are not read: JVM heap flags are not detected"},
	{Input: "DCGM_FI_DEV_*", UsedFor: "GPU usage", Note: "GPU metrics are not read from Dynatrace"},
}

// metricStatus is the API form of one mapped input with its series count.
func metricStatus(in Input, series int, found bool) hubapi.DatasourceMetricStatus {
	st := hubapi.DatasourceMetricStatus{Input: in.Input, SourceMetric: in.Source, UsedFor: in.UsedFor, Note: in.Note, Series: series}
	switch {
	case in.Source == "":
		st.Status = hubapi.MetricNotAvailable
	case found:
		st.Status = hubapi.MetricFound
	default:
		st.Status = hubapi.MetricMissing
	}
	return st
}
