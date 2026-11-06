package capacity

import (
	"fmt"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

const gi = float64(1 << 30)

func n(name, pool, typ string, cpu string, memGi int) hubapi.CapacityNode {
	return hubapi.CapacityNode{Name: name, NodePool: pool, InstanceType: typ, CapacityType: "on-demand",
		Allocatable: hubapi.ResourceValues{CPU: cpu, Memory: fmt.Sprintf("%dGi", memGi)}}
}

func nodes(count int, pool, typ, cpu string, memGi int) []hubapi.CapacityNode {
	var out []hubapi.CapacityNode
	for i := range count {
		out = append(out, n(fmt.Sprintf("%s-%s-%d", pool, typ, i), pool, typ, cpu, memGi))
	}
	return out
}

func grp(ns, name, pool string, count int, cpu, mem string) hubapi.CapacityPodGroup {
	return hubapi.CapacityPodGroup{
		Owner:    &hubapi.CapacityObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: ns, Name: name},
		Workload: &hubapi.WorkloadRef{Namespace: ns, Kind: "Deployment", Name: name},
		NodePool: pool, Count: count,
		Containers: []hubapi.ContainerResources{{Name: "app", Requests: &hubapi.ResourceValues{CPU: cpu, Memory: mem}}},
	}
}

func ds(name, pool string, count int, cpu, mem string) hubapi.CapacityPodGroup {
	g := grp("kube-system", name, pool, count, cpu, mem)
	g.Owner.Kind, g.Workload.Kind, g.DaemonSet = "DaemonSet", "DaemonSet", true
	return g
}

func rec(ns, name string, cpu, memGi float64) Recs {
	return Recs{{Namespace: ns, Kind: "Deployment", Name: name}: {"app": {CPU: &cpu, Mem: ptrF(memGi * gi)}}}
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

var xlPrice = Prices{ByType: map[TypeKey]float64{{"m5.xlarge", "on-demand"}: 0.2, {"m5.large", "on-demand"}: 0.1, {"m5.4xlarge", "on-demand"}: 0.8}}

func TestEstimate(t *testing.T) {
	type typeWant struct {
		typ          string
		nodes, after int
	}
	tests := []struct {
		name          string
		in            Input
		pool          string
		wantNodes     int
		wantEstimated int
		wantUnplaced  int
		wantTypes     []typeWant
		wantSavings   float64
		wantRecs      int
		wantSource    string
	}{
		{
			name: "recommendations free three of four nodes",
			in: Input{Report: hubapi.CapacityReport{
				NodePools: []hubapi.CapacityNodePool{{Name: "general", Autoscaler: "karpenter"}},
				Nodes:     nodes(4, "general", "m5.xlarge", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "web", "general", 6, "1", "2Gi")},
			}, Recs: rec("shop", "web", 0.25, 0.5), Prices: xlPrice},
			pool: "general", wantNodes: 4, wantEstimated: 1,
			wantTypes:   []typeWant{{"m5.xlarge", 4, 1}},
			wantSavings: 3 * 0.2 * 730, wantRecs: 1, wantSource: "metrics",
		},
		{
			name: "without recommendations the pods still need their nodes",
			in: Input{Report: hubapi.CapacityReport{
				Nodes:     nodes(2, "general", "m5.xlarge", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "web", "general", 6, "1", "2Gi")},
			}, Prices: xlPrice},
			pool: "general", wantNodes: 2, wantEstimated: 2, wantTypes: []typeWant{{"m5.xlarge", 2, 2}}, wantSource: "metrics",
		},
		{
			name: "daemonset overhead is reserved on every node",
			in: Input{Report: hubapi.CapacityReport{
				Nodes: nodes(3, "general", "m5.xlarge", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{
					grp("shop", "web", "general", 4, "1500m", "1Gi"),
					ds("fluent", "general", 3, "1", "256Mi"),
				},
			}, Prices: xlPrice},
			// room per node 3 cores: two 1.5-core pods per node
			pool: "general", wantNodes: 3, wantEstimated: 2, wantTypes: []typeWant{{"m5.xlarge", 3, 2}},
			wantSavings: 0.2 * 730, wantSource: "metrics",
		},
		{
			name: "the pool minimum is kept",
			in: Input{Report: hubapi.CapacityReport{
				NodePools: []hubapi.CapacityNodePool{{Name: "ng", Autoscaler: "cluster-autoscaler", MinNodes: ptrI(3)}},
				Nodes:     nodes(4, "ng", "m5.xlarge", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "web", "ng", 2, "500m", "1Gi")},
			}, Prices: xlPrice},
			pool: "ng", wantNodes: 4, wantEstimated: 3, wantTypes: []typeWant{{"m5.xlarge", 4, 3}},
			wantSavings: 0.2 * 730, wantSource: "metrics",
		},
		{
			name: "each node gets the cheapest type its pods fit",
			in: Input{Report: hubapi.CapacityReport{
				Nodes: append(nodes(1, "general", "m5.4xlarge", "16", 64), nodes(1, "general", "m5.large", "2", 8)...),
				PodGroups: []hubapi.CapacityPodGroup{
					grp("shop", "big", "general", 1, "10", "20Gi"),
					grp("shop", "small", "general", 1, "1", "1Gi"),
				},
			}, Prices: xlPrice},
			// FFD packs both pods into one 4xlarge (11 cores); the large node goes.
			pool: "general", wantNodes: 2, wantEstimated: 1,
			wantTypes:   []typeWant{{"m5.4xlarge", 1, 1}, {"m5.large", 1, 0}},
			wantSavings: 0.1 * 730, wantSource: "metrics",
		},
		{
			name: "small load moves to the cheaper type",
			in: Input{Report: hubapi.CapacityReport{
				Nodes:     append(nodes(1, "general", "m5.4xlarge", "16", 64), nodes(1, "general", "m5.large", "2", 8)...),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "small", "general", 1, "1", "1Gi")},
			}, Prices: xlPrice},
			pool: "general", wantNodes: 2, wantEstimated: 1,
			wantTypes:   []typeWant{{"m5.4xlarge", 1, 0}, {"m5.large", 1, 1}},
			wantSavings: 0.8 * 730, wantSource: "metrics",
		},
		{
			name: "a pod larger than every type is unplaced and counted as one node",
			in: Input{Report: hubapi.CapacityReport{
				Nodes:     nodes(2, "general", "m5.xlarge", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "huge", "general", 1, "8", "1Gi"), grp("shop", "web", "general", 1, "1", "1Gi")},
			}, Prices: xlPrice},
			pool: "general", wantNodes: 2, wantEstimated: 2, wantUnplaced: 1, wantTypes: []typeWant{{"m5.xlarge", 2, 2}}, wantSource: "metrics",
		},
		{
			name: "types the pool no longer allows are not chosen",
			in: Input{Report: hubapi.CapacityReport{
				NodePools: []hubapi.CapacityNodePool{{Name: "general", Autoscaler: "karpenter", InstanceTypes: []string{"m5.xlarge"}}},
				Nodes:     append(nodes(1, "general", "m5.large", "2", 8), nodes(1, "general", "m5.xlarge", "4", 16)...),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "web", "general", 1, "1", "1Gi")},
			}, Prices: xlPrice},
			pool: "general", wantNodes: 2, wantEstimated: 1,
			wantTypes:   []typeWant{{"m5.large", 1, 0}, {"m5.xlarge", 1, 1}},
			wantSavings: 0.1 * 730, wantSource: "metrics",
		},
		{
			name: "without a pricing series the rates price the nodes",
			in: Input{Report: hubapi.CapacityReport{
				Nodes:     nodes(2, "general", "custom", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{grp("shop", "web", "general", 1, "1", "1Gi")},
			}, Prices: Prices{CPUCoreHour: 0.03, MemoryGiBHour: 0.004}},
			pool: "general", wantNodes: 2, wantEstimated: 1, wantTypes: []typeWant{{"custom", 2, 1}},
			wantSavings: (4*0.03 + 16*0.004) * 730, wantSource: "rates",
		},
		{
			name: "an init container larger than the app sets the pod size",
			in: Input{Report: hubapi.CapacityReport{
				Nodes: nodes(2, "general", "m5.xlarge", "4", 16),
				PodGroups: []hubapi.CapacityPodGroup{func() hubapi.CapacityPodGroup {
					g := grp("shop", "web", "general", 2, "500m", "1Gi")
					g.InitRequests = &hubapi.ResourceValues{CPU: "3"}
					return g
				}()},
			}, Prices: xlPrice},
			pool: "general", wantNodes: 2, wantEstimated: 2, wantTypes: []typeWant{{"m5.xlarge", 2, 2}}, wantSource: "metrics",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Estimate(tt.in)
			var p *hubapi.CapacityPoolEstimate
			for i := range res.Pools {
				if res.Pools[i].Name == tt.pool {
					p = &res.Pools[i]
				}
			}
			if p == nil {
				t.Fatalf("pool %q missing in %+v", tt.pool, res.Pools)
			}
			if p.Nodes != tt.wantNodes || p.EstimatedNodes != tt.wantEstimated || p.Unplaced != tt.wantUnplaced {
				t.Errorf("nodes %d -> %d (unplaced %d), want %d -> %d (%d)", p.Nodes, p.EstimatedNodes, p.Unplaced, tt.wantNodes, tt.wantEstimated, tt.wantUnplaced)
			}
			var got []typeWant
			for _, it := range p.InstanceTypes {
				got = append(got, typeWant{it.InstanceType, it.Nodes, it.EstimatedNodes})
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.wantTypes) {
				t.Errorf("types %v, want %v", got, tt.wantTypes)
			}
			e := res.Estimate
			if e == nil {
				t.Fatal("no estimate")
			}
			if d := e.SavingsMonthlyUSD - round(tt.wantSavings, 2); d > 0.011 || d < -0.011 {
				t.Errorf("savings %.2f, want %.2f", e.SavingsMonthlyUSD, tt.wantSavings)
			}
			if e.Recommendations != tt.wantRecs || e.PriceSource != tt.wantSource || e.Method != "first-fit-decreasing" || e.MethodURL == "" {
				t.Errorf("estimate %+v", e)
			}
		})
	}
}

func TestEstimateTotals(t *testing.T) {
	res := Estimate(Input{Report: hubapi.CapacityReport{
		NodePools: []hubapi.CapacityNodePool{{Name: "idle", Autoscaler: "karpenter"}},
		Nodes:     nodes(2, "general", "m5.xlarge", "4", 16),
		PodGroups: []hubapi.CapacityPodGroup{grp("shop", "web", "general", 4, "1", "2Gi"), ds("fluent", "general", 2, "100m", "128Mi")},
	}, Recs: rec("shop", "web", 0.5, 1), Prices: xlPrice})
	want := hubapi.CapacityTotals{
		Nodes:          2,
		Allocatable:    hubapi.CapacityAmounts{CPUCores: 8, MemoryBytes: 32 * gi},
		Requested:      hubapi.CapacityAmounts{CPUCores: 4.2, MemoryBytes: 8*gi + 256*(1<<20)},
		RequestedAfter: hubapi.CapacityAmounts{CPUCores: 2.2, MemoryBytes: 4*gi + 256*(1<<20)},
	}
	if res.Totals != want {
		t.Errorf("totals %+v\nwant %+v", res.Totals, want)
	}
	if len(res.Pools) != 2 || res.Pools[1].Name != "idle" || res.Pools[1].EstimatedNodes != 0 {
		t.Errorf("pools %+v", res.Pools)
	}
	if res.Pools[0].DaemonSetOverhead == nil || res.Pools[0].DaemonSetOverhead.CPUCores != 0.1 {
		t.Errorf("overhead %+v", res.Pools[0].DaemonSetOverhead)
	}
}

func TestEstimateNoNodes(t *testing.T) {
	if res := Estimate(Input{}); res.Estimate != nil || len(res.Pools) != 0 {
		t.Errorf("got %+v", res)
	}
}
