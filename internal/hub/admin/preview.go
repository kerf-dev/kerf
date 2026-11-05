package admin

import (
	"math"
	"sort"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Preview item limits.
const (
	DefaultPreviewLimit = 50
	MaxPreviewLimit     = 500
	maxPreviewWarnings  = 20
)

// PreviewWorkload is a workload of the impact preview.
type PreviewWorkload struct {
	Workload
	Savings *hubapi.Savings // monthly savings of its latest recommendation (nil = none)
}

// Preview compares effective modes before and after.
func Preview(ws []PreviewWorkload, before, after []Candidate, limit int) hubapi.ImpactPreview {
	if limit <= 0 {
		limit = DefaultPreviewLimit
	}
	if limit > MaxPreviewLimit {
		limit = MaxPreviewLimit
	}
	out := hubapi.ImpactPreview{Items: []hubapi.ImpactItem{}}
	var into, outOf hubapi.Savings
	seen := map[string]bool{}
	for _, w := range ws {
		out.WorkloadsEvaluated++
		rb, ra := resolve(w.Workload, before), resolve(w.Workload, after)
		for _, msg := range append(rb.labelWarnings, ra.labelWarnings...) {
			if !seen[msg] && len(out.Warnings) < maxPreviewWarnings {
				seen[msg] = true
				out.Warnings = append(out.Warnings, msg)
			}
		}
		b, a := rb.modes, ra.modes
		if b == a {
			continue
		}
		if b.CPU != a.CPU || b.Memory != a.Memory {
			out.WorkloadsChanged++
		}
		if s := w.Savings; s != nil {
			addMove(&into, &outOf, b.CPU, a.CPU, s.CPU, true)
			addMove(&into, &outOf, b.Memory, a.Memory, s.Memory, false)
		}
		item := hubapi.ImpactItem{
			WorkloadID:   w.Key.ID(),
			Cluster:      w.Key.Cluster,
			Namespace:    w.Key.Namespace,
			Kind:         w.Key.Kind,
			Name:         w.Key.Name,
			Before:       b,
			After:        a,
			PolicyBefore: rb.policyName(),
			PolicyAfter:  ra.policyName(),
		}
		if w.Savings != nil {
			s := *w.Savings
			item.MonthlySavingsUSD = &s
		}
		out.Items = append(out.Items, item)
	}
	into.Total, outOf.Total = into.CPU+into.Memory, outOf.CPU+outOf.Memory
	out.SavingsIntoAutoPatchUSD, out.SavingsOutOfAutoPatchUSD = roundSavings(into), roundSavings(outOf)

	total := func(it hubapi.ImpactItem) float64 {
		if it.MonthlySavingsUSD == nil {
			return 0
		}
		return it.MonthlySavingsUSD.Total
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		ti, tj := total(out.Items[i]), total(out.Items[j])
		if ti != tj {
			return ti > tj
		}
		return out.Items[i].WorkloadID < out.Items[j].WorkloadID
	})
	if len(out.Items) > limit {
		out.Items, out.Truncated = out.Items[:limit], true
	}
	return out
}

// policyName is the policy an impact item names: the layer that set the CPU mode if a policy or
// override set it, else the winning policy.
func (r resolution) policyName() string {
	if src := r.cpuBy.source; src == hubapi.LayerPolicy || src == hubapi.LayerWorkloadOverride {
		return r.cpuBy.name
	}
	return r.winner
}

func addMove(into, outOf *hubapi.Savings, before, after string, amount float64, cpu bool) {
	var dst *hubapi.Savings
	switch {
	case before != hubapi.ModeAutoPatch && after == hubapi.ModeAutoPatch:
		dst = into
	case before == hubapi.ModeAutoPatch && after != hubapi.ModeAutoPatch:
		dst = outOf
	default:
		return
	}
	if cpu {
		dst.CPU += amount
	} else {
		dst.Memory += amount
	}
}

func cents(v float64) float64 { return math.Round(v*100) / 100 }

func roundSavings(s hubapi.Savings) hubapi.Savings {
	return hubapi.Savings{CPU: cents(s.CPU), Memory: cents(s.Memory), Total: cents(s.Total)}
}
