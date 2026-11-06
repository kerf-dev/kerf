package pricing

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// NodeLister lists the nodes to price (Discovery in production).
type NodeLister interface {
	Nodes(ctx context.Context) ([]Node, error)
}

// NodeCost is a priced node.
type NodeCost struct {
	Node
	Provider string
	// Source is the rate's source (Source* constants), the `source` label.
	Source  string
	NodeUSD float64
	CPURate float64 // USD per allocatable vCPU-hour
	MemRate float64 // USD per allocatable GiB-hour
	Weight  float64
	// GPUs is the number of GPUs of the instance type and GPURate the node price per GPU-hour
	// (the whole node price over its GPUs, so a pod's GPU cost is its share of the node's GPUs); both
	// are 0 for nodes without GPUs.
	GPUs    float64
	GPURate float64
}

// Reasons a node is not priced (label `reason` of kerf_pricing_nodes).
const (
	ReasonNoInstanceType      = "no-instance-type"
	ReasonNoRegion            = "no-region"
	ReasonUnknownCapacityType = "unknown-capacity-type"
	ReasonNoAllocatable       = "no-allocatable"
	ReasonNoPrice             = "no-price"
	ReasonProviderError       = "provider-error"
)

var costLabels = []string{"cluster", "node", "instance_type", "capacity_type", "region", "zone", "provider", "source"}

var (
	nodeCostDesc = prometheus.NewDesc("kerf_node_hourly_cost_usd",
		"Hourly price of the whole node in USD.", costLabels, nil)
	cpuCostDesc = prometheus.NewDesc("kerf_cpu_core_hourly_cost_usd",
		"CPU share of the node price per allocatable vCPU, in USD per hour.", costLabels, nil)
	memCostDesc = prometheus.NewDesc("kerf_memory_gib_hourly_cost_usd",
		"Memory share of the node price per allocatable GiB, in USD per hour.", costLabels, nil)
	gpuCostDesc = prometheus.NewDesc("kerf_gpu_hourly_cost_usd",
		"Node price per GPU of the instance type (node price over its GPU count), in USD per hour. Only nodes with GPUs.", costLabels, nil)
	nodeGPUsDesc = prometheus.NewDesc("kerf_node_gpus",
		"GPUs of the node's instance type. Only nodes with GPUs.", costLabels, nil)
)

// Exporter periodically prices every discovered node and exports the kerf_*_hourly_cost_usd series.
type Exporter struct {
	Nodes    NodeLister
	Provider Provider
	Splitter *Splitter
	Log      *slog.Logger

	mu    sync.RWMutex
	costs []NodeCost
	ready bool

	refreshes   *prometheus.CounterVec
	lastSuccess prometheus.Gauge
	duration    prometheus.Gauge
	nodeCount   *prometheus.GaugeVec
}

// NewExporter wires an exporter. Register it with Register before serving /metrics.
func NewExporter(nodes NodeLister, p Provider, s *Splitter, log *slog.Logger) *Exporter {
	if log == nil {
		log = slog.Default()
	}
	return &Exporter{
		Nodes: nodes, Provider: p, Splitter: s, Log: log,
		refreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kerf_pricing_refreshes_total", Help: "Pricing refreshes by result (success|error).",
		}, []string{"result"}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_last_success_timestamp_seconds", Help: "Unix time of the last successful refresh.",
		}),
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_refresh_duration_seconds", Help: "Duration of the last refresh.",
		}),
		nodeCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kerf_pricing_nodes", Help: "Nodes seen in the last refresh by status (priced|skipped) and skip reason.",
		}, []string{"status", "reason"}),
	}
}

// Register registers the cost series on costReg (whose series carry their own `cluster` label) and the
// operational kerf_pricing_* series on opsReg (which should add cluster=<the hub's name>).
func (e *Exporter) Register(costReg, opsReg prometheus.Registerer) error {
	if err := costReg.Register(e); err != nil {
		return err
	}
	for _, c := range []prometheus.Collector{e.refreshes, e.lastSuccess, e.duration, e.nodeCount} {
		if err := opsReg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// Ready reports whether at least one refresh has succeeded.
func (e *Exporter) Ready() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ready
}

// Costs returns the last priced nodes.
func (e *Exporter) Costs() []NodeCost {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]NodeCost(nil), e.costs...)
}

// RetryInterval is the wait after a failed refresh, when shorter than the refresh interval.
const RetryInterval = 15 * time.Second

// Run refreshes immediately and then every interval until ctx is done. After a failed refresh it
// retries sooner (RetryInterval).
func (e *Exporter) Run(ctx context.Context, interval time.Duration) {
	for {
		wait := interval
		if err := e.Refresh(ctx); err != nil && ctx.Err() == nil {
			e.Log.ErrorContext(ctx, "pricing refresh failed; keeping previous prices", "err", err)
			wait = min(interval, RetryInterval)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Refresh discovers nodes and prices them. If discovery fails, the previous prices stay exported.
func (e *Exporter) Refresh(ctx context.Context) error {
	start := time.Now()
	nodes, err := e.Nodes.Nodes(ctx)
	if err != nil {
		e.refreshes.WithLabelValues("error").Inc()
		return err
	}
	costs := make([]NodeCost, 0, len(nodes))
	skipped := map[string]int{}
	for _, n := range nodes {
		c, reason := e.price(ctx, n)
		if reason != "" {
			skipped[reason]++
			continue
		}
		costs = append(costs, c)
	}

	e.mu.Lock()
	e.costs = costs
	e.ready = true
	e.mu.Unlock()

	e.nodeCount.Reset()
	e.nodeCount.WithLabelValues("priced", "").Set(float64(len(costs)))
	for reason, n := range skipped {
		e.nodeCount.WithLabelValues("skipped", reason).Set(float64(n))
	}
	e.refreshes.WithLabelValues("success").Inc()
	e.lastSuccess.SetToCurrentTime()
	e.duration.Set(time.Since(start).Seconds())
	e.Log.InfoContext(ctx, "pricing refreshed", "nodes", len(nodes), "priced", len(costs), "skipped", skipped, "took", time.Since(start).Round(time.Millisecond).String())
	return nil
}

func (e *Exporter) price(ctx context.Context, n Node) (NodeCost, string) {
	log := e.Log.With("cluster", n.Cluster, "node", n.Name, "instance_type", n.InstanceType)
	switch {
	case n.InstanceType == "":
		log.DebugContext(ctx, "skipping node without instance type label")
		return NodeCost{}, ReasonNoInstanceType
	case n.Region == "":
		log.WarnContext(ctx, "skipping node without region (set --default-region)")
		return NodeCost{}, ReasonNoRegion
	case n.CapacityType == "":
		log.WarnContext(ctx, "skipping node with unknown capacity type")
		return NodeCost{}, ReasonUnknownCapacityType
	case n.AllocatableCores <= 0 || n.AllocatableGiB <= 0:
		log.WarnContext(ctx, "skipping node without allocatable cpu/memory")
		return NodeCost{}, ReasonNoAllocatable
	}
	k := Key{Region: n.Region, Zone: n.Zone, InstanceType: n.InstanceType, CapacityType: n.CapacityType}
	p, err := e.Provider.Price(ctx, k)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			log.WarnContext(ctx, "no price for node", "key", k.String())
			return NodeCost{}, ReasonNoPrice
		}
		log.ErrorContext(ctx, "pricing node failed", "key", k.String(), "err", err)
		return NodeCost{}, ReasonProviderError
	}
	w, _ := e.Splitter.Weight(ctx, n.Region, n.InstanceType)
	cpu, mem, err := Split(p.HourlyUSD, n.AllocatableCores, n.AllocatableGiB, w)
	if err != nil {
		log.WarnContext(ctx, "cannot split node price", "err", err)
		return NodeCost{}, ReasonNoAllocatable
	}
	nc := NodeCost{Node: n, Provider: e.Provider.Name(), Source: p.Source, NodeUSD: p.HourlyUSD, CPURate: cpu, MemRate: mem, Weight: w}
	if p.GPUs > 0 {
		nc.GPUs, nc.GPURate = p.GPUs, p.HourlyUSD/p.GPUs
	}
	return nc, ""
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- nodeCostDesc
	ch <- cpuCostDesc
	ch <- memCostDesc
	ch <- gpuCostDesc
	ch <- nodeGPUsDesc
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	for _, c := range e.Costs() {
		lv := []string{c.Cluster, c.Name, c.InstanceType, string(c.CapacityType), c.Region, c.Zone, c.Provider, c.Source}
		ch <- prometheus.MustNewConstMetric(nodeCostDesc, prometheus.GaugeValue, c.NodeUSD, lv...)
		ch <- prometheus.MustNewConstMetric(cpuCostDesc, prometheus.GaugeValue, c.CPURate, lv...)
		ch <- prometheus.MustNewConstMetric(memCostDesc, prometheus.GaugeValue, c.MemRate, lv...)
		if c.GPUs > 0 {
			ch <- prometheus.MustNewConstMetric(gpuCostDesc, prometheus.GaugeValue, c.GPURate, lv...)
			ch <- prometheus.MustNewConstMetric(nodeGPUsDesc, prometheus.GaugeValue, c.GPUs, lv...)
		}
	}
}
