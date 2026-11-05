// Package recommender computes CPU and memory recommendations for workloads from usage statistics.
//
// The package is split in three layers:
//   - Source (prom.go): reads inventory and per-container usage statistics from Prometheus or
//     VictoriaMetrics with PromQL only (D-003).
//   - Strategy (strategy.go, percentile.go): turns usage statistics into unrounded per-pod requests.
//   - Recommend (recommend.go): applies risk rules, rounding, limit policy, confidence and savings,
//     and produces the hub API shape.
package recommender

import (
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Stats are statistics of a per-step usage series (per-pod maximum across the workload's pods).
type Stats struct {
	// Samples is the number of steps with data.
	Samples int
	// First and Last are the first and last step with data; zero when Samples is 0.
	First, Last time.Time
	Mean        float64
	Stddev      float64
	Max         float64
	// Quantiles maps a quantile (0.5, 0.95, ...) to its value.
	Quantiles map[float64]float64
}

// Q returns quantile q; ok is false if it was not computed.
func (s Stats) Q(q float64) (float64, bool) {
	v, ok := s.Quantiles[q]
	return v, ok
}

// Empty reports whether there is no data.
func (s Stats) Empty() bool { return s.Samples == 0 }

// Current are a container's current requests and limits in cores and bytes. 0 means "not set".
type Current struct {
	CPURequest, CPULimit       float64
	MemoryRequest, MemoryLimit float64
}

// ContainerData is everything the recommender knows about one container of a workload.
type ContainerData struct {
	Name string
	// Init is true for init containers that run alongside the app (native sidecars).
	Init bool
	// CPU and Memory are usage statistics over the profile window, in cores and bytes.
	CPU, Memory Stats
	// MemoryPeak is the maximum working set over the profile's peak window.
	MemoryPeak float64
	// ThrottleRatio is the mean CFS throttling ratio over the risk window (0-1).
	ThrottleRatio float64
	// OOMKilled is true if the container was OOM-killed within the risk window.
	OOMKilled bool
	// Restarts is the number of restarts within the risk window (summed over pods).
	Restarts float64
	Current  Current
	// Used24h is the 24 h average usage summed over all pods (cores, bytes).
	Used24hCPU, Used24hMemory float64
	// JVM is set for JVM containers (WO-12).
	JVM *JVMInfo
}

// HPA describes the HorizontalPodAutoscaler targeting a workload.
type HPA struct {
	Name string `json:"name"`
	// Metrics are "<metric_name>/<metric_target_type>", e.g. "cpu/utilization".
	Metrics []string `json:"metrics,omitempty"`
	// Targets are the averageUtilization targets in percent by resource ("cpu", "memory").
	Targets map[string]float64 `json:"targets,omitempty"`
	// MinReplicas and MaxReplicas are the HPA's bounds (0 when unknown).
	MinReplicas float64 `json:"minReplicas,omitempty"`
	MaxReplicas float64 `json:"maxReplicas,omitempty"`
	// PeakReplicas is the highest current replica count over the loaded window.
	PeakReplicas float64 `json:"peakReplicas,omitempty"`
}

// Coupled reports whether the HPA scales on CPU or memory utilization (D-011).
func (h *HPA) Coupled() bool {
	if h == nil {
		return false
	}
	for _, m := range h.Metrics {
		switch m {
		case "cpu/utilization", "memory/utilization":
			return true
		}
	}
	return false
}

// WorkloadData is the inventory and usage data of one workload.
type WorkloadData struct {
	Key        hubapi.WorkloadKey
	Containers []ContainerData
	// AverageReplicas is the mean number of running pods over the window (missing steps count as 0
	// after the first sample).
	AverageReplicas float64
	HPA             *HPA
}

// ContainerNames returns the sorted container names.
func (w WorkloadData) ContainerNames() []string {
	out := make([]string, 0, len(w.Containers))
	for _, c := range w.Containers {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// Pricing are the hourly rates used for savings.
type Pricing struct {
	CPUCoreHour   float64 `json:"cpuCoreHourUSD"`
	MemoryGiBHour float64 `json:"memoryGiBHourUSD"`
	// Source is "metrics" when the rates come from kerf_*_hourly_cost_usd, "default" otherwise.
	Source string `json:"source"`
	// Nodes is the number of priced nodes the rates were averaged over.
	Nodes int `json:"nodes,omitempty"`
	// Adjustment records the hub's pricing settings applied to the rates (WO-20); nil = none.
	// CPUCoreHour and MemoryGiBHour are then the adjusted rates.
	Adjustment *PricingAdjustment `json:"adjustment,omitempty"`
}

// PricingAdjustment is how the hub's pricing settings changed a cluster's rates: a fixed rate
// replaces the metric or default rate, then the discount applies to whichever rate is used.
type PricingAdjustment struct {
	DiscountPercent   float64 `json:"discountPercent,omitempty"`
	FixedCPU          bool    `json:"fixedCPU,omitempty"`
	FixedMemory       bool    `json:"fixedMemory,omitempty"`
	BaseCPUCoreHour   float64 `json:"baseCPUCoreHourUSD"`
	BaseMemoryGiBHour float64 `json:"baseMemoryGiBHourUSD"`
	SettingsVersion   int     `json:"settingsVersion,omitempty"`
}

// Base returns the rates before any adjustment.
func (p Pricing) Base() Pricing {
	if p.Adjustment == nil {
		return p
	}
	return Pricing{CPUCoreHour: p.Adjustment.BaseCPUCoreHour, MemoryGiBHour: p.Adjustment.BaseMemoryGiBHour, Source: p.Source, Nodes: p.Nodes}
}

// Fallback reports whether the default rates were used.
func (p Pricing) Fallback() bool { return p.Source != PricingSourceMetrics }

// Pricing sources.
const (
	PricingSourceMetrics = hubapi.PricingSourceMetrics
	PricingSourceDefault = hubapi.PricingSourceDefault
)
