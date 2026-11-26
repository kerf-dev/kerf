package capacity

import (
	"slices"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// List limits of the agent's capacity report (internal/agent/capacity/report.go).
const (
	maxNodes     = 10000
	maxPodGroups = 50000
	maxPDBs      = 500
	maxPods      = 500
)

// CutLists returns the lists of a report that hit a limit: nodes, podGroups, pdbs, pods. Agents
// before 0.4.4 send only `truncated`, so the hub infers the lists from their lengths. A truncated
// report with no list at its limit was cut for size, which drops pod groups first.
// ponytail: a size cut of a report that also has 500 blocking pods reads as pods only; size cuts
// start at 4 MiB, far above real reports.
func CutLists(rep hubapi.CapacityReport) []string {
	if len(rep.TruncatedLists) > 0 || !rep.Truncated {
		return rep.TruncatedLists
	}
	var out []string
	for _, l := range []struct {
		name     string
		n, limit int
	}{
		{"nodes", len(rep.Nodes), maxNodes},
		{"podGroups", len(rep.PodGroups), maxPodGroups},
		{"pdbs", len(rep.PDBs), maxPDBs},
		{"pods", len(rep.Pods), maxPods},
	} {
		if l.n >= l.limit {
			out = append(out, l.name)
		}
	}
	if out == nil {
		out = []string{"podGroups"}
	}
	return out
}

// TotalsCut reports whether the report cut nodes or pod groups, so node counts and requests may be low.
func TotalsCut(rep hubapi.CapacityReport) bool {
	lists := CutLists(rep)
	return slices.Contains(lists, "nodes") || slices.Contains(lists, "podGroups")
}
