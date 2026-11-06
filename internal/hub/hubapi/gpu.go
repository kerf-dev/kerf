package hubapi

// GPU statuses (GPUUsage.Status and the `gpu` filter of GET /v1/workloads).
const (
	GPUIdle      = "idle"
	GPUUnderUsed = "under-used"
	GPUOK        = "ok"
	GPUNoData    = "no-data"
	// GPUAny is a filter value only: every workload that requests GPUs.
	GPUAny = "any"
)

// GPUStatuses lists the GPUUsage.Status enum.
var GPUStatuses = []string{GPUIdle, GPUUnderUsed, GPUOK, GPUNoData}

// GPUFilterValues lists the values of the `gpu` filter.
var GPUFilterValues = []string{GPUAny, GPUIdle, GPUUnderUsed, GPUOK, GPUNoData}

// GPU suggestion kinds (GPUSuggestion.Kind).
const (
	GPUSuggestTimeSlicing = "time-slicing"
	GPUSuggestMIG         = "mig"
	GPUSuggestSmallerGPU  = "smaller-gpu"
	GPUSuggestScaleToZero = "scale-to-zero"
)

// GPUSuggestionKinds lists the GPUSuggestion.Kind enum.
var GPUSuggestionKinds = []string{GPUSuggestTimeSlicing, GPUSuggestMIG, GPUSuggestSmallerGPU, GPUSuggestScaleToZero}

// ValidGPUFilter reports whether v is a value of the `gpu` filter.
func ValidGPUFilter(v string) bool { return contains(GPUFilterValues, v) }

// GPUUsage is what the hub knows about the GPUs of a workload: utilisation and memory from the
// NVIDIA DCGM exporter, the cost of the GPUs and what to do about idle or under-used ones.
// Visibility only: nothing is changed automatically.
type GPUUsage struct {
	Status string `json:"status"`
	// GPUsPerPod is the `nvidia.com/gpu` requested by one pod; GPUs and Pods are the totals now.
	GPUsPerPod int `json:"gpusPerPod"`
	GPUs       int `json:"gpus"`
	Pods       int `json:"pods"`
	// Model is the DCGM `modelName` label, e.g. "NVIDIA A10G".
	Model string `json:"model,omitempty"`
	// Window is the period the statistics cover, e.g. "168h0m0s".
	Window string `json:"window,omitempty"`
	// Samples is the number of steps with utilisation data in the window.
	Samples int `json:"samples"`
	// UtilizationP95 and UtilizationAvg are the GPU utilisation in percent (0-100), averaged over
	// the workload's GPUs at every step.
	UtilizationP95 *float64 `json:"utilizationP95,omitempty"`
	UtilizationAvg *float64 `json:"utilizationAvg,omitempty"`
	// MemoryUsedP95Bytes is the 95th percentile of the framebuffer used on the busiest GPU of a
	// pod; MemoryTotalBytes is the framebuffer size of one GPU.
	MemoryUsedP95Bytes *float64 `json:"memoryUsedP95Bytes,omitempty"`
	MemoryTotalBytes   *float64 `json:"memoryTotalBytes,omitempty"`
	// MemoryUtilizationP95 is MemoryUsedP95Bytes / MemoryTotalBytes in percent.
	MemoryUtilizationP95 *float64 `json:"memoryUtilizationP95,omitempty"`
	// Priced is false when no node of the workload's pods has a GPU price; the costs are then 0.
	Priced bool `json:"priced"`
	// MonthlyCostUSD is the cost of the GPUs the workload holds now: the node price times the
	// workload's share of the node's GPUs. PerPodMonthlyCostUSD is the same for one pod.
	MonthlyCostUSD       float64 `json:"monthlyCostUSD"`
	PerPodMonthlyCostUSD float64 `json:"perPodMonthlyCostUSD"`
	// UnusedMonthlyCostUSD is MonthlyCostUSD times the share of the GPU time not used.
	UnusedMonthlyCostUSD float64         `json:"unusedMonthlyCostUSD"`
	Suggestions          []GPUSuggestion `json:"suggestions"`
}

// GPUSuggestion is a direction for an idle or under-used GPU workload.
type GPUSuggestion struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// TargetModel is set on smaller-gpu: a GPU type the memory fits.
	TargetModel string `json:"targetModel,omitempty"`
	// MonthlySavingsUSD is set when it is known: scale-to-zero saves the whole cost.
	MonthlySavingsUSD *float64 `json:"monthlySavingsUSD,omitempty"`
}
