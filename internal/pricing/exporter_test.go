package pricing

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeNodes struct {
	nodes []Node
	err   error
}

func (f *fakeNodes) Nodes(context.Context) ([]Node, error) { return f.nodes, f.err }

func TestExporter(t *testing.T) {
	static, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	nodes := &fakeNodes{nodes: []Node{
		{Cluster: "ro-spoke-1", Name: "n1", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", Zone: "eu-west-1a", AllocatableCores: 4, AllocatableGiB: 15.5},
		{Cluster: "ro-spoke-2", Name: "n2", InstanceType: "c6a.2xlarge", CapacityType: Spot, Region: "eu-west-1", Zone: "eu-west-1b", AllocatableCores: 8, AllocatableGiB: 15},
		{Cluster: "synth-a", Name: "s1", InstanceType: "", CapacityType: OnDemand, Region: "eu-west-1", AllocatableCores: 1, AllocatableGiB: 1},
		{Cluster: "synth-a", Name: "s2", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "", AllocatableCores: 1, AllocatableGiB: 1},
		{Cluster: "synth-a", Name: "s3", InstanceType: "m6i.xlarge", CapacityType: "", Region: "eu-west-1", AllocatableCores: 1, AllocatableGiB: 1},
		{Cluster: "synth-a", Name: "s4", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1"},
		{Cluster: "synth-b", Name: "s5", InstanceType: "z1d.large", CapacityType: OnDemand, Region: "eu-west-1", AllocatableCores: 1, AllocatableGiB: 1},
	}}
	prov := NewCached(static, time.Hour, nil)
	e := NewExporter(nodes, prov, &Splitter{Provider: prov}, nil)

	reg := prometheus.NewRegistry()
	if err := e.Register(reg, prometheus.WrapRegistererWith(prometheus.Labels{"cluster": "ro-hub"}, reg)); err != nil {
		t.Fatal(err)
	}
	if e.Ready() {
		t.Fatal("ready before first refresh")
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.Ready() {
		t.Fatal("not ready after refresh")
	}

	costs := e.Costs()
	if len(costs) != 2 {
		t.Fatalf("priced %d nodes, want 2: %+v", len(costs), costs)
	}
	for _, c := range costs {
		// Contract invariant.
		if got := c.CPURate*c.AllocatableCores + c.MemRate*c.AllocatableGiB; math.Abs(got-c.NodeUSD) > 1e-9 {
			t.Errorf("%s: split sums to %v, node price %v", c.Name, got, c.NodeUSD)
		}
		if c.CPURate <= c.MemRate {
			t.Errorf("%s: vCPU rate %v not above GiB rate %v", c.Name, c.CPURate, c.MemRate)
		}
	}
	if costs[0].NodeUSD != 0.214 || costs[1].NodeUSD != 0.1356 {
		t.Errorf("node prices %v, %v; want 0.214 (on-demand) and 0.1356 (spot eu-west-1b)", costs[0].NodeUSD, costs[1].NodeUSD)
	}

	if n := testutil.CollectAndCount(e); n != 6 {
		t.Fatalf("exported %d cost series, want 6", n)
	}
	want := `
# HELP kerf_node_hourly_cost_usd Hourly price of the whole node in USD.
# TYPE kerf_node_hourly_cost_usd gauge
kerf_node_hourly_cost_usd{capacity_type="on-demand",cluster="ro-spoke-1",instance_type="m6i.xlarge",node="n1",provider="static",region="eu-west-1",zone="eu-west-1a"} 0.214
kerf_node_hourly_cost_usd{capacity_type="spot",cluster="ro-spoke-2",instance_type="c6a.2xlarge",node="n2",provider="static",region="eu-west-1",zone="eu-west-1b"} 0.1356
# HELP kerf_pricing_nodes Nodes seen in the last refresh by status (priced|skipped) and skip reason.
# TYPE kerf_pricing_nodes gauge
kerf_pricing_nodes{cluster="ro-hub",reason="",status="priced"} 2
kerf_pricing_nodes{cluster="ro-hub",reason="no-allocatable",status="skipped"} 1
kerf_pricing_nodes{cluster="ro-hub",reason="no-instance-type",status="skipped"} 1
kerf_pricing_nodes{cluster="ro-hub",reason="no-price",status="skipped"} 1
kerf_pricing_nodes{cluster="ro-hub",reason="no-region",status="skipped"} 1
kerf_pricing_nodes{cluster="ro-hub",reason="unknown-capacity-type",status="skipped"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kerf_node_hourly_cost_usd", "kerf_pricing_nodes"); err != nil {
		t.Fatal(err)
	}

	// A discovery failure keeps the previous prices and counts an error.
	nodes.err = errors.New("vm down")
	if err := e.Refresh(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if len(e.Costs()) != 2 {
		t.Fatal("prices dropped after a failed refresh")
	}
	if v := testutil.ToFloat64(e.refreshes.WithLabelValues("error")); v != 1 {
		t.Fatalf("error refreshes = %v", v)
	}
}

type errProvider struct{}

func (errProvider) Name() string { return "aws" }
func (errProvider) Price(context.Context, Key) (Price, error) {
	return Price{}, errors.New("throttled")
}

func TestExporterProviderErrorAndRun(t *testing.T) {
	nodes := &fakeNodes{nodes: []Node{{Cluster: "c", Name: "n", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", AllocatableCores: 4, AllocatableGiB: 16}}}
	e := NewExporter(nodes, errProvider{}, &Splitter{Provider: errProvider{}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx, time.Hour); close(done) }()
	deadline := time.After(5 * time.Second)
	for !e.Ready() {
		select {
		case <-deadline:
			t.Fatal("Run did not refresh")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if v := testutil.ToFloat64(e.nodeCount.WithLabelValues("skipped", ReasonProviderError)); v != 1 {
		t.Fatalf("provider-error nodes = %v", v)
	}
	if len(e.Costs()) != 0 {
		t.Fatal("priced a node despite provider error")
	}
}
