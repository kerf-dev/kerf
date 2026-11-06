// Package capacity turns an agent's capacity report into node-level numbers: requested vs
// allocatable now and after the open recommendations, an estimate of the nodes each node pool
// needs (first-fit-decreasing bin packing of pod requests, priced), and what likely blocks
// consolidation. The method is documented in docs/node-savings.md. Kerf never changes nodes.
package capacity

import (
	"math"
	"sort"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// MethodURL documents the estimate.
const MethodURL = "https://getkerf.dev/docs/node-savings#how-the-estimate-works"

// HoursPerMonth converts hourly prices to monthly ones.
const HoursPerMonth = recommender.HoursPerMonth

// Amounts are CPU cores and memory bytes.
type Amounts struct{ CPU, Mem float64 }

func (a Amounts) add(b Amounts) Amounts { return Amounts{a.CPU + b.CPU, a.Mem + b.Mem} }
func (a Amounts) sub(b Amounts) Amounts { return Amounts{a.CPU - b.CPU, a.Mem - b.Mem} }
func (a Amounts) scale(n float64) Amounts {
	return Amounts{a.CPU * n, a.Mem * n}
}

// fits reports whether a fits into b (with a small tolerance for float sums).
func (a Amounts) fits(b Amounts) bool { return a.CPU <= b.CPU+1e-9 && a.Mem <= b.Mem+0.5 }

// API converts to the hub API type.
func (a Amounts) API() hubapi.CapacityAmounts {
	return hubapi.CapacityAmounts{CPUCores: round(a.CPU, 3), MemoryBytes: math.Round(a.Mem)}
}

// Recs are the open recommendations by workload.
type Recs map[hubapi.WorkloadRef]RecommendedRequests

// RecommendedRequests are the recommended requests of one workload's containers; a missing
// resource keeps the current value.
type RecommendedRequests map[string]Request

// Request is a recommended request; nil keeps the current value.
type Request struct{ CPU, Mem *float64 }

// Prices are the hourly prices the estimate uses.
type Prices struct {
	// ByType is the hourly price per instance type and capacity type (the pricing series).
	ByType map[TypeKey]float64
	// CPUCoreHour and MemoryGiBHour price an instance type without a series.
	CPUCoreHour, MemoryGiBHour float64
}

// TypeKey identifies an instance type offering.
type TypeKey struct{ InstanceType, CapacityType string }

func (p Prices) price(k TypeKey, alloc Amounts) (float64, string) {
	if v, ok := p.ByType[k]; ok && v > 0 {
		return v, hubapi.PriceSourceMetric
	}
	if p.CPUCoreHour > 0 || p.MemoryGiBHour > 0 {
		return alloc.CPU*p.CPUCoreHour + alloc.Mem/(1<<30)*p.MemoryGiBHour, hubapi.PriceSourceRates
	}
	return 0, hubapi.PriceSourceNone
}

// Input is everything the estimate needs.
type Input struct {
	Report hubapi.CapacityReport
	Recs   Recs
	Prices Prices
}

// Result is the estimate and the totals.
type Result struct {
	Totals   hubapi.CapacityTotals
	Estimate *hubapi.NodeEstimate
	Pools    []hubapi.CapacityPoolEstimate
	// Needed is the packed node count per pool (before the pool minimum), for the blockers.
	Needed map[string]int
}

// pod is one pod to pack.
type pod struct{ req Amounts }

// itype is one candidate instance type of a pool.
type itype struct {
	key       TypeKey
	alloc     Amounts
	nodes     int
	estimated int
	hourly    float64
	source    string
	// usable is false for types the pool no longer allows: their nodes count now, but the
	// estimate never chooses them.
	usable bool
}

// Estimate computes the totals and the per-pool estimate.
func Estimate(in Input) Result {
	rep := in.Report
	res := Result{Needed: map[string]int{}}

	// Pools of the report and those only nodes or pods name ("" = no pool); listed by name.
	type poolAcc struct {
		def      hubapi.CapacityNodePool
		nodes    []hubapi.CapacityNode
		pods     []pod
		dsMax    map[string]Amounts // per daemonset owner: the largest pod, after recommendations
		req, aft Amounts
	}
	pools := map[string]*poolAcc{}
	var order []string
	get := func(name string) *poolAcc {
		if p, ok := pools[name]; ok {
			return p
		}
		p := &poolAcc{def: hubapi.CapacityNodePool{Name: name, Autoscaler: hubapi.AutoscalerNone}, dsMax: map[string]Amounts{}}
		pools[name] = p
		order = append(order, name)
		return p
	}
	for _, np := range rep.NodePools {
		get(np.Name).def = np
	}
	var totalAlloc Amounts
	for _, n := range rep.Nodes {
		p := get(n.NodePool)
		p.nodes = append(p.nodes, n)
		totalAlloc = totalAlloc.add(parseValues(&n.Allocatable))
	}
	res.Totals.Nodes, res.Totals.Allocatable = len(rep.Nodes), totalAlloc.API()

	recsUsed := map[hubapi.WorkloadRef]bool{}
	var totalReq, totalAft Amounts
	for _, g := range rep.PodGroups {
		p := get(g.NodePool)
		now := groupRequests(g, nil)
		var rr RecommendedRequests
		if g.Workload != nil {
			rr = in.Recs[*g.Workload]
		}
		after := groupRequests(g, rr)
		if rr != nil && after != now {
			recsUsed[*g.Workload] = true
		}
		n := float64(g.Count)
		p.req, p.aft = p.req.add(now.scale(n)), p.aft.add(after.scale(n))
		totalReq, totalAft = totalReq.add(now.scale(n)), totalAft.add(after.scale(n))
		if g.DaemonSet {
			key := "pod"
			if g.Owner != nil {
				key = g.Owner.Kind + "/" + g.Owner.Namespace + "/" + g.Owner.Name
			}
			cur := p.dsMax[key]
			p.dsMax[key] = Amounts{max(cur.CPU, after.CPU), max(cur.Mem, after.Mem)}
			continue
		}
		for range g.Count {
			p.pods = append(p.pods, pod{req: after})
		}
	}
	res.Totals.Requested, res.Totals.RequestedAfter = totalReq.API(), totalAft.API()

	est := &hubapi.NodeEstimate{Method: hubapi.EstimateFFD, MethodURL: MethodURL, Recommendations: len(recsUsed)}
	sources := map[string]bool{}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	for _, name := range order {
		p := pools[name]
		if len(p.nodes) == 0 && len(p.pods) == 0 {
			// A pool without nodes or pods (e.g. an idle Karpenter NodePool): listed, nothing to estimate.
			res.Pools = append(res.Pools, hubapi.CapacityPoolEstimate{
				Name: name, Autoscaler: p.def.Autoscaler, Object: p.def.Object, MinNodes: p.def.MinNodes,
				InstanceTypes: []hubapi.CapacityInstanceType{}, ConsolidationPolicy: p.def.ConsolidationPolicy,
			})
			continue
		}
		var overhead Amounts
		for _, a := range p.dsMax {
			overhead = overhead.add(a)
		}
		types := candidateTypes(p.nodes, p.def.InstanceTypes, in.Prices)
		pe := hubapi.CapacityPoolEstimate{
			Name: name, Autoscaler: p.def.Autoscaler, Object: p.def.Object, MinNodes: p.def.MinNodes,
			Nodes: len(p.nodes), Requested: p.req.API(), RequestedAfter: p.aft.API(),
			ConsolidationPolicy: p.def.ConsolidationPolicy, InstanceTypes: []hubapi.CapacityInstanceType{},
		}
		if overhead != (Amounts{}) {
			o := overhead.API()
			pe.DaemonSetOverhead = &o
		}
		var alloc Amounts
		for _, n := range p.nodes {
			alloc = alloc.add(parseValues(&n.Allocatable))
		}
		pe.Allocatable = alloc.API()
		for _, n := range p.nodes {
			k := TypeKey{n.InstanceType, n.CapacityType}
			for _, t := range types {
				if t.key == k {
					pe.CurrentMonthlyUSD += t.hourly * HoursPerMonth
				}
			}
		}
		bins, unplaced := pack(p.pods, types, overhead)
		res.Needed[name] = bins
		total := bins
		if p.def.MinNodes != nil && *p.def.MinNodes > total && len(types) > 0 {
			// The pool keeps its minimum: add the cheapest type.
			if c := cheapestIndex(types); c >= 0 {
				types[c].estimated += *p.def.MinNodes - total
				total = *p.def.MinNodes
			}
		}
		pe.EstimatedNodes, pe.Unplaced = total, unplaced
		for _, t := range types {
			pe.EstimatedMonthlyUSD += t.hourly * HoursPerMonth * float64(t.estimated)
			if t.nodes > 0 || t.estimated > 0 {
				sources[t.source] = true
			}
			pe.InstanceTypes = append(pe.InstanceTypes, hubapi.CapacityInstanceType{
				InstanceType: t.key.InstanceType, CapacityType: t.key.CapacityType, Nodes: t.nodes, EstimatedNodes: t.estimated,
				Allocatable: t.alloc.API(), HourlyUSD: round(t.hourly, 4), PriceSource: t.source,
			})
		}
		pe.CurrentMonthlyUSD, pe.EstimatedMonthlyUSD = round(pe.CurrentMonthlyUSD, 2), round(pe.EstimatedMonthlyUSD, 2)
		est.CurrentNodes += pe.Nodes
		est.EstimatedNodes += pe.EstimatedNodes
		est.CurrentMonthlyUSD += pe.CurrentMonthlyUSD
		est.EstimatedMonthlyUSD += pe.EstimatedMonthlyUSD
		res.Pools = append(res.Pools, pe)
	}
	est.CurrentMonthlyUSD, est.EstimatedMonthlyUSD = round(est.CurrentMonthlyUSD, 2), round(est.EstimatedMonthlyUSD, 2)
	est.SavingsMonthlyUSD = round(est.CurrentMonthlyUSD-est.EstimatedMonthlyUSD, 2)
	switch {
	case len(sources) == 0:
		est.PriceSource = hubapi.PriceSourceNone
	case len(sources) == 1:
		for s := range sources {
			est.PriceSource = s
		}
	default:
		est.PriceSource = hubapi.PriceSourceMixed
	}
	if len(rep.Nodes) > 0 {
		res.Estimate = est
	}
	return res
}

// candidateTypes are the instance types running in the pool (restricted to the pool's allowed
// types when it lists them), each with the smallest allocatable seen and its price.
func candidateTypes(nodes []hubapi.CapacityNode, allowed []string, prices Prices) []*itype {
	ok := map[string]bool{}
	for _, t := range allowed {
		ok[t] = true
	}
	byKey := map[TypeKey]*itype{}
	var out []*itype
	for _, n := range nodes {
		k := TypeKey{n.InstanceType, n.CapacityType}
		a := parseValues(&n.Allocatable)
		t, seen := byKey[k]
		if !seen {
			t = &itype{key: k, alloc: a}
			byKey[k] = t
			out = append(out, t)
		}
		t.nodes++
		t.alloc = Amounts{min(t.alloc.CPU, a.CPU), min(t.alloc.Mem, a.Mem)}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].key.InstanceType != out[j].key.InstanceType {
			return out[i].key.InstanceType < out[j].key.InstanceType
		}
		return out[i].key.CapacityType < out[j].key.CapacityType
	})
	for _, t := range out {
		t.hourly, t.source = prices.price(t.key, t.alloc)
		t.usable = len(ok) == 0 || ok[t.key.InstanceType]
	}
	if !anyUsable(out) {
		// None of the running types is allowed any more: pack into them anyway.
		for _, t := range out {
			t.usable = true
		}
	}
	return out
}

func anyUsable(ts []*itype) bool {
	for _, t := range ts {
		if t.usable {
			return true
		}
	}
	return false
}

// cheapestIndex returns the cheapest usable type.
func cheapestIndex(ts []*itype) int {
	best := -1
	for i, t := range ts {
		if t.usable && (best < 0 || t.hourly < ts[best].hourly) {
			best = i
		}
	}
	return best
}

// pack places pods with first-fit decreasing into bins of the largest usable type (allocatable
// minus daemonset overhead), then gives each bin the cheapest type its pods fit. It returns the
// bins and the pods that fit no type (each counted as a bin of the largest type).
func pack(pods []pod, all []*itype, overhead Amounts) (bins, unplaced int) {
	var types []*itype
	for _, t := range all {
		if t.usable {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return 0, len(pods)
	}
	largest := types[0]
	var maxA Amounts
	for _, t := range types {
		maxA = Amounts{max(maxA.CPU, t.alloc.CPU), max(maxA.Mem, t.alloc.Mem)}
	}
	size := func(a Amounts) float64 {
		s := 0.0
		if maxA.CPU > 0 {
			s += a.CPU / maxA.CPU
		}
		if maxA.Mem > 0 {
			s += a.Mem / maxA.Mem
		}
		return s
	}
	for _, t := range types {
		if size(t.alloc) > size(largest.alloc) {
			largest = t
		}
	}
	room := largest.alloc.sub(overhead)
	sorted := append([]pod(nil), pods...)
	sort.SliceStable(sorted, func(i, j int) bool { return size(sorted[i].req) > size(sorted[j].req) })
	var loads []Amounts
	for _, p := range sorted {
		if !p.req.fits(room) {
			unplaced++
			continue
		}
		placed := false
		for i := range loads {
			if next := loads[i].add(p.req); next.fits(room) {
				loads[i], placed = next, true
				break
			}
		}
		if !placed {
			loads = append(loads, p.req)
		}
	}
	for _, l := range loads {
		best := largest
		for _, t := range types {
			if l.add(overhead).fits(t.alloc) && t.hourly < best.hourly {
				best = t
			}
		}
		best.estimated++
	}
	largest.estimated += unplaced
	return len(loads) + unplaced, unplaced
}

// groupRequests returns one pod's requests: the containers (recommended where rr has a value),
// at least the init floor, plus the overhead.
func groupRequests(g hubapi.CapacityPodGroup, rr RecommendedRequests) Amounts {
	var sum Amounts
	for _, c := range g.Containers {
		a := parseValues(c.Requests)
		if r, ok := rr[c.Name]; ok {
			if r.CPU != nil {
				a.CPU = *r.CPU
			}
			if r.Mem != nil {
				a.Mem = *r.Mem
			}
		}
		sum = sum.add(a)
	}
	init := parseValues(g.InitRequests)
	sum = Amounts{max(sum.CPU, init.CPU), max(sum.Mem, init.Mem)}
	return sum.add(parseValues(g.Overhead))
}

// parseValues parses CPU and memory quantities (unset or invalid = 0).
func parseValues(v *hubapi.ResourceValues) Amounts {
	if v == nil {
		return Amounts{}
	}
	var a Amounts
	if v.CPU != "" {
		if c, err := recommender.ParseCPU(v.CPU); err == nil {
			a.CPU = c
		}
	}
	if v.Memory != "" {
		if m, err := recommender.ParseMemory(v.Memory); err == nil {
			a.Mem = m
		}
	}
	return a
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
