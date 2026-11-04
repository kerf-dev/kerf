package recommender

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// HPA coordination (WO-11, lifts D-011).
//
// An HPA on a resource's utilization scales on usage / (sum of the pod's requests x target): the
// desired replica count is ceil(replicas x utilization / target). Changing the requests alone
// changes when it scales; changing them together with the target so that
// sum(requests) x target stays the same leaves the scaling behaviour unchanged. The recommended
// requests come from the strategy as for any workload; the target follows from them,
// target' = target x sum(current requests) / sum(recommended requests). When that target would
// leave the bounds (40-90 %, widened to include the current target), the target is clamped and
// the requests of that resource are scaled so the product holds again.

// HPA target bounds in percent (WO-11).
const (
	HPAMinTarget = 40
	HPAMaxTarget = 90
)

// coordinateHPA computes the HPA coordination of an hpa-coupled workload and, where the target
// would leave its bounds, adjusts the recommended requests (and the limits and savings that
// follow from them) of rec's containers. It returns nil for workloads without a coupled HPA.
func coordinateHPA(rec *hubapi.WorkloadRecommendation, w WorkloadData, req Request, cfg Config) *hubapi.HPACoordination {
	h := w.HPA
	if !h.Coupled() {
		return nil
	}
	out := &hubapi.HPACoordination{Name: h.Name}
	if h.MinReplicas > 0 {
		v := int(h.MinReplicas)
		out.MinReplicas = &v
	}
	if h.MaxReplicas > 0 {
		v := int(h.MaxReplicas)
		out.MaxReplicas = &v
	}
	var util, other []string
	for _, m := range h.Metrics {
		switch m {
		case "cpu/utilization", "memory/utilization":
			util = append(util, strings.TrimSuffix(m, "/utilization"))
		default:
			other = append(other, m)
		}
	}
	out.Metric = util[0] // Coupled: at least one
	target := h.Targets[out.Metric]
	out.CurrentTarget = int(math.Round(target))
	switch {
	case len(util) != 1 || len(other) > 0 || target <= 0:
		out.SkipReason = hubapi.HPASkipUnsupported
		return out
	case h.MaxReplicas > 0 && h.PeakReplicas >= h.MaxReplicas:
		out.SkipReason = hubapi.HPASkipMaxReplicas
		return out
	}

	res := out.Metric
	parse, step, minimum := ParseCPU, cfg.CPUStepCores, cfg.MinCPUCores
	if res == "memory" {
		parse, step, minimum = ParseMemory, cfg.MemoryStepBytes, cfg.MinMemoryBytes
	}
	request := func(r hubapi.Resources) float64 {
		if r.Requests == nil {
			return 0
		}
		v := r.Requests.CPU
		if res == "memory" {
			v = r.Requests.Memory
		}
		f, _ := parse(v)
		return f
	}
	// The HPA divides by the requests of the pod's (app) containers.
	var idx []int
	var curSum, recSum float64
	for i, c := range rec.Containers {
		if c.Init {
			continue
		}
		cur := request(c.Current)
		if cur <= 0 {
			out.SkipReason = hubapi.HPASkipMissingRequest
			return out
		}
		idx = append(idx, i)
		curSum += cur
		recSum += request(c.Recommended)
	}
	if len(idx) == 0 || recSum <= 0 {
		out.SkipReason = hubapi.HPASkipMissingRequest
		return out
	}
	lo, hi := math.Min(HPAMinTarget, target), math.Max(HPAMaxTarget, target)
	product := target * curSum // sum(requests) x target, kept constant
	next := product / recSum
	if next < lo || next > hi {
		bound := math.Min(math.Max(next, lo), hi)
		want := product / bound // the sum of requests that gives the bound
		if want < recSum {
			// The target would fall below its bound: the requests must be lower than the strategy
			// recommends. Never below a floor that a throttled / oom-killed container needs.
			flag := hubapi.FlagThrottled
			if res == "memory" {
				flag = hubapi.FlagOOMKilled
			}
			for _, i := range idx {
				if slices.Contains(rec.Containers[i].RiskFlags, flag) {
					out.SkipReason = hubapi.HPASkipTargetOutOfRange
					return out
				}
			}
		}
		f := want / recSum
		recSum = 0
		for _, i := range idx {
			c := &rec.Containers[i]
			v := roundUp(math.Max(request(c.Recommended)*f, minimum), step)
			setRequest(c, res, v, req, w, cfg)
			recSum += v
		}
		next = product / recSum
	}
	out.RecommendedTarget = int(math.Round(math.Min(math.Max(next, lo), hi)))
	note := "HPA " + h.Name + ": " + res + " utilization target " + strconv.Itoa(out.CurrentTarget) + "% -> " +
		strconv.Itoa(out.RecommendedTarget) + "% with these requests (scaling unchanged)"
	for _, i := range idx {
		if e := explanationOf(&rec.Containers[i], res); e != nil {
			e.Notes = append(e.Notes, note)
		}
	}
	return out
}

func explanationOf(c *hubapi.ContainerRecommendation, res string) *hubapi.ResourceExplanation {
	if c.Explanation == nil {
		return nil
	}
	if res == "memory" {
		return c.Explanation.Memory
	}
	return c.Explanation.CPU
}

// setRequest changes the recommended request of res of c to v (keeping the limit rule of the
// explanation) and recomputes the container's savings.
func setRequest(c *hubapi.ContainerRecommendation, res string, v float64, req Request, w WorkloadData, cfg Config) {
	e := explanationOf(c, res)
	if e != nil {
		e.Notes = append(e.Notes, "adjusted to keep the HPA target within "+strconv.Itoa(HPAMinTarget)+"-"+strconv.Itoa(HPAMaxTarget)+"%")
	}
	rule := ""
	if e != nil {
		rule = e.LimitRule
	}
	cpuReq, memReq, cpuLim, memLim := values(c.Recommended)
	curCPU, curMem, _, _ := values(c.Current)
	if res == "cpu" {
		cpuReq = v
		switch {
		case strings.HasPrefix(rule, "ratio "):
			if r, err := parseRatio(strings.TrimPrefix(rule, "ratio ")); err == nil {
				cpuLim = roundUp(v*r, cfg.CPUStepCores)
			}
		case cpuLim > 0 && cpuLim < v:
			cpuLim = v
		}
	} else {
		memReq = v
		switch {
		case rule == "equals request":
			memLim = v
		case memLim > 0 && memLim < v:
			memLim = v
		}
	}
	c.Recommended = resources(cpuReq, memReq, cpuLim, memLim)
	if c.MonthlySavingsUSD != nil {
		sv := *c.MonthlySavingsUSD
		reps := w.AverageReplicas
		if res == "cpu" {
			sv.CPU = (curCPU - cpuReq) * req.Pricing.CPUCoreHour * reps * HoursPerMonth
		} else {
			sv.Memory = (curMem - memReq) / GiB * req.Pricing.MemoryGiBHour * reps * HoursPerMonth
		}
		sv.Total = sv.CPU + sv.Memory
		sv = roundSavings(sv)
		c.MonthlySavingsUSD = &sv
	}
}

// values parses requests and limits (0 when unset).
func values(r hubapi.Resources) (cpuReq, memReq, cpuLim, memLim float64) {
	if r.Requests != nil {
		cpuReq, _ = ParseCPU(r.Requests.CPU)
		memReq, _ = ParseMemory(r.Requests.Memory)
	}
	if r.Limits != nil {
		cpuLim, _ = ParseCPU(r.Limits.CPU)
		memLim, _ = ParseMemory(r.Limits.Memory)
	}
	return
}
