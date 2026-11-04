package hub

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// getSummary serves GET /v1/summary.
func (s *Server) getSummary(w http.ResponseWriter, r *http.Request) {
	clusters, err := s.Store.Clusters(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	lastRuns, err := s.Store.LastRunPerCluster(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	per := map[string]*hubapi.Summary{}
	out := hubapi.FleetSummary{GeneratedAt: s.now().UTC(), Fleet: newSummary(), Clusters: []hubapi.ClusterSummary{}}
	for _, c := range clusters {
		sum := newSummary()
		if t, ok := lastRuns[c.Name]; ok {
			sum.LastRecommenderRunAt = &t
			if out.Fleet.LastRecommenderRunAt == nil || t.After(*out.Fleet.LastRecommenderRunAt) {
				out.Fleet.LastRecommenderRunAt = &t
			}
		}
		out.Clusters = append(out.Clusters, hubapi.ClusterSummary{Summary: sum, Cluster: c.Name})
	}
	for i := range out.Clusters {
		per[out.Clusters[i].Cluster] = &out.Clusters[i].Summary
	}
	for _, v := range views {
		addToSummary(&out.Fleet, v)
		if cs := per[v.Key.Cluster]; cs != nil {
			addToSummary(cs, v)
		}
	}
	roundSummary(&out.Fleet)
	for i := range out.Clusters {
		roundSummary(&out.Clusters[i].Summary)
	}
	writeJSON(w, http.StatusOK, out)
}

func newSummary() hubapi.Summary {
	return hubapi.Summary{WorkloadsByRiskFlag: map[string]int{}, WorkloadsByPhase: map[string]int{}}
}

func addToSummary(sum *hubapi.Summary, v *store.WorkloadView) {
	sum.Workloads++
	m := v.Modes()
	sum.WorkloadsByMode.CPU.Inc(m.CPU)
	sum.WorkloadsByMode.Memory.Inc(m.Memory)
	sum.WorkloadsByPhase[v.Phase()]++
	sum.CPU.Used += v.Attributes.Used24h["cpu"]
	sum.Memory.Used += v.Attributes.Used24h["memory"]
	if v.Rec == nil {
		return
	}
	for _, f := range v.Rec.RiskFlags {
		sum.WorkloadsByRiskFlag[f]++
	}
	reps := v.Rec.AverageReplicas
	for _, c := range v.Rec.Containers {
		cpuCur, memCur := requestValues(c.Current)
		cpuRec, memRec := requestValues(c.Recommended)
		sum.CPU.Requested += cpuCur * reps
		sum.Memory.Requested += memCur * reps
		sum.CPU.Recommended += cpuRec * reps
		sum.Memory.Recommended += memRec * reps
	}
	sum.MonthlySavingsUSD = sum.MonthlySavingsUSD.Add(v.Rec.MonthlySavingsUSD)
}

func requestValues(r hubapi.Resources) (cpu, mem float64) {
	if r.Requests == nil {
		return 0, 0
	}
	if r.Requests.CPU != "" {
		cpu, _ = recommender.ParseCPU(r.Requests.CPU)
	}
	if r.Requests.Memory != "" {
		mem, _ = recommender.ParseMemory(r.Requests.Memory)
	}
	return cpu, mem
}

func roundSummary(s *hubapi.Summary) {
	r3 := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	s.CPU = hubapi.ResourceTotals{Requested: r3(s.CPU.Requested), Used: r3(s.CPU.Used), Recommended: r3(s.CPU.Recommended)}
	s.Memory = hubapi.ResourceTotals{Requested: math.Round(s.Memory.Requested), Used: math.Round(s.Memory.Used), Recommended: math.Round(s.Memory.Recommended)}
	r2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	s.MonthlySavingsUSD = hubapi.Savings{CPU: r2(s.MonthlySavingsUSD.CPU), Memory: r2(s.MonthlySavingsUSD.Memory)}
	s.MonthlySavingsUSD.Total = r2(s.MonthlySavingsUSD.CPU + s.MonthlySavingsUSD.Memory)
}

// listClusters serves GET /v1/clusters.
func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	clusters, err := s.Store.Clusters(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts := map[string]*hubapi.ClusterCounts{}
	for _, v := range views {
		c := counts[v.Key.Cluster]
		if c == nil {
			c = &hubapi.ClusterCounts{}
			counts[v.Key.Cluster] = c
		}
		c.Workloads++
		if v.Rec != nil {
			c.WithRecommendation++
		}
		if m := v.Modes(); m.CPU == hubapi.ModeAutoPatch || m.Memory == hubapi.ModeAutoPatch {
			c.AutoPatch++
		}
		if v.Phase() == hubapi.PhaseBlocked {
			c.Blocked++
		}
	}
	out := hubapi.ClusterList{Items: []hubapi.ClusterInfo{}}
	for _, c := range clusters {
		ci := hubapi.ClusterInfo{Name: c.Name, Description: c.Description, AgentVersion: c.AgentVersion, AgentStatus: "never"}
		created := c.CreatedAt.UTC()
		ci.CreatedAt = &created
		if c.LastSeenAt != nil {
			t := c.LastSeenAt.UTC()
			ci.LastHeartbeatAt = &t
			ci.AgentStatus = "connected"
			if s.now().Sub(t) > s.Cfg.AgentStaleAfter.D() {
				ci.AgentStatus = "stale"
			}
		}
		if cc := counts[c.Name]; cc != nil {
			ci.Counts = *cc
		}
		out.Items = append(out.Items, ci)
	}
	writeJSON(w, http.StatusOK, out)
}

// listWorkloads serves GET /v1/workloads.
func (s *Server) listWorkloads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, c := range q["cluster"] {
		if !hubapi.ClusterNameRE.MatchString(c) {
			badRequest(w, r, "invalid_cluster", "Invalid cluster name "+c+".")
			return
		}
	}
	for _, k := range q["kind"] {
		if !hubapi.ValidKind(k) {
			badRequest(w, r, "invalid_kind", "Invalid workload kind "+k+".")
			return
		}
	}
	for _, m := range q["mode"] {
		if !hubapi.Contains(hubapi.Modes, m) {
			badRequest(w, r, "invalid_mode", "Invalid mode "+m+".")
			return
		}
	}
	for _, f := range q["riskFlag"] {
		if !hubapi.ValidRiskFlag(f) {
			badRequest(w, r, "invalid_risk_flag", "Invalid risk flag "+f+".")
			return
		}
	}
	minSavings := math.Inf(-1)
	if v := q.Get("minSavings"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) {
			badRequest(w, r, "invalid_min_savings", "minSavings must be a number.")
			return
		}
		minSavings = f
	}
	search := strings.ToLower(q.Get("q"))
	if len(search) > 253 {
		badRequest(w, r, "invalid_q", "q is longer than 253 characters.")
		return
	}
	sortBy := cmp.Or(q.Get("sort"), "savings")
	if !slices.Contains([]string{"savings", "confidence", "name"}, sortBy) {
		badRequest(w, r, "invalid_sort", "sort must be savings, confidence or name.")
		return
	}
	order := q.Get("order")
	if order == "" {
		order = "desc"
		if sortBy == "name" {
			order = "asc"
		}
	}
	if order != "asc" && order != "desc" {
		badRequest(w, r, "invalid_order", "order must be asc or desc.")
		return
	}
	limit, err := intParam(q.Get("limit"), 50, 1, 500)
	if err != nil {
		badRequest(w, r, "invalid_limit", err.Error())
		return
	}
	queryKey := r.URL.Query()
	queryKey.Del("continue")
	queryKey.Del("limit")
	offset := 0
	if c := q.Get("continue"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil || cur.Q != queryKey.Encode() {
			badRequest(w, r, "invalid_continue", "Malformed continue token or filters changed.")
			return
		}
		offset = cur.Offset
	}

	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var rows []*store.WorkloadView
	for _, v := range views {
		if len(q["cluster"]) > 0 && !slices.Contains(q["cluster"], v.Key.Cluster) ||
			len(q["namespace"]) > 0 && !slices.Contains(q["namespace"], v.Key.Namespace) ||
			len(q["kind"]) > 0 && !slices.Contains(q["kind"], v.Key.Kind) {
			continue
		}
		if ms := q["mode"]; len(ms) > 0 {
			m := v.Modes()
			if !slices.Contains(ms, m.CPU) && !slices.Contains(ms, m.Memory) {
				continue
			}
		}
		if fs := q["riskFlag"]; len(fs) > 0 && (v.Rec == nil || !slices.ContainsFunc(fs, func(f string) bool { return slices.Contains(v.Rec.RiskFlags, f) })) {
			continue
		}
		if !math.IsInf(minSavings, -1) && (v.Rec == nil || v.Rec.MonthlySavingsUSD.Total < minSavings) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(v.Key.Namespace), search) && !strings.Contains(strings.ToLower(v.Key.Name), search) {
			continue
		}
		rows = append(rows, v)
	}
	desc := order == "desc"
	slices.SortStableFunc(rows, func(a, b *store.WorkloadView) int {
		var c int
		switch sortBy {
		case "name":
			c = cmp.Compare(a.Key.Name+"\x00"+a.Key.ID(), b.Key.Name+"\x00"+b.Key.ID())
		default:
			// Workloads without a recommendation always sort last.
			if (a.Rec == nil) != (b.Rec == nil) {
				if a.Rec == nil {
					return 1
				}
				return -1
			}
			if a.Rec != nil {
				if sortBy == "savings" {
					c = cmp.Compare(a.Rec.MonthlySavingsUSD.Total, b.Rec.MonthlySavingsUSD.Total)
				} else {
					c = cmp.Compare(a.Rec.Confidence, b.Rec.Confidence)
				}
			}
			if c == 0 {
				return cmp.Compare(a.Key.ID(), b.Key.ID())
			}
		}
		if desc {
			c = -c
		}
		return c
	})
	out := hubapi.WorkloadList{Items: []hubapi.WorkloadRow{}, Total: len(rows)}
	end := min(offset+limit, len(rows))
	for _, v := range rows[min(offset, len(rows)):end] {
		out.Items = append(out.Items, workloadRow(v))
	}
	if end < len(rows) {
		out.Continue = encodeCursor(cursor{Offset: end, Q: queryKey.Encode()})
	}
	writeJSON(w, http.StatusOK, out)
}

func workloadRow(v *store.WorkloadView) hubapi.WorkloadRow {
	row := hubapi.WorkloadRow{
		ID: v.Key.ID(), Cluster: v.Key.Cluster, Namespace: v.Key.Namespace, Kind: v.Key.Kind, Name: v.Key.Name,
		Modes: v.Modes(), Phase: v.Phase(),
	}
	if v.Rec != nil {
		rec := v.Rec.WithoutExplanations()
		row.RecommendationID = rec.ID
		row.GeneratedAt = &rec.GeneratedAt
		row.Containers = rec.Containers
		row.Confidence = &rec.Confidence
		row.RiskFlags = rec.RiskFlags
		row.MonthlySavingsUSD = &rec.MonthlySavingsUSD
	}
	return row
}

// workloadFromPath resolves {id}; it writes the error response and returns nil when not found.
func (s *Server) workloadFromPath(w http.ResponseWriter, r *http.Request) *store.WorkloadView {
	key, err := hubapi.ParseWorkloadID(r.PathValue("id"))
	if err != nil {
		badRequest(w, r, "invalid_workload_id", err.Error())
		return nil
	}
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{Key: &key})
	if err != nil {
		s.internal(w, r, err)
		return nil
	}
	if len(views) == 0 {
		writeProblem(w, r, http.StatusNotFound, "workload_not_found", "Unknown workload "+key.ID()+".")
		return nil
	}
	return views[0]
}

// getWorkload serves GET /v1/workloads/{id}.
func (s *Server) getWorkload(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	d := hubapi.WorkloadDetail{
		ID: v.Key.ID(), Cluster: v.Key.Cluster, Namespace: v.Key.Namespace, Kind: v.Key.Kind, Name: v.Key.Name,
		Modes: v.Modes(), Phase: v.Phase(), FirstSeenAt: &v.FirstSeenAt, LastSeenAt: &v.LastSeenAt,
		Recommendation: v.Rec, Status: v.Status,
	}
	writeJSON(w, http.StatusOK, d)
}

// getWorkloadHistory serves GET /v1/workloads/{id}/history.
func (s *Server) getWorkloadHistory(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	q := r.URL.Query()
	from, to, err := s.timeRange(q.Get("from"), q.Get("to"))
	if err != nil {
		badRequest(w, r, "invalid_time_range", err.Error())
		return
	}
	limit, err := intParam(q.Get("limit"), 100, 1, 1000)
	if err != nil {
		badRequest(w, r, "invalid_limit", err.Error())
		return
	}
	recs, err := s.Store.RecommendationHistory(r.Context(), v.DBID, v.Key.Ref(), from, to, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range recs {
		recs[i] = recs[i].WithoutExplanations()
	}
	events, err := s.Store.WorkloadEvents(r.Context(), v.Key, from, to, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hubapi.WorkloadHistory{WorkloadID: v.Key.ID(), Recommendations: recs, Events: events})
}

// getSavingsTrend serves GET /v1/savings/trend.
func (s *Server) getSavingsTrend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, c := range q["cluster"] {
		if !hubapi.ClusterNameRE.MatchString(c) {
			badRequest(w, r, "invalid_cluster", "Invalid cluster name "+c+".")
			return
		}
	}
	groupBy := cmp.Or(q.Get("groupBy"), "none")
	if groupBy != "none" && groupBy != "cluster" {
		badRequest(w, r, "invalid_group_by", "groupBy must be none or cluster.")
		return
	}
	from, to, err := s.timeRange(q.Get("from"), q.Get("to"))
	if err != nil {
		badRequest(w, r, "invalid_time_range", err.Error())
		return
	}
	step, err := chooseStep(q.Get("step"), from, to)
	if err != nil {
		badRequest(w, r, "invalid_step", err.Error())
		return
	}
	samples, err := s.Store.SavingsSamples(r.Context(), q["cluster"], from, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Step through time; at each step use the latest sample per cluster at or before it.
	byCluster := map[string][]store.SavingsSample{}
	var names []string
	for _, x := range samples {
		if _, ok := byCluster[x.Cluster]; !ok {
			names = append(names, x.Cluster)
		}
		byCluster[x.Cluster] = append(byCluster[x.Cluster], x)
	}
	slices.Sort(names)
	pointsFor := func(clusters []string) []hubapi.SavingsPoint {
		pts := []hubapi.SavingsPoint{}
		idx := make([]int, len(clusters))
		for t := from; t.Before(to); t = t.Add(step) {
			p := hubapi.SavingsPoint{Time: t.UTC()}
			for i, c := range clusters {
				xs := byCluster[c]
				for idx[i]+1 < len(xs) && !xs[idx[i]+1].AsOf.After(t) {
					idx[i]++
				}
				if len(xs) > 0 && !xs[idx[i]].AsOf.After(t) {
					p.CPU += xs[idx[i]].CPU
					p.Memory += xs[idx[i]].Memory
				}
			}
			p.CPU, p.Memory = math.Round(p.CPU*100)/100, math.Round(p.Memory*100)/100
			p.Total = math.Round((p.CPU+p.Memory)*100) / 100
			pts = append(pts, p)
		}
		return pts
	}
	out := hubapi.SavingsTrend{From: from.UTC(), To: to.UTC(), Step: step.String(), Series: []hubapi.SavingsSeries{}}
	if groupBy == "cluster" {
		for _, c := range names {
			out.Series = append(out.Series, hubapi.SavingsSeries{Cluster: c, Points: pointsFor([]string{c})})
		}
	} else {
		out.Series = append(out.Series, hubapi.SavingsSeries{Points: pointsFor(names)})
	}
	writeJSON(w, http.StatusOK, out)
}

// timeRange applies the defaults: to = now, from = to - 7d.
func (s *Server) timeRange(fromS, toS string) (time.Time, time.Time, error) {
	to := s.now().UTC()
	if t, err := timeParam(toS); err != nil {
		return to, to, err
	} else if t != nil {
		to = *t
	}
	from := to.Add(-7 * 24 * time.Hour)
	if t, err := timeParam(fromS); err != nil {
		return from, to, err
	} else if t != nil {
		from = *t
	}
	if !from.Before(to) {
		return from, to, errors.New("from must be before to")
	}
	return from, to, nil
}

var niceSteps = []time.Duration{time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// chooseStep parses an explicit step (at most 11000 points) or picks the smallest nice step that
// gives at most 1000 points.
func chooseStep(s string, from, to time.Time) (time.Duration, error) {
	span := to.Sub(from)
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 10*time.Second {
			return 0, fmt.Errorf("step must be a Go duration of at least 10s")
		}
		if span/d > 11000 {
			return 0, fmt.Errorf("step %s gives more than 11000 points", d)
		}
		return d, nil
	}
	for _, d := range niceSteps {
		if span/d <= 1000 {
			return d, nil
		}
	}
	return niceSteps[len(niceSteps)-1], nil
}
