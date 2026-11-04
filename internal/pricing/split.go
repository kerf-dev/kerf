package pricing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sync"
)

// DefaultCPUWeight is the fallback weight of one vCPU, in GiB of memory: 1 vCPU costs as much as 9 GiB.
const DefaultCPUWeight = 9.0

// Weight sources, for logs.
const (
	WeightFixed    = "fixed"
	WeightDerived  = "derived"
	WeightFallback = "fallback"
)

// Split divides a node's hourly price into a per-vCPU and a per-GiB rate, given the node's allocatable
// cores and GiB and the weight of one vCPU in GiB. The rates satisfy
// cpuRate*cores + memRate*gib == nodeUSD (the invariant in docs/contracts/metrics.md).
func Split(nodeUSD, cores, gib, cpuWeight float64) (cpuRate, memRate float64, err error) {
	if cores <= 0 || gib <= 0 {
		return 0, 0, fmt.Errorf("allocatable cpu (%g) and memory (%g GiB) must be > 0", cores, gib)
	}
	if cpuWeight <= 0 || math.IsNaN(cpuWeight) || math.IsInf(cpuWeight, 0) {
		return 0, 0, fmt.Errorf("invalid cpu weight %g", cpuWeight)
	}
	memRate = nodeUSD / (cpuWeight*cores + gib)
	return cpuWeight * memRate, memRate, nil
}

// Splitter decides the CPU weight per instance type.
//
// With Fixed > 0 every node uses that weight. Otherwise the weight is derived per instance family:
// the on-demand prices of the c, m and r classes of the same generation and size (e.g. c6i.xlarge,
// m6i.xlarge, r6i.xlarge), which differ only in their vCPU:GiB ratio, are fitted to
// price = a*vCPU + b*GiB and the weight is a/b. When fewer than two distinct ratios are priced, or the
// fit is not positive, Fallback (DefaultCPUWeight if 0) is used.
type Splitter struct {
	Provider Provider
	Fixed    float64
	Fallback float64
	Log      *slog.Logger

	mu    sync.Mutex
	cache map[string]weight
}

type weight struct {
	value  float64
	source string
}

var instanceTypeRE = regexp.MustCompile(`^([a-z]+)(\d+)([a-z0-9-]*)\.([a-z0-9]+)$`)

// siblings returns the c/m/r instance types of the same generation and size, or nil when the
// instance type is not one of those classes.
func siblings(instanceType string) []string {
	m := instanceTypeRE.FindStringSubmatch(instanceType)
	if m == nil {
		return nil
	}
	switch m[1] {
	case "c", "m", "r":
	default:
		return nil
	}
	out := make([]string, 0, 3)
	for _, class := range []string{"c", "m", "r"} {
		out = append(out, class+m[2]+m[3]+"."+m[4])
	}
	return out
}

// Weight returns the CPU weight for an instance type in a region and where it came from.
func (s *Splitter) Weight(ctx context.Context, region, instanceType string) (float64, string) {
	if s.Fixed > 0 {
		return s.Fixed, WeightFixed
	}
	fallback := s.Fallback
	if fallback <= 0 {
		fallback = DefaultCPUWeight
	}
	sibs := siblings(instanceType)
	if sibs == nil {
		return fallback, WeightFallback
	}
	cacheKey := region + "/" + sibs[0]
	s.mu.Lock()
	w, ok := s.cache[cacheKey]
	s.mu.Unlock()
	if ok {
		return w.value, w.source
	}

	var points []Price
	transient := false
	for _, it := range sibs {
		p, err := s.Provider.Price(ctx, Key{Region: region, InstanceType: it, CapacityType: OnDemand})
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				transient = true
			}
			continue
		}
		points = append(points, p)
	}
	w = weight{fallback, WeightFallback}
	if v, ok := fitWeight(points); ok {
		w = weight{v, WeightDerived}
	}
	if s.Log != nil {
		s.Log.DebugContext(ctx, "cpu weight", "region", region, "family", sibs[0], "weight", w.value, "source", w.source, "points", len(points))
	}
	if !transient { // retry next time if a sibling price failed for a transient reason
		s.mu.Lock()
		if s.cache == nil {
			s.cache = map[string]weight{}
		}
		s.cache[cacheKey] = w
		s.mu.Unlock()
	}
	return w.value, w.source
}

// fitWeight solves price = a*vCPU + b*GiB by least squares and returns a/b.
func fitWeight(points []Price) (float64, bool) {
	var vv, vg, gg, vp, gp float64
	ratios := map[float64]bool{}
	for _, p := range points {
		v, g, usd := p.Shape.VCPU, p.Shape.MemoryGiB, p.HourlyUSD
		if v <= 0 || g <= 0 || usd <= 0 {
			continue
		}
		ratios[math.Round(g/v*1000)/1000] = true
		vv += v * v
		vg += v * g
		gg += g * g
		vp += v * usd
		gp += g * usd
	}
	if len(ratios) < 2 {
		return 0, false
	}
	det := vv*gg - vg*vg
	if det == 0 {
		return 0, false
	}
	a := (vp*gg - gp*vg) / det
	b := (vv*gp - vg*vp) / det
	if a <= 0 || b <= 0 {
		return 0, false
	}
	return a / b, true
}
