package recommender

import (
	"context"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Loader loads raw cluster data (Source implements it).
type Loader interface {
	Load(ctx context.Context, cluster string, end time.Time, window time.Duration) (*ClusterData, error)
}

// Engine computes recommendations for whole clusters.
type Engine struct {
	Loader   Loader
	Cfg      Config
	Strategy Strategy
	// ProfilesInUse returns the profiles reported for a cluster's workloads; the data window is the
	// longest of these and the default profile. nil means every configured profile.
	ProfilesInUse func(cluster string) []string
	// JVM returns the JVM containers the agent reported for a workload; reported is false when the
	// agent never reported it (JVMs are then detected by image). nil: no agent reports (WO-12).
	JVM func(hubapi.WorkloadKey) (containers []hubapi.JVMContainer, reported bool)
	// PricingFallback is asked for the cluster's rates when it has no pricing series at the end of
	// the window (a backfill before the series existed, or a gap); ok false keeps the configured
	// default rates (WO-04d).
	PricingFallback func(ctx context.Context, cluster string, end time.Time) (p Pricing, ok bool)
}

// ClusterResult is the outcome of one cluster in a run.
type ClusterResult struct {
	Cluster string
	End     time.Time
	Pricing Pricing
	// Inventory has every workload found, with or without a recommendation.
	Inventory []WorkloadInventory
	Results   []Result
	// WithRequests is the number of inventory workloads with request series (KSM); a sudden drop
	// means incomplete metrics rather than workloads without requests.
	WithRequests int
}

// WorkloadInventory is a discovered workload plus attributes stored by the hub.
type WorkloadInventory struct {
	Key             hubapi.WorkloadKey
	Containers      []string
	AverageReplicas float64
	HPA             *HPA
	// Used24hCPU / Used24hMemory: mean usage over the last 24 h summed over pods and containers.
	Used24hCPU, Used24hMemory float64
}

// RunCluster recommends for every workload of a cluster. settings returns the agent-reported
// settings of a workload (nil when never reported).
func (e *Engine) RunCluster(ctx context.Context, cluster string, end time.Time, settings func(hubapi.WorkloadKey) *hubapi.ValueSettings) (*ClusterResult, error) {
	window := time.Duration(0)
	if e.ProfilesInUse == nil {
		for _, p := range e.Cfg.Profiles {
			window = max(window, p.Window)
		}
	} else {
		window = e.Cfg.Profiles[e.Cfg.DefaultSettings.Profile].Window
		for _, name := range e.ProfilesInUse(cluster) {
			if p, ok := e.Cfg.Profiles[name]; ok {
				window = max(window, p.Window)
			}
		}
	}
	d, err := e.Loader.Load(ctx, cluster, end, window)
	if err != nil {
		return nil, err
	}
	if d.Pricing.Fallback() && e.PricingFallback != nil {
		if p, ok := e.PricingFallback(ctx, cluster, d.End); ok {
			d.Pricing = p
		}
	}
	res := &ClusterResult{Cluster: cluster, End: d.End, Pricing: d.Pricing}
	for _, k := range d.SortedKeys() {
		raw := d.Workloads[k]
		var reported *hubapi.ValueSettings
		if settings != nil {
			reported = settings(k)
		}
		s := e.Cfg.EffectiveSettings(reported)
		p := e.Cfg.Profiles[s.Profile]
		data := raw.Data(p, d.End, e.Cfg)
		var jvm []hubapi.JVMContainer
		jvmReported := false
		if e.JVM != nil {
			jvm, jvmReported = e.JVM(k)
		}
		attachJVM(&data, raw, jvm, jvmReported, e.Cfg.JVM)
		inv := WorkloadInventory{Key: k, AverageReplicas: data.AverageReplicas, HPA: raw.HPA, Containers: data.ContainerNames()}
		for _, c := range data.Containers {
			inv.Used24hCPU += c.Used24hCPU
			inv.Used24hMemory += c.Used24hMemory
		}
		if len(inv.Containers) == 0 {
			for n := range raw.Containers {
				inv.Containers = append(inv.Containers, n)
			}
		}
		res.Inventory = append(res.Inventory, inv)
		if raw.HasRequests {
			res.WithRequests++
		}
		r, ok := Recommend(Request{Workload: data, Settings: s, Profile: p, Pricing: d.Pricing, End: d.End}, e.Strategy, e.Cfg)
		if ok {
			res.Results = append(res.Results, r)
		}
	}
	return res, nil
}
