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

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
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
	clusters = visibleClusters(r, clusters, views, func(c store.Cluster) string { return c.Name })
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
	// Fleet savings are the sum of the rounded cluster savings, so they add up exactly (and match
	// the savings trend, which is stored per cluster).
	var fleet hubapi.Savings
	for i := range out.Clusters {
		roundSummary(&out.Clusters[i].Summary)
		fleet = fleet.Add(out.Clusters[i].MonthlySavingsUSD)
	}
	if len(out.Clusters) > 0 {
		out.Fleet.MonthlySavingsUSD = roundSavings(fleet)
	}
	if out.Teams, err = s.teamSummaries(r, views); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, out)
}

// visibleClusters drops, for a team-scoped caller, the clusters where it sees no workload.
func visibleClusters[T any](r *http.Request, clusters []T, views []*store.WorkloadView, name func(T) string) []T {
	if PrincipalFrom(r.Context()).Scope.All() {
		return clusters
	}
	seen := map[string]bool{}
	for _, v := range views {
		seen[v.Key.Cluster] = true
	}
	return slices.DeleteFunc(clusters, func(c T) bool { return !seen[name(c)] })
}

// teamSummaries groups the summary by team: every team the caller sees (also without workloads),
// then Unassigned when it has workloads. nil while no team is defined.
func (s *Server) teamSummaries(r *http.Request, views []*store.WorkloadView) ([]hubapi.TeamSummary, error) {
	list, err := s.Store.ListTeams(r.Context())
	if err != nil || len(list) == 0 {
		return nil, err
	}
	byTeam := map[string][]*store.WorkloadView{}
	for _, v := range views {
		byTeam[teamOf(v)] = append(byTeam[teamOf(v)], v)
	}
	sum := func(id, name string) hubapi.TeamSummary {
		ts := hubapi.TeamSummary{Summary: newSummary(), Team: id, DisplayName: name}
		for _, v := range byTeam[id] {
			addToSummary(&ts.Summary, v)
		}
		roundSummary(&ts.Summary)
		return ts
	}
	out := make([]hubapi.TeamSummary, 0, len(list)+1)
	for _, t := range list {
		out = append(out, sum(t.ID, t.DisplayName))
	}
	if len(byTeam[hubapi.TeamUnassigned]) > 0 {
		out = append(out, sum(hubapi.TeamUnassigned, "Unassigned"))
	}
	return out, nil
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
	s.MonthlySavingsUSD = roundSavings(s.MonthlySavingsUSD)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func roundSavings(s hubapi.Savings) hubapi.Savings {
	out := hubapi.Savings{CPU: round2(s.CPU), Memory: round2(s.Memory)}
	out.Total = round2(out.CPU + out.Memory)
	return out
}

// clusterSavings is the savings total of a set of workloads exactly as GET /v1/summary computes it.
func clusterSavings(views []*store.WorkloadView) hubapi.Savings {
	sum := newSummary()
	for _, v := range views {
		addToSummary(&sum, v)
	}
	roundSummary(&sum)
	return sum.MonthlySavingsUSD
}

// listClusters serves GET /v1/clusters.
func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	s.expirePauses(r)
	clusters, err := s.Store.ClusterRows(r.Context(), false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	automation, err := s.Store.Automations(r.Context())
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
	clusters = visibleClusters(r, clusters, views, func(c store.ClusterRow) string { return c.Name })
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
		// Admin area and readiness.
		ca, err := s.clusterAdmin(r, c, automation)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		paused := ca.Paused
		ci.Environment, ci.Owner, ci.PolicySource, ci.Paused = ca.Environment, ca.Owner, ca.PolicySource, &paused
		ci.Automation, ci.ConfigSync, ci.Readiness = &ca.Automation, &ca.Agent, &ca.Readiness
		out.Items = append(out.Items, ci)
	}
	writeJSON(w, out)
}

// listWorkloads serves GET /v1/workloads.
func (s *Server) listWorkloads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wf, ok := parseWorkloadFilters(w, r)
	if !ok {
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
	limit, err := intParam(q.Get("limit"), 50, 500)
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
		if wf.match(v, "") {
			rows = append(rows, v)
		}
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
	writeJSON(w, out)
}

// workloadFilters are the filters of GET /v1/workloads and /v1/workloads/facets.
type workloadFilters struct {
	clusters, namespaces, kinds, modes, riskFlags, teams []string
	gpu                                                  []string // hubapi.GPUFilterValues
	minSavings                                           float64  // -Inf when unset
	search                                               string   // lower case
}

// parseWorkloadFilters validates the filters; on error it writes the 400 response.
func parseWorkloadFilters(w http.ResponseWriter, r *http.Request) (workloadFilters, bool) {
	q := r.URL.Query()
	f := workloadFilters{
		clusters: q["cluster"], namespaces: q["namespace"], kinds: q["kind"], modes: q["mode"], riskFlags: q["riskFlag"], gpu: q["gpu"],
		teams: q["team"], minSavings: math.Inf(-1), search: strings.ToLower(q.Get("q")),
	}
	if !validTeamFilter(w, r, f.teams) {
		return f, false
	}
	for _, c := range f.clusters {
		if !hubapi.ClusterNameRE.MatchString(c) {
			badRequest(w, r, "invalid_cluster", "Invalid cluster name "+c+".")
			return f, false
		}
	}
	for _, k := range f.kinds {
		if !hubapi.ValidKind(k) {
			badRequest(w, r, "invalid_kind", "Invalid workload kind "+k+".")
			return f, false
		}
	}
	for _, m := range f.modes {
		if !hubapi.Contains(hubapi.Modes, m) {
			badRequest(w, r, "invalid_mode", "Invalid mode "+m+".")
			return f, false
		}
	}
	for _, fl := range f.riskFlags {
		if !hubapi.ValidRiskFlag(fl) {
			badRequest(w, r, "invalid_risk_flag", "Invalid risk flag "+fl+".")
			return f, false
		}
	}
	for _, g := range f.gpu {
		if !hubapi.ValidGPUFilter(g) {
			badRequest(w, r, "invalid_gpu", "Invalid gpu filter "+g+".")
			return f, false
		}
	}
	if v := q.Get("minSavings"); v != "" {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(x) {
			badRequest(w, r, "invalid_min_savings", "minSavings must be a number.")
			return f, false
		}
		f.minSavings = x
	}
	if len(f.search) > 253 {
		badRequest(w, r, "invalid_q", "q is longer than 253 characters.")
		return f, false
	}
	return f, true
}

// Facet names (the filter a facet ignores when it is counted).
const (
	facetCluster   = "cluster"
	facetNamespace = "namespace"
	facetKind      = "kind"
	facetMode      = "mode"
	facetRiskFlag  = "riskFlag"
	facetTeam      = "team"
	facetGPU       = "gpu"
)

// match applies all filters except the one named by skip ("" applies all).
func (f workloadFilters) match(v *store.WorkloadView, skip string) bool {
	if skip != facetCluster && len(f.clusters) > 0 && !slices.Contains(f.clusters, v.Key.Cluster) {
		return false
	}
	if skip != facetNamespace && len(f.namespaces) > 0 && !slices.Contains(f.namespaces, v.Key.Namespace) {
		return false
	}
	if skip != facetKind && len(f.kinds) > 0 && !slices.Contains(f.kinds, v.Key.Kind) {
		return false
	}
	if skip != facetMode && len(f.modes) > 0 {
		m := v.Modes()
		if !slices.Contains(f.modes, m.CPU) && !slices.Contains(f.modes, m.Memory) {
			return false
		}
	}
	if skip != facetRiskFlag && len(f.riskFlags) > 0 &&
		(v.Rec == nil || !slices.ContainsFunc(f.riskFlags, func(fl string) bool { return slices.Contains(v.Rec.RiskFlags, fl) })) {
		return false
	}
	if skip != facetTeam && len(f.teams) > 0 && !slices.Contains(f.teams, teamOf(v)) {
		return false
	}
	if skip != facetGPU && len(f.gpu) > 0 && !f.matchGPU(v) {
		return false
	}
	if !math.IsInf(f.minSavings, -1) && (v.Rec == nil || v.Rec.MonthlySavingsUSD.Total < f.minSavings) {
		return false
	}
	if f.search != "" && !strings.Contains(strings.ToLower(v.Key.Namespace), f.search) && !strings.Contains(strings.ToLower(v.Key.Name), f.search) {
		return false
	}
	return true
}

// matchGPU reports whether the workload requests GPUs and its GPU status is one of the filter values
// (`any` matches every GPU workload).
func (f workloadFilters) matchGPU(v *store.WorkloadView) bool {
	if v.Rec == nil || v.Rec.GPU == nil {
		return false
	}
	return slices.Contains(f.gpu, hubapi.GPUAny) || slices.Contains(f.gpu, v.Rec.GPU.Status)
}

// getWorkloadFacets serves GET /v1/workloads/facets.
func (s *Server) getWorkloadFacets(w http.ResponseWriter, r *http.Request) {
	wf, ok := parseWorkloadFilters(w, r)
	if !ok {
		return
	}
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts := map[string]map[string]int{}
	inc := func(facet string, values ...string) {
		if counts[facet] == nil {
			counts[facet] = map[string]int{}
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !seen[v] {
				seen[v] = true
				counts[facet][v]++
			}
		}
	}
	out := hubapi.WorkloadFacets{}
	for _, v := range views {
		if wf.match(v, "") {
			out.Total++
		}
		if wf.match(v, facetCluster) {
			inc(facetCluster, v.Key.Cluster)
		}
		if wf.match(v, facetNamespace) {
			inc(facetNamespace, v.Key.Namespace)
		}
		if wf.match(v, facetKind) {
			inc(facetKind, v.Key.Kind)
		}
		if wf.match(v, facetMode) {
			m := v.Modes()
			inc(facetMode, m.CPU, m.Memory)
		}
		if wf.match(v, facetRiskFlag) && v.Rec != nil {
			inc(facetRiskFlag, v.Rec.RiskFlags...)
		}
		if wf.match(v, facetTeam) {
			inc(facetTeam, teamOf(v))
		}
		if wf.match(v, facetGPU) && v.Rec != nil && v.Rec.GPU != nil {
			inc(facetGPU, v.Rec.GPU.Status)
		}
	}
	list := func(facet string) []hubapi.FacetCount {
		items := []hubapi.FacetCount{}
		for val, n := range counts[facet] {
			items = append(items, hubapi.FacetCount{Value: val, Count: n})
		}
		slices.SortFunc(items, func(a, b hubapi.FacetCount) int { return cmp.Compare(a.Value, b.Value) })
		return items
	}
	out.Clusters, out.Namespaces, out.Kinds = list(facetCluster), list(facetNamespace), list(facetKind)
	out.Modes, out.RiskFlags, out.Teams = list(facetMode), list(facetRiskFlag), list(facetTeam)
	if g := list(facetGPU); len(g) > 0 {
		out.GPU = g
	}
	writeJSON(w, out)
}

func workloadRow(v *store.WorkloadView) hubapi.WorkloadRow {
	row := hubapi.WorkloadRow{
		ID: v.Key.ID(), Cluster: v.Key.Cluster, Namespace: v.Key.Namespace, Kind: v.Key.Kind, Name: v.Key.Name,
		Modes: v.Modes(), Phase: v.Phase(), Team: teamOf(v),
	}
	if v.Rec != nil {
		rec := v.Rec.WithoutExplanations()
		row.RecommendationID = rec.ID
		row.GeneratedAt = &rec.GeneratedAt
		row.Containers = rec.Containers
		row.Confidence = &rec.Confidence
		row.RiskFlags = rec.RiskFlags
		row.MonthlySavingsUSD = &rec.MonthlySavingsUSD
		row.GPU = rec.GPU
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
		Modes: v.Modes(), Phase: v.Phase(), Team: teamOf(v), FirstSeenAt: &v.FirstSeenAt, LastSeenAt: &v.LastSeenAt,
		Recommendation: v.Rec, Status: v.Status,
	}
	if err := s.approvalAndOverride(r, &d, v.Key); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, d)
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
	limit, err := intParam(q.Get("limit"), 100, 1000)
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
	changes, err := s.workloadConfigChanges(r, v.Key, from, to, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, hubapi.WorkloadHistory{WorkloadID: v.Key.ID(), Recommendations: recs, Events: events, ConfigChanges: changes})
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
	if !validTeamFilter(w, r, q["team"]) {
		return
	}
	groupBy := cmp.Or(q.Get("groupBy"), "none")
	if groupBy != "none" && groupBy != "cluster" && groupBy != "team" {
		badRequest(w, r, "invalid_group_by", "groupBy must be none, cluster or team.")
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
	// A source series is one cluster, or one cluster and team when teams matter: a team filter or
	// grouping, or a team-scoped caller (whose trend covers its teams only).
	type source struct{ cluster, team string }
	bySource := map[source][]store.SavingsSample{}
	var sources []source
	add := func(src source, x store.SavingsSample) {
		if _, ok := bySource[src]; !ok {
			sources = append(sources, src)
		}
		bySource[src] = append(bySource[src], x)
	}
	if len(q["team"]) > 0 || groupBy == "team" || !PrincipalFrom(r.Context()).Scope.All() {
		samples, err := s.Store.TeamSavingsSamples(r.Context(), q["cluster"], q["team"], from, to)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, x := range samples {
			add(source{x.Cluster, cmp.Or(x.Team, hubapi.TeamUnassigned)}, x.SavingsSample)
		}
	} else {
		samples, err := s.Store.SavingsSamples(r.Context(), q["cluster"], from, to)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, x := range samples {
			add(source{cluster: x.Cluster}, x)
		}
	}
	slices.SortFunc(sources, func(a, b source) int { return cmp.Or(cmp.Compare(a.cluster, b.cluster), cmp.Compare(a.team, b.team)) })
	first := from.Truncate(step)
	if first.Before(from) {
		first = first.Add(step)
	}
	// Points are aligned to multiples of step; each holds the value at the end of its bucket,
	// min(t+step, to), so the last point with the default to (now) equals the current summary.
	pointsFor := func(srcs []source) []hubapi.SavingsPoint {
		pts := []hubapi.SavingsPoint{}
		idx := make([]int, len(srcs))
		for t := first; t.Before(to); t = t.Add(step) {
			at := t.Add(step)
			if at.After(to) {
				at = to
			}
			p := hubapi.SavingsPoint{Time: t.UTC()}
			for i, src := range srcs {
				xs := bySource[src]
				for idx[i]+1 < len(xs) && !xs[idx[i]+1].At.After(at) {
					idx[i]++
				}
				if len(xs) > 0 && !xs[idx[i]].At.After(at) {
					p.CPU += xs[idx[i]].CPU
					p.Memory += xs[idx[i]].Memory
				}
			}
			p.CPU, p.Memory = round2(p.CPU), round2(p.Memory)
			p.Total = round2(p.CPU + p.Memory)
			pts = append(pts, p)
		}
		return pts
	}
	out := hubapi.SavingsTrend{From: from.UTC(), To: to.UTC(), Step: step.String(), Series: []hubapi.SavingsSeries{}}
	group := func(key func(source) string, series func(k string, pts []hubapi.SavingsPoint) hubapi.SavingsSeries) {
		groups := map[string][]source{}
		var keys []string
		for _, src := range sources {
			k := key(src)
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], src)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out.Series = append(out.Series, series(k, pointsFor(groups[k])))
		}
	}
	switch groupBy {
	case "cluster":
		group(func(x source) string { return x.cluster }, func(k string, pts []hubapi.SavingsPoint) hubapi.SavingsSeries {
			return hubapi.SavingsSeries{Cluster: k, Points: pts}
		})
	case "team":
		group(func(x source) string { return x.team }, func(k string, pts []hubapi.SavingsPoint) hubapi.SavingsSeries {
			return hubapi.SavingsSeries{Team: k, Points: pts}
		})
	default:
		out.Series = append(out.Series, hubapi.SavingsSeries{Points: pointsFor(sources)})
	}
	writeJSON(w, out)
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
	// Longer than the largest nice step allows: whole days giving at most 1000 points.
	days := (span/1000 + 24*time.Hour - 1) / (24 * time.Hour)
	return days * 24 * time.Hour, nil
}
