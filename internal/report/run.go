package report

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Row is one workload of the report.
type Row struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Workload  string `json:"workload"`
	// Replicas is the average number of running pods over the window.
	Replicas float64 `json:"averageReplicas"`
	// CPU and memory requests per pod, summed over the containers: current and recommended.
	CPURequestCores        float64 `json:"cpuRequestCores"`
	CPURecommendedCores    float64 `json:"cpuRecommendedCores"`
	MemoryRequestBytes     float64 `json:"memoryRequestBytes"`
	MemoryRecommendedBytes float64 `json:"memoryRecommendedBytes"`
	// Monthly saving in USD at the report's rates.
	MonthlySavingsUSD hubapi.Savings `json:"monthlySavingsUSD"`
	Confidence        float64        `json:"confidence"`
	RiskFlags         []string       `json:"riskFlags"`
	// HPA is the HPA coordination of hpa-coupled workloads: the requests and the HPA target change together.
	HPA *hubapi.HPACoordination `json:"hpa,omitempty"`
	// Containers are the per-container recommendations as the hub shows them.
	Containers []hubapi.ContainerRecommendation `json:"containers,omitempty"`
}

// Report is the result of one run.
type Report struct {
	Cluster     string    `json:"cluster"`
	GeneratedAt time.Time `json:"generatedAt"`
	Profile     string    `json:"profile"`
	// Window is the usage window of the profile.
	Window  string  `json:"window"`
	Pricing Pricing `json:"pricing"`
	Rows    []Row   `json:"workloads"`
	// Totals cover all rows.
	Totals Totals `json:"totals"`
	// Warnings are things to know before trusting the numbers.
	Warnings []string `json:"warnings,omitempty"`
}

// Pricing are the rates the savings use.
type Pricing struct {
	CPUCoreHourUSD   float64 `json:"cpuCoreHourUSD"`
	MemoryGiBHourUSD float64 `json:"memoryGiBHourUSD"`
	// Source is where the rates come from: default rates, a static price table, the AWS Pricing
	// API, or the cluster's own kerf_* price series.
	Source string `json:"source"`
	Nodes  int    `json:"nodes,omitempty"`
}

// Totals are the sums over the rows.
type Totals struct {
	Workloads         int     `json:"workloads"`
	MonthlySavingsUSD float64 `json:"monthlySavingsUSD"`
	// WithoutRecommendation counts workloads found without usage data in the window.
	WithoutRecommendation int `json:"workloadsWithoutRecommendation"`
}

// Deps are the replaceable parts of a run; the zero value is the real thing.
type Deps struct {
	// Querier replaces the HTTP client of the metrics source (tests).
	Querier recommender.Querier
	// Credentials replaces the AWS credential chain of SigV4 (tests).
	Credentials datasource.CredentialsFunc
	// HTTPClient replaces the authenticated client built from Options.Auth (tests).
	HTTPClient *http.Client
	// Kube replaces the client built from the kubeconfig (tests).
	Kube kubeAPI
	Log  *slog.Logger
}

// Run loads the usage history from the metrics source and computes the report. It writes nothing
// to the metrics source or the cluster.
func Run(ctx context.Context, o Options, d Deps) (*Report, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	matcher, err := o.Matcher()
	if err != nil {
		return nil, err
	}
	cluster := o.ClusterName()

	hc := d.HTTPClient
	if hc == nil {
		hc, err = o.datasourceConfig().HTTPClient(ctx, d.Credentials)
		if err != nil {
			return nil, fmt.Errorf("metrics source: %w", err)
		}
	}
	q := d.Querier
	if q == nil {
		q = &recommender.PromClient{BaseURL: trimURL(o.MetricsURL), HTTP: hc, MaxPointsPerQuery: 2016}
	}

	cfg := recommender.DefaultConfig()
	cfg.DefaultSettings.Profile = o.Profile
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	profile := cfg.Profiles[o.Profile]

	var warnings []string
	var rates *Rates
	if o.Prices != "" {
		p, source, err := provider(ctx, o.Prices)
		if err != nil {
			return nil, err
		}
		rates, err = nodeRates(ctx, o, hc, p, source, log)
		if err != nil {
			return nil, fmt.Errorf("prices: %w", err)
		}
		switch {
		case rates.Nodes == 0:
			warnings = append(warnings, "no node could be priced (kube_node_labels and kube_node_status_allocatable are needed, and every instance type must be in the price table); savings use the default rates")
		case rates.Skipped > 0:
			warnings = append(warnings, fmt.Sprintf("%d node(s) could not be priced and are left out of the rates", rates.Skipped))
		}
	}

	src := &recommender.Source{
		Q: q, Cfg: cfg, Concurrency: 2,
		Binder: func(context.Context, string) (recommender.Querier, *datasource.Matcher, error) {
			return q, &matcher, nil
		},
	}
	eng := &recommender.Engine{
		Loader: src, Cfg: cfg, Strategy: recommender.Strategies["percentile"],
		ProfilesInUse: func(string) []string { return []string{o.Profile} },
	}
	if rates != nil {
		eng.AdjustPricing = rates.adjust
	}

	if o.Kube || d.Kube != nil {
		kr, err := newKubeReader(ctx, o, d.Kube, cluster)
		if err != nil {
			return nil, err
		}
		eng.Loader = kubeLoader{inner: src, kube: kr}
		eng.JVM = kr.jvmFor
	}

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	res, err := eng.RunCluster(ctx, cluster, now, nil)
	if err != nil {
		return nil, fmt.Errorf("read usage from the metrics source: %w", err)
	}
	return build(o, profile, res, rates, warnings), nil
}

type kubeAPI = kubernetes.Interface

func newKubeReader(ctx context.Context, o Options, api kubeAPI, cluster string) (*kubeReader, error) {
	if api == nil {
		c, err := newKubeClient(o.Kubeconfig, o.KubeContext)
		if err != nil {
			return nil, err
		}
		api = c
	}
	kr := &kubeReader{client: api, namespaces: o.Namespaces, cluster: cluster}
	if err := kr.load(ctx); err != nil {
		return nil, fmt.Errorf("read the cluster (needs list on HPAs, Deployments, StatefulSets, DaemonSets and CronJobs): %w", err)
	}
	return kr, nil
}

// build turns the engine's result into rows sorted by monthly saving.
func build(o Options, profile recommender.Profile, res *recommender.ClusterResult, rates *Rates, warnings []string) *Report {
	r := &Report{
		Cluster: res.Cluster, GeneratedAt: res.End.UTC(), Profile: o.Profile, Window: profile.Window.String(),
		Pricing:  Pricing{CPUCoreHourUSD: res.Pricing.CPUCoreHour, MemoryGiBHourUSD: res.Pricing.MemoryGiBHour, Nodes: res.Pricing.Nodes},
		Warnings: warnings,
	}
	switch {
	case res.Pricing.Source == recommender.PricingSourceMetrics && rates != nil && rates.Nodes > 0:
		r.Pricing.Source = rates.Source
	case res.Pricing.Source == recommender.PricingSourceMetrics:
		r.Pricing.Source = "kerf_*_hourly_cost_usd series of the metrics source"
	default:
		r.Pricing.Source = PriceSourceDefault
		if o.Prices == "" {
			r.Warnings = append(r.Warnings, "savings use default rates (an m6i.xlarge in eu-west-1); pass --prices to price your own nodes")
		}
	}
	inScope := func(ns string) bool { return len(o.Namespaces) == 0 || slices.Contains(o.Namespaces, ns) }
	for _, w := range res.Inventory {
		if inScope(w.Key.Namespace) && w.Live && !hasResult(res, w.Key) {
			r.Totals.WithoutRecommendation++
		}
	}
	for _, x := range res.Results {
		rec := x.Recommendation
		if !inScope(rec.Workload.Namespace) {
			continue
		}
		row := Row{
			Namespace: rec.Workload.Namespace, Kind: rec.Workload.Kind, Workload: rec.Workload.Name,
			Replicas: rec.AverageReplicas, MonthlySavingsUSD: rec.MonthlySavingsUSD,
			Confidence: rec.Confidence, RiskFlags: rec.RiskFlags, Containers: rec.Containers, HPA: rec.HPA,
		}
		for _, c := range rec.Containers {
			if c.Init {
				continue
			}
			row.CPURequestCores += quantity(c.Current.Requests, true)
			row.CPURecommendedCores += quantity(c.Recommended.Requests, true)
			row.MemoryRequestBytes += quantity(c.Current.Requests, false)
			row.MemoryRecommendedBytes += quantity(c.Recommended.Requests, false)
		}
		r.Rows = append(r.Rows, row)
		r.Totals.MonthlySavingsUSD += rec.MonthlySavingsUSD.Total
	}
	sort.SliceStable(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		if a.MonthlySavingsUSD.Total != b.MonthlySavingsUSD.Total {
			return a.MonthlySavingsUSD.Total > b.MonthlySavingsUSD.Total
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Workload < b.Workload
	})
	r.Totals.Workloads = len(r.Rows)
	return r
}

func hasResult(res *recommender.ClusterResult, k hubapi.WorkloadKey) bool {
	return slices.ContainsFunc(res.Results, func(x recommender.Result) bool {
		return x.Recommendation.Workload == k.Ref()
	})
}

// quantity returns the CPU (cores) or memory (bytes) of a ResourceValues; 0 when unset.
func quantity(v *hubapi.ResourceValues, cpu bool) float64 {
	if v == nil {
		return 0
	}
	var f float64
	if cpu {
		f, _ = recommender.ParseCPU(v.CPU)
	} else {
		f, _ = recommender.ParseMemory(v.Memory)
	}
	return f
}

func trimURL(u string) string {
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}
