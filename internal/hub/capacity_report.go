package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/capacity"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
)

func (s *Server) capacityDB() *capacity.DB { return &capacity.DB{Pool: s.Store.Pool} }

// recordCapacity stores the agent's capacity report; nil = unchanged.
func (s *Server) recordCapacity(ctx context.Context, cluster string, rep *hubapi.CapacityReport) error {
	if rep == nil {
		return nil
	}
	return s.capacityDB().Save(ctx, cluster, rep, s.now())
}

// validateCapacity checks the limits and enums of a capacity report.
func validateCapacity(rep *hubapi.CapacityReport) error {
	if rep == nil {
		return nil
	}
	switch {
	case rep.ObservedAt.IsZero():
		return errors.New("capacity.observedAt is required")
	case len(rep.Autoscalers) > 10:
		return errors.New("capacity.autoscalers: at most 10 items")
	case len(rep.NodePools) > 1000:
		return errors.New("capacity.nodePools: at most 1000 items")
	case len(rep.Nodes) > 10000:
		return errors.New("capacity.nodes: at most 10000 items")
	case len(rep.PodGroups) > 50000:
		return errors.New("capacity.podGroups: at most 50000 items")
	case len(rep.PDBs) > 500:
		return errors.New("capacity.pdbs: at most 500 items")
	case len(rep.Pods) > 500:
		return errors.New("capacity.pods: at most 500 items")
	}
	for i, a := range rep.Autoscalers {
		if !slices.Contains(hubapi.NodeAutoscalerKinds, a.Kind) {
			return fmt.Errorf("capacity.autoscalers[%d]: unknown kind %q", i, a.Kind)
		}
	}
	for i, p := range rep.NodePools {
		if p.Name == "" || !slices.Contains(hubapi.NodeAutoscalerKinds, p.Autoscaler) {
			return fmt.Errorf("capacity.nodePools[%d]: name and a known autoscaler are required", i)
		}
	}
	for i, n := range rep.Nodes {
		if n.Name == "" {
			return fmt.Errorf("capacity.nodes[%d]: name is required", i)
		}
	}
	for i, g := range rep.PodGroups {
		if g.Count < 1 {
			return fmt.Errorf("capacity.podGroups[%d]: count must be at least 1", i)
		}
	}
	for i, p := range rep.Pods {
		for _, r := range p.Reasons {
			if !slices.Contains(hubapi.CapacityPodReasons, r) {
				return fmt.Errorf("capacity.pods[%d]: unknown reason %q", i, r)
			}
		}
	}
	return nil
}

// getClusterCapacity serves GET /v1/clusters/{cluster}/capacity.
func (s *Server) getClusterCapacity(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	out := hubapi.ClusterCapacity{
		Cluster: cluster, Autoscalers: []hubapi.CapacityAutoscaler{},
		Pools: []hubapi.CapacityPoolEstimate{}, Blockers: []hubapi.ConsolidationBlocker{},
	}
	st, ok, err := s.capacityDB().Load(r.Context(), cluster)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !ok {
		writeJSON(w, out)
		return
	}
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{Cluster: cluster})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	recs, known, rates := capacityRecs(views)
	prices := capacity.Prices{CPUCoreHour: s.RecCfg.DefaultCPUCoreHourUSD, MemoryGiBHour: s.RecCfg.DefaultMemoryGiBHourUSD}
	if rates != nil {
		prices.CPUCoreHour, prices.MemoryGiBHour = rates.CPUCoreHourUSD, rates.MemoryGiBHourUSD
	}
	prices.ByType = s.instancePrices(r.Context(), cluster)

	res := capacity.Estimate(capacity.Input{Report: st.Report, Recs: recs, Prices: prices})
	blockers, omitted := capacity.Blockers(st.Report, res.Needed, func(w hubapi.WorkloadRef) string {
		k := hubapi.WorkloadKey{Cluster: cluster, Namespace: w.Namespace, Kind: w.Kind, Name: w.Name}
		if known[k] {
			return k.ID()
		}
		return ""
	})
	reported, observed := st.ReportedAt.UTC(), st.Report.ObservedAt.UTC()
	out.ReportedAt, out.ObservedAt = &reported, &observed
	if st.Report.Autoscalers != nil {
		out.Autoscalers = st.Report.Autoscalers
	}
	out.Totals, out.Estimate, out.Pools = res.Totals, res.Estimate, res.Pools
	out.Blockers, out.BlockersOmitted, out.Truncated = blockers, omitted, st.Report.Truncated
	writeJSON(w, out)
}

// capacityRanges maps the range query values of the capacity history to their length.
var capacityRanges = map[string]time.Duration{
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour, "1y": 365 * 24 * time.Hour,
}

// getClusterCapacityHistory serves GET /v1/clusters/{cluster}/capacity/history.
func (s *Server) getClusterCapacityHistory(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	q := r.URL.Query()
	rng := q.Get("range")
	if rng == "" {
		rng = "30d"
	}
	dur, ok := capacityRanges[rng]
	if !ok {
		badRequest(w, r, "invalid_range", "range must be 7d, 30d, 90d or 1y.")
		return
	}
	pool := q.Get("nodePool")
	live, err := s.Store.ClusterLive(r.Context(), cluster)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !live {
		writeProblem(w, r, http.StatusNotFound, "cluster_not_found", "Unknown cluster "+cluster+".")
		return
	}
	to := s.now()
	from := to.Add(-dur)
	db := s.capacityDB()
	ctx := r.Context()

	out := hubapi.CapacityHistory{Range: rng}
	pts, err := db.History(ctx, cluster, pool, from, to, rng == "90d" || rng == "1y")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.Points = make([]hubapi.CapacityHistoryPoint, 0, len(pts))
	for _, p := range pts {
		out.Points = append(out.Points, hubapi.CapacityHistoryPoint{Hour: p.Time.UTC(), Nodes: p.Nodes,
			CPURequested: p.CPURequested, CPUAllocatable: p.CPUAllocatable, MemRequested: p.MemRequested, MemAllocatable: p.MemAllocatable})
	}
	if out.NodePools, err = db.Pools(ctx, cluster, from, to); err != nil {
		s.internal(w, r, err)
		return
	}
	if out.NodePools == nil {
		out.NodePools = []string{}
	}
	if out.Truncated, err = db.AnyTruncated(ctx, cluster, from, to); err != nil {
		s.internal(w, r, err)
		return
	}
	since, found, err := db.Since(ctx, cluster)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if found {
		since = since.UTC()
		out.Since = &since
	}

	hours, err := db.PoolHours(ctx, cluster, from, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	events := []capacity.Event{}
	for _, ev := range capacity.PoolEvents(hours) {
		if pool == "" || ev.NodePool == pool {
			events = append(events, ev)
		}
	}
	// Without a node pool every workload counts; with one, only the workloads running in it
	// (none when there is no report to tell).
	var workloads []hubapi.WorkloadRef
	if pool != "" {
		workloads = []hubapi.WorkloadRef{}
		st, have, err := db.Load(ctx, cluster)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if have {
			workloads = capacity.PoolWorkloads(st.Report, pool)
		}
	}
	changes, err := db.ChangeEvents(ctx, cluster, from, to, workloads)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	events = append(events, changes...)
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Day.Equal(events[j].Day) {
			return events[i].Day.Before(events[j].Day)
		}
		return events[i].Type < events[j].Type
	})
	out.Events = make([]hubapi.CapacityHistoryEvent, 0, len(events))
	for _, ev := range events {
		out.Events = append(out.Events, hubapi.CapacityHistoryEvent{Day: ev.Day.UTC().Format("2006-01-02"), Type: ev.Type,
			Count: ev.Count, NodePool: ev.NodePool, TopWorkloads: ev.Top})
	}
	writeJSON(w, out)
}

// capacityRecs returns the recommended requests per workload, the workloads the hub knows and
// the pricing rates of a recommendation (nil = none).
func capacityRecs(views []*store.WorkloadView) (capacity.Recs, map[hubapi.WorkloadKey]bool, *hubapi.RecommendationPricing) {
	recs := capacity.Recs{}
	known := map[hubapi.WorkloadKey]bool{}
	var rates *hubapi.RecommendationPricing
	for _, v := range views {
		known[v.Key] = true
		if v.Rec == nil {
			continue
		}
		if rates == nil && v.Rec.Pricing != nil {
			rates = v.Rec.Pricing
		}
		rr := capacity.RecommendedRequests{}
		for _, c := range v.Rec.Containers {
			req := c.Recommended.Requests
			if req == nil {
				continue
			}
			var q capacity.Request
			if req.CPU != "" {
				if f, err := recommender.ParseCPU(req.CPU); err == nil {
					q.CPU = &f
				}
			}
			if req.Memory != "" {
				if f, err := recommender.ParseMemory(req.Memory); err == nil {
					q.Mem = &f
				}
			}
			if q.CPU != nil || q.Mem != nil {
				rr[c.Name] = q
			}
		}
		if len(rr) > 0 {
			recs[v.Key.Ref()] = rr
		}
	}
	return recs, known, rates
}

// instancePrices reads the hourly price per instance type and capacity type from the pricing
// series (the hub's own store). Errors are logged: the estimate then uses the rates.
func (s *Server) instancePrices(ctx context.Context, cluster string) map[capacity.TypeKey]float64 {
	out := map[capacity.TypeKey]float64{}
	if s.Prom == nil {
		return out
	}
	q := fmt.Sprintf("max by (instance_type, capacity_type) (last_over_time(kerf_node_hourly_cost_usd%s[1h]))",
		datasource.DefaultMatcher(cluster).Sel())
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.Prom.Instant(ctx, q, s.now())
	if err != nil {
		s.Log.WarnContext(ctx, "capacity: instance prices", "cluster", cluster, "err", err)
		return out
	}
	for _, v := range res {
		out[capacity.TypeKey{InstanceType: v.Labels["instance_type"], CapacityType: v.Labels["capacity_type"]}] = v.Sample.V
	}
	return out
}

// runCapacityHistory samples capacity history hourly while this replica leads.
func (s *Server) runCapacityHistory(ctx context.Context) {
	(&capacity.Worker{DB: s.capacityDB(), Log: s.Log, Now: s.Now}).Run(ctx)
}
