package capacity

import (
	"sort"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// HourRow is the capacity of one node pool at one point in time; NodePool "" is the cluster
// total. CPU is in cores, memory in bytes.
type HourRow struct {
	NodePool                     string
	Nodes                        int
	CPURequested, CPUAllocatable float64
	MemRequested, MemAllocatable float64
	// Truncated is true when the report the row comes from cut nodes or pod groups (TotalsCut).
	Truncated bool
}

// Rollup totals a report per node pool and for the cluster, sorted by pool name (the total,
// named "", comes first). Every pool the report lists gets a row, also one without nodes. Nodes
// and pods outside any pool count only in the total. Requests are the current ones, with no
// recommendation applied.
func Rollup(rep hubapi.CapacityReport) []HourRow {
	cut := TotalsCut(rep)
	byPool := map[string]*HourRow{"": {Truncated: cut}}
	get := func(name string) *HourRow {
		r, ok := byPool[name]
		if !ok {
			r = &HourRow{NodePool: name, Truncated: cut}
			byPool[name] = r
		}
		return r
	}
	// targets are the rows an object of the pool counts in: the total and, with a pool, its row.
	targets := func(pool string) []*HourRow {
		if pool == "" {
			return []*HourRow{byPool[""]}
		}
		return []*HourRow{byPool[""], get(pool)}
	}
	for _, np := range rep.NodePools {
		get(np.Name)
	}
	for _, n := range rep.Nodes {
		a := parseValues(&n.Allocatable)
		for _, r := range targets(n.NodePool) {
			r.Nodes++
			r.CPUAllocatable += a.CPU
			r.MemAllocatable += a.Mem
		}
	}
	for _, g := range rep.PodGroups {
		req := groupRequests(g, nil).scale(float64(g.Count))
		for _, r := range targets(g.NodePool) {
			r.CPURequested += req.CPU
			r.MemRequested += req.Mem
		}
	}
	rows := make([]HourRow, 0, len(byPool))
	for _, r := range byPool {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].NodePool < rows[j].NodePool })
	return rows
}
