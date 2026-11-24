package capacity

import (
	"math"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestRollup(t *testing.T) {
	node := func(pool, cpu, mem string) hubapi.CapacityNode {
		return hubapi.CapacityNode{Name: "n", NodePool: pool, Allocatable: hubapi.ResourceValues{CPU: cpu, Memory: mem}}
	}
	twoPools := hubapi.CapacityReport{
		NodePools: []hubapi.CapacityNodePool{{Name: "b"}, {Name: "a"}},
		Nodes:     []hubapi.CapacityNode{node("a", "4", "16Gi"), node("a", "4", "16Gi")},
	}
	noPoolNode := twoPools
	noPoolNode.Nodes = append(append([]hubapi.CapacityNode{}, twoPools.Nodes...), node("", "2", "1Gi"))
	cordoned := hubapi.CapacityReport{Nodes: []hubapi.CapacityNode{node("a", "4", "1Gi")}}
	cordoned.Nodes[0].Unschedulable = true
	podGroup := hubapi.CapacityReport{
		NodePools: []hubapi.CapacityNodePool{{Name: "a"}},
		PodGroups: []hubapi.CapacityPodGroup{{
			NodePool: "a",
			Count:    3,
			Containers: []hubapi.ContainerResources{
				{Name: "x", Requests: &hubapi.ResourceValues{CPU: "100m"}},
				{Name: "y", Requests: &hubapi.ResourceValues{CPU: "200m"}},
			},
			InitRequests: &hubapi.ResourceValues{CPU: "500m"},
			Overhead:     &hubapi.ResourceValues{CPU: "50m"},
		}},
	}
	truncated := twoPools
	truncated.Truncated = true

	tests := []struct {
		name  string
		rep   hubapi.CapacityReport
		check func(t *testing.T, rows []HourRow)
	}{
		{"empty pool is listed", twoPools, func(t *testing.T, rows []HourRow) {
			if len(rows) != 3 || rows[0].NodePool != "" || rows[1].NodePool != "a" || rows[2].NodePool != "b" {
				t.Fatalf("rows = %+v", rows)
			}
			if rows[1].Nodes != 2 || rows[1].CPUAllocatable != 8 || rows[1].MemAllocatable != 32*(1<<30) {
				t.Errorf("a = %+v", rows[1])
			}
			if (rows[2] != HourRow{NodePool: "b"}) {
				t.Errorf("b = %+v, want all zero", rows[2])
			}
		}},
		{"node without pool counts in total only", noPoolNode, func(t *testing.T, rows []HourRow) {
			if len(rows) != 3 {
				t.Fatalf("rows = %+v", rows)
			}
			if rows[0].CPUAllocatable != 10 || rows[0].Nodes != 3 {
				t.Errorf("total = %+v", rows[0])
			}
			if rows[1].CPUAllocatable != 8 || rows[1].Nodes != 2 {
				t.Errorf("a = %+v", rows[1])
			}
		}},
		{"unschedulable node counts", cordoned, func(t *testing.T, rows []HourRow) {
			if rows[0].CPUAllocatable != 4 || rows[0].Nodes != 1 || rows[1].CPUAllocatable != 4 {
				t.Errorf("rows = %+v", rows)
			}
		}},
		{"pod group uses the init floor and overhead", podGroup, func(t *testing.T, rows []HourRow) {
			want := 3 * (0.5 + 0.05)
			if rows[1].NodePool != "a" || math.Abs(rows[1].CPURequested-want) > 1e-9 || math.Abs(rows[0].CPURequested-want) > 1e-9 {
				t.Errorf("rows = %+v, want cpu %v", rows, want)
			}
		}},
		{"truncated report marks every row", truncated, func(t *testing.T, rows []HourRow) {
			for _, r := range rows {
				if !r.Truncated {
					t.Errorf("row %q not truncated", r.NodePool)
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, Rollup(tt.rep)) })
	}
}
