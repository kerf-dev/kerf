package pricing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
kerf_node_hourly_cost_usd{capacity_type="on-demand",cluster="ro-spoke-1",instance_type="m6i.xlarge",node="n1",provider="static",region="eu-west-1",source="static",zone="eu-west-1a"} 0.214
kerf_node_hourly_cost_usd{capacity_type="spot",cluster="ro-spoke-2",instance_type="c6a.2xlarge",node="n2",provider="static",region="eu-west-1",source="static",zone="eu-west-1b"} 0.1356
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

func TestExporterNamesUnpricedInstanceType(t *testing.T) {
	static, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	nodes := &fakeNodes{nodes: []Node{
		{Cluster: "c", Name: "priced", InstanceType: "c7g.xlarge", CapacityType: OnDemand, Region: "us-east-1", Zone: "us-east-1a", AllocatableCores: 3.9, AllocatableGiB: 7},
		{Cluster: "c", Name: "unpriced", InstanceType: "c9g.xlarge", CapacityType: Spot, Region: "us-east-1", Zone: "us-east-1a", AllocatableCores: 3.9, AllocatableGiB: 7},
	}}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	prov := NewCached(static, time.Hour, nil)
	e := NewExporter(nodes, prov, &Splitter{Provider: prov}, log)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.Costs()) != 1 {
		t.Fatalf("priced %d nodes, want 1", len(e.Costs()))
	}
	out := buf.String()
	for _, want := range []string{"no price for node", "instance_type=c9g.xlarge", "us-east-1/c9g.xlarge/spot/us-east-1a", "no-price:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
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

func TestExporterGPUCost(t *testing.T) {
	static, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	nodes := &fakeNodes{nodes: []Node{
		{Cluster: "synth-gpu", Name: "gpu-1", InstanceType: "g5.12xlarge", CapacityType: OnDemand, Region: "eu-west-1", AllocatableCores: 47, AllocatableGiB: 180},
		{Cluster: "synth-gpu", Name: "cpu-1", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", AllocatableCores: 4, AllocatableGiB: 15.5},
	}}
	prov := NewCached(static, time.Hour, nil)
	e := NewExporter(nodes, prov, &Splitter{Provider: prov}, nil)
	reg := prometheus.NewRegistry()
	if err := e.Register(reg, prometheus.WrapRegistererWith(prometheus.Labels{"cluster": "ro-hub"}, reg)); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var gpu NodeCost
	for _, c := range e.Costs() {
		switch c.Name {
		case "gpu-1":
			gpu = c
		case "cpu-1":
			if c.GPUs != 0 || c.GPURate != 0 {
				t.Errorf("CPU node has GPU cost: %+v", c)
			}
		}
	}
	if gpu.GPUs != 4 || math.Abs(gpu.GPURate-6.240/4) > 1e-9 {
		t.Fatalf("gpu node: %d GPUs at %v per GPU-hour, want 4 at %v", int(gpu.GPUs), gpu.GPURate, 6.240/4)
	}
	// Only GPU nodes export the GPU series.
	if n := testutil.CollectAndCount(e, "kerf_gpu_hourly_cost_usd"); n != 1 {
		t.Errorf("kerf_gpu_hourly_cost_usd series: %d, want 1", n)
	}
	if n := testutil.CollectAndCount(e, "kerf_node_gpus"); n != 1 {
		t.Errorf("kerf_node_gpus series: %d, want 1", n)
	}
	if n := testutil.CollectAndCount(e, "kerf_node_hourly_cost_usd"); n != 2 {
		t.Errorf("kerf_node_hourly_cost_usd series: %d, want 2", n)
	}
}

// The GPU share follows the rate source: a CUR rate for a GPU instance type is split over its GPUs.
func TestExporterGPUCostFromCUR(t *testing.T) {
	static, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	c := NewCUR(&fakeReader{}, static, nil)
	c.Now = func() time.Time { return now }
	c.rates = &CURRates{Rates: map[Key]CURRate{
		{Region: "eu-west-1", InstanceType: "g5.12xlarge", CapacityType: OnDemand}: {HourlyUSD: 4.0},
	}, Start: now.Add(-24 * time.Hour), End: now}
	c.readAt = now
	nodes := &fakeNodes{nodes: []Node{
		{Cluster: "synth-gpu", Name: "gpu-1", InstanceType: "g5.12xlarge", CapacityType: OnDemand, Region: "eu-west-1", AllocatableCores: 47, AllocatableGiB: 180},
	}}
	e := NewExporter(nodes, c, &Splitter{Provider: c}, nil)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	costs := e.Costs()
	if len(costs) != 1 || costs[0].Source != SourceCUR || costs[0].GPUs != 4 || math.Abs(costs[0].GPURate-1.0) > 1e-9 {
		t.Fatalf("costs = %+v, want one CUR-priced node with 4 GPUs at 1.0 per GPU-hour", costs)
	}
}
