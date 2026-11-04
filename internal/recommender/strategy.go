package recommender

import (
	"fmt"
	"math"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Proposal is a strategy's unrounded per-pod request for one container, with the explanation of
// how it was derived. A zero value for a resource means "no data, keep current".
type Proposal struct {
	CPU    float64
	Memory float64
	// CPUExplanation and MemoryExplanation carry statistic, observed, base and margin; the
	// rounding, limit and risk fields are filled in by Recommend.
	CPUExplanation    *hubapi.ResourceExplanation
	MemoryExplanation *hubapi.ResourceExplanation
}

// Strategy turns usage statistics into per-pod requests.
type Strategy interface {
	Name() string
	Propose(c ContainerData, p Profile) Proposal
}

// Strategies is the registry of built-in strategies.
var Strategies = map[string]Strategy{
	PercentileStrategy{}.Name(): PercentileStrategy{},
}

// PercentileStrategy is the built-in strategy:
// CPU request = P(cpuPercentile) of 5m usage x (1 + headroom);
// memory request = max(P(memoryPercentile), peak) x (1 + buffer).
type PercentileStrategy struct{}

// Name implements Strategy.
func (PercentileStrategy) Name() string { return "percentile" }

// Propose implements Strategy.
func (PercentileStrategy) Propose(c ContainerData, p Profile) Proposal {
	var out Proposal
	if !c.CPU.Empty() {
		q, _ := c.CPU.Q(p.CPUPercentile)
		out.CPU = q * (1 + p.CPUHeadroom)
		out.CPUExplanation = &hubapi.ResourceExplanation{
			Statistic: quantileName(p.CPUPercentile),
			Observed:  observed(c.CPU, FormatCPU),
			Base:      FormatCPU(q),
			Margin:    p.CPUHeadroom,
		}
	}
	if !c.Memory.Empty() {
		q, _ := c.Memory.Q(p.MemoryPercentile)
		peak := c.MemoryPeak
		if peak == 0 {
			peak = c.Memory.Max
		}
		base := max(q, peak)
		out.Memory = base * (1 + p.MemoryBuffer)
		peakName := "peak"
		if p.PeakWindow() < p.Window {
			peakName = fmt.Sprintf("peak %s", shortDuration(p.PeakWindow()))
		}
		out.MemoryExplanation = &hubapi.ResourceExplanation{
			Statistic: fmt.Sprintf("max(%s,%s)", quantileName(p.MemoryPercentile), peakName),
			Observed:  observed(c.Memory, formatObservedMemory),
			Base:      formatObservedMemory(base),
			Margin:    p.MemoryBuffer,
		}
	}
	return out
}

func observed(s Stats, format func(float64) string) hubapi.UsageStats {
	var u hubapi.UsageStats
	if v, ok := s.Q(0.5); ok {
		u.P50 = format(v)
	}
	if v, ok := s.Q(0.95); ok {
		u.P95 = format(v)
	}
	if v, ok := s.Q(0.99); ok {
		u.P99 = format(v)
	}
	u.Max = format(s.Max)
	return u
}

// quantileName renders 0.95 as "p95" and 0.999 as "p99.9".
func quantileName(q float64) string {
	return "p" + trimFloat(q*100)
}

// formatObservedMemory rounds to whole KiB so explanations stay readable.
func formatObservedMemory(b float64) string { return FormatMemory(math.Round(b/1024) * 1024) }
