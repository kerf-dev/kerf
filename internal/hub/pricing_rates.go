package hub

import (
	"context"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Queries of the pricing service's own series in the hub's store (docs/contracts/metrics.md).
const (
	pricingRatesBySource = `count by (cluster, source) (max by (cluster, node, source) (last_over_time(kerf_cpu_core_hourly_cost_usd[10m])))`
	pricingCURStatus     = `max by (__name__) ({__name__=~"kerf_pricing_cur_(last_read_ok|last_success_timestamp_seconds|rates|period_start_timestamp_seconds|period_end_timestamp_seconds)"})`
)

// pricingRates reads where the pricing service's rates come from, for the Pricing page. It returns nil
// when the store cannot be queried: the settings stay usable without it.
func (s *Server) pricingRates(ctx context.Context) *hubapi.PricingRates {
	if s.Prom == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	bySource, err := s.Prom.Instant(ctx, pricingRatesBySource, now)
	if err != nil {
		s.Log.WarnContext(ctx, "pricing rate sources unavailable", "err", err)
		return nil
	}
	out := &hubapi.PricingRates{Clusters: []hubapi.ClusterPricingRates{}}
	clusters := map[string][]hubapi.PricingRateSource{}
	for _, r := range bySource {
		c, src := r.Labels["cluster"], r.Labels["source"]
		if c == "" || src == "" || r.Sample.V <= 0 {
			continue
		}
		clusters[c] = append(clusters[c], hubapi.PricingRateSource{Source: src, Nodes: int(r.Sample.V)})
	}
	for c, srcs := range clusters {
		sort.Slice(srcs, func(i, j int) bool { return srcs[i].Source < srcs[j].Source })
		out.Clusters = append(out.Clusters, hubapi.ClusterPricingRates{Cluster: c, Sources: srcs})
	}
	sort.Slice(out.Clusters, func(i, j int) bool { return out.Clusters[i].Cluster < out.Clusters[j].Cluster })

	cur, err := s.Prom.Instant(ctx, pricingCURStatus, now)
	if err != nil {
		s.Log.WarnContext(ctx, "pricing cost and usage report status unavailable", "err", err)
		return out
	}
	v := map[string]float64{}
	for _, r := range cur {
		v[r.Labels["__name__"]] = r.Sample.V
	}
	ok, enabled := v["kerf_pricing_cur_last_read_ok"]
	if !enabled { // the reader is off: the series do not exist
		return out
	}
	st := &hubapi.PricingCURStatus{LastReadOK: ok == 1, Rates: int(v["kerf_pricing_cur_rates"])}
	if ts := v["kerf_pricing_cur_last_success_timestamp_seconds"]; ts > 0 {
		t := time.Unix(int64(ts), 0).UTC()
		st.LastSuccess = &t
	}
	if a, b := v["kerf_pricing_cur_period_start_timestamp_seconds"], v["kerf_pricing_cur_period_end_timestamp_seconds"]; a > 0 && b > a && st.Rates > 0 {
		st.Period = &hubapi.PricingPeriod{Start: time.Unix(int64(a), 0).UTC(), End: time.Unix(int64(b), 0).UTC()}
	}
	out.CUR = st
	return out
}
