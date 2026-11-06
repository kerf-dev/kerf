package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/realized"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// Realized savings (docs/realized-savings.md): the leader materialises them hourly; the API reads
// the materialised rows and never recomputes on a request.

func (s *Server) realizedDB() *realized.DB { return &realized.DB{Pool: s.Store.Pool} }

// RealizedWorker returns the leader's realized-savings worker.
func (s *Server) RealizedWorker() *realized.Worker {
	w := &realized.Worker{
		DB: s.realizedDB(), Local: s.Prom, MetricsFor: s.metricsFor, Log: s.Log, Now: s.Now,
		DefaultRates: realized.Rates{CPUCoreHour: s.RecCfg.DefaultCPUCoreHourUSD, MemoryGiBHour: s.RecCfg.DefaultMemoryGiBHourUSD},
	}
	if s.Metrics != nil {
		w.Collector = s.Metrics.realized
	}
	return w
}

// realizedRange parses from, to and step; defaults: to = now, from = to minus 12 months (month)
// or 30 days (day).
func (s *Server) realizedRange(r *http.Request, defStep string) (from, to time.Time, step string, ok string, msg string) {
	q := r.URL.Query()
	step = q.Get("step")
	if step == "" {
		step = defStep
	}
	if step != hubapi.RealizedStepDay && step != hubapi.RealizedStepMonth {
		return from, to, step, "invalid_step", "step must be day or month."
	}
	to = s.now().UTC()
	if t, err := timeParam(q.Get("to")); err != nil {
		return from, to, step, "invalid_time_range", err.Error()
	} else if t != nil {
		to = t.UTC()
	}
	from = to.AddDate(0, 0, -30)
	if step == hubapi.RealizedStepMonth {
		from = to.AddDate(-1, 0, 0)
	}
	if t, err := timeParam(q.Get("from")); err != nil {
		return from, to, step, "invalid_time_range", err.Error()
	} else if t != nil {
		from = t.UTC()
	}
	if !from.Before(to) {
		return from, to, step, "invalid_time_range", "from must be before to"
	}
	if step == hubapi.RealizedStepDay && to.Sub(from) > 3*366*24*time.Hour {
		return from, to, step, "invalid_time_range", "at most 3 years with step=day"
	}
	return from, to, step, "", ""
}

func toRealizedAPI(sum *realized.Summary, from, to time.Time, step string) hubapi.RealizedSavings {
	amt := func(a realized.Amount) hubapi.RealizedAmount {
		return hubapi.RealizedAmount{CPU: a.CPU, Memory: a.Memory, Total: a.Total()}
	}
	out := hubapi.RealizedSavings{
		Since: sum.Since, ComputedThrough: sum.ComputedThrough, From: from, To: to, Step: step,
		Total: amt(sum.Total), InRange: amt(sum.InRange),
		ReleasedNow: hubapi.ReleasedRequests{CPUCores: sum.ReleasedNowCPUCores, MemoryBytes: sum.ReleasedNowMemoryBytes},
		Points:      make([]hubapi.RealizedPoint, 0, len(sum.Points)),
		Coverage: hubapi.RealizedCoverage{
			WorkloadHours: sum.Coverage.Hours, ReplicasFromRecommendations: sum.Coverage.HoursReplicasFromRecs,
			WithoutReplicas: sum.Coverage.HoursWithoutReplicas, DefaultPricing: sum.Coverage.HoursWithDefaultPrices,
		},
	}
	for _, p := range sum.Points {
		out.Points = append(out.Points, hubapi.RealizedPoint{
			Time: p.Time, CPU: p.USD.CPU, Memory: p.USD.Memory, Total: p.USD.Total(),
			Released: hubapi.ReleasedRequests{CPUCores: p.CPUCores, MemoryBytes: p.MemoryBytes},
		})
	}
	if out.Since != nil {
		t := out.Since.UTC()
		out.Since = &t
	}
	if out.ComputedThrough != nil {
		t := out.ComputedThrough.UTC()
		out.ComputedThrough = &t
	}
	return out
}

// getRealizedSavings serves GET /v1/savings/realized.
func (s *Server) getRealizedSavings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, c := range q["cluster"] {
		if !hubapi.ClusterNameRE.MatchString(c) {
			badRequest(w, r, "invalid_cluster", "Invalid cluster name "+c+".")
			return
		}
	}
	from, to, step, code, msg := s.realizedRange(r, hubapi.RealizedStepMonth)
	if code != "" {
		badRequest(w, r, code, msg)
		return
	}
	sum, err := s.realizedDB().Summary(r.Context(), realized.Scope{Clusters: q["cluster"], Namespaces: q["namespace"]}, from, to, step)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, toRealizedAPI(sum, from, to, step))
}

// getWorkloadRealizedSavings serves GET /v1/workloads/{id}/savings/realized. A deleted workload
// with realized history still answers.
func (s *Server) getWorkloadRealizedSavings(w http.ResponseWriter, r *http.Request) {
	key, err := hubapi.ParseWorkloadID(r.PathValue("id"))
	if err != nil {
		badRequest(w, r, "invalid_workload_id", err.Error())
		return
	}
	from, to, step, code, msg := s.realizedRange(r, hubapi.RealizedStepDay)
	if code != "" {
		badRequest(w, r, code, msg)
		return
	}
	sc := realized.Scope{Clusters: []string{key.Cluster}, Workload: &realized.WorkloadKey{Namespace: key.Namespace, Kind: key.Kind, Name: key.Name}}
	sum, err := s.realizedDB().Summary(r.Context(), sc, from, to, step)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if sum.Since == nil {
		views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{Key: &key})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(views) == 0 {
			writeProblem(w, r, http.StatusNotFound, "workload_not_found", "Unknown workload "+key.ID()+".")
			return
		}
	}
	writeJSON(w, toRealizedAPI(sum, from, to, step))
}

// getNodeCostTrend serves GET /v1/savings/realized/node-cost.
func (s *Server) getNodeCostTrend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cluster := q.Get("cluster")
	if !hubapi.ClusterNameRE.MatchString(cluster) {
		badRequest(w, r, "invalid_cluster", "cluster is required and must be a cluster name.")
		return
	}
	to := s.now().UTC()
	if t, err := timeParam(q.Get("to")); err != nil {
		badRequest(w, r, "invalid_time_range", err.Error())
		return
	} else if t != nil {
		to = t.UTC()
	}
	from := to.AddDate(0, 0, -30)
	if t, err := timeParam(q.Get("from")); err != nil {
		badRequest(w, r, "invalid_time_range", err.Error())
		return
	} else if t != nil {
		from = t.UTC()
	}
	if !from.Before(to) || to.Sub(from) > 400*24*time.Hour {
		badRequest(w, r, "invalid_time_range", "from must be before to, at most 400 days apart")
		return
	}
	step := q.Get("step")
	if step == "" {
		step = hubapi.NodeCostStepDay
		if to.Sub(from) <= 7*24*time.Hour {
			step = hubapi.NodeCostStepHour
		}
	}
	if step != hubapi.NodeCostStepHour && step != hubapi.NodeCostStepDay {
		badRequest(w, r, "invalid_step", "step must be hour or day.")
		return
	}
	if step == hubapi.NodeCostStepHour && to.Sub(from) > 31*24*time.Hour {
		badRequest(w, r, "invalid_time_range", "at most 31 days with step=hour")
		return
	}
	exists, err := s.Store.ClusterExists(r.Context(), cluster)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !exists {
		writeProblem(w, r, http.StatusNotFound, "cluster_not_found", "Unknown cluster "+cluster+".")
		return
	}
	pts, through, err := s.realizedDB().NodeCost(r.Context(), cluster, from, to, step)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.NodeCostTrend{Cluster: cluster, From: from, To: to, Step: step, Relation: hubapi.RelationCorrelated,
		Points: make([]hubapi.NodeCostPoint, 0, len(pts))}
	if through != nil {
		t := through.UTC()
		out.ComputedThrough = &t
	}
	for _, p := range pts {
		out.Points = append(out.Points, hubapi.NodeCostPoint{
			Time: p.Time, NodeCostUSDPerHour: p.NodeCostUSDPerHour, Nodes: p.Nodes,
			CPURequestedRatio: p.CPURequestedRatio, MemoryRequestedRatio: p.MemoryRequestedRatio,
			Released: hubapi.ReleasedRequests{CPUCores: p.ReleasedCPUCores, MemoryBytes: p.ReleasedMemoryBytes},
		})
	}
	writeJSON(w, out)
}

// runRealized runs the realized-savings worker while this replica leads.
func (s *Server) runRealized(ctx context.Context) { s.RealizedWorker().Run(ctx) }
