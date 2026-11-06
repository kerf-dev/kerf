package report

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/kerf-dev/kerf/internal/pricing"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// PricesAWS selects the AWS Pricing API as the price source.
const PricesAWS = "aws"

// Price sources shown in the report.
const (
	PriceSourceDefault = "default rates"
	PriceSourceStatic  = "static price table"
	PriceSourceAWS     = "AWS Pricing API"
)

// Rates are the CPU and memory prices of the cluster, derived from its nodes.
type Rates struct {
	CPUCoreHour, MemoryGiBHour float64
	// Nodes is the number of priced nodes the rates are averaged over.
	Nodes int
	// Skipped is the number of nodes that could not be priced.
	Skipped int
	Source  string
}

// provider returns the price provider for the --prices value.
func provider(ctx context.Context, spec string) (pricing.Provider, string, error) {
	if strings.EqualFold(spec, PricesAWS) {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("--prices aws: load AWS credentials: %w", err)
		}
		return pricing.NewCached(pricing.NewAWS(cfg, ""), time.Hour, nil), PriceSourceAWS, nil
	}
	st, err := pricing.LoadStatic(spec)
	if err != nil {
		return nil, "", fmt.Errorf("--prices %s: %w", spec, err)
	}
	return st, PriceSourceStatic, nil
}

// nodeRates discovers the cluster's nodes from the metrics source (kube_node_labels and
// kube_node_status_allocatable, like the pricing service), prices them with p, splits each node's
// price into CPU and memory rates like the pricing service, and averages them over the nodes like
// the recommender's pricing queries.
func nodeRates(ctx context.Context, o Options, hc *http.Client, p pricing.Provider, source string, log *slog.Logger) (*Rates, error) {
	m, err := o.Matcher()
	if err != nil {
		return nil, err
	}
	disc := &pricing.Discovery{
		VMURL: o.MetricsURL, HTTP: hc, DefaultRegion: o.DefaultRegion, Log: log,
		// Only the bound target below is read.
		ClusterRegex: regexp.QuoteMeta(o.ClusterName()),
		Targets:      singleTarget{pricing.Target{Cluster: o.ClusterName(), BaseURL: o.MetricsURL, HTTP: hc, Matcher: m}},
	}
	counted := &countingLister{inner: disc, cluster: o.ClusterName()}
	exp := pricing.NewExporter(counted, p, &pricing.Splitter{Provider: p, Log: log}, log)
	if err := exp.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("find the cluster's nodes: %w", err)
	}
	var cpu, mem float64
	n := 0
	for _, c := range exp.Costs() {
		if c.Cluster != o.ClusterName() {
			continue
		}
		cpu += c.CPURate
		mem += c.MemRate
		n++
	}
	r := &Rates{Nodes: n, Skipped: counted.n - n, Source: source}
	if n > 0 {
		r.CPUCoreHour, r.MemoryGiBHour = cpu/float64(n), mem/float64(n)
	}
	return r, nil
}

// countingLister remembers how many nodes discovery found.
type countingLister struct {
	inner   pricing.NodeLister
	cluster string
	n       int
}

func (c *countingLister) Nodes(ctx context.Context) ([]pricing.Node, error) {
	nodes, err := c.inner.Nodes(ctx)
	c.n = 0
	for _, n := range nodes {
		if n.Cluster == c.cluster {
			c.n++
		}
	}
	return nodes, err
}

type singleTarget struct{ t pricing.Target }

func (s singleTarget) Targets(context.Context) ([]pricing.Target, error) {
	return []pricing.Target{s.t}, nil
}

// adjust applies the computed rates to a cluster's pricing (recommender.Engine.AdjustPricing).
func (r *Rates) adjust(_ string, p recommender.Pricing) recommender.Pricing {
	if r == nil || r.Nodes == 0 || r.CPUCoreHour <= 0 || r.MemoryGiBHour <= 0 {
		return p
	}
	return recommender.Pricing{CPUCoreHour: r.CPUCoreHour, MemoryGiBHour: r.MemoryGiBHour, Source: recommender.PricingSourceMetrics, Nodes: r.Nodes}
}
