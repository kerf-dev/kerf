package capacity

import (
	"reflect"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestCutLists(t *testing.T) {
	pods := make([]hubapi.CapacityPod, maxPods)
	for _, c := range []struct {
		name   string
		rep    hubapi.CapacityReport
		want   []string
		totals bool
	}{
		{"complete", hubapi.CapacityReport{Pods: pods}, nil, false},
		{"lists from the agent", hubapi.CapacityReport{Truncated: true, TruncatedLists: []string{"pdbs"}}, []string{"pdbs"}, false},
		{"old agent, blocking pods at the limit", hubapi.CapacityReport{Truncated: true, Pods: pods}, []string{"pods"}, false},
		{"old agent, size cut", hubapi.CapacityReport{Truncated: true}, []string{"podGroups"}, true},
		{"agent cut nodes", hubapi.CapacityReport{Truncated: true, TruncatedLists: []string{"nodes", "pods"}}, []string{"nodes", "pods"}, true},
	} {
		if got := CutLists(c.rep); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: CutLists %v, want %v", c.name, got, c.want)
		}
		if got := TotalsCut(c.rep); got != c.totals {
			t.Errorf("%s: TotalsCut %v, want %v", c.name, got, c.totals)
		}
		if rows := Rollup(c.rep); rows[0].Truncated != c.totals {
			t.Errorf("%s: Rollup truncated %v, want %v", c.name, rows[0].Truncated, c.totals)
		}
	}
}
