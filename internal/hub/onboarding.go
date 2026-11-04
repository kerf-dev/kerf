package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/install"
	"github.com/tomer1983/resource-optimizer/internal/hub/readiness"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// Onboarding is the hub side of D-025: datasources, metrics bindings, readiness and install.
type Onboarding struct {
	Bind      *Bindings
	DS        *datasource.Registry
	Readiness *readiness.Store
	Runner    *readiness.Runner
}

// InitOnboarding wires datasources, readiness and the readiness runner. cipher may be nil (no
// datasource secrets possible). It upserts datasource local from prometheusURL.
func (s *Server) InitOnboarding(ctx context.Context, cipher *datasource.Cipher) error {
	pg := &datasource.PG{DB: s.Store.Pool}
	reg := &datasource.Registry{Repo: pg, Cipher: cipher, Now: s.Now}
	if err := reg.EnsureLocal(ctx, s.Cfg.PrometheusURL); err != nil {
		return err
	}
	local := s.Prom
	if local == nil {
		local = recommender.NewPromClient(s.Cfg.PrometheusURL, s.Cfg.PrometheusTimeout.D())
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	gauges := readiness.NewGauges()
	if s.Metrics != nil && s.Metrics.Registry != nil {
		if err := gauges.Register(s.Metrics.Registry); err != nil {
			return err
		}
	}
	o := &Onboarding{
		Bind:      &Bindings{PG: pg, Registry: reg, Local: local, LocalAPI: datasource.NewAPI(s.Cfg.PrometheusURL, hc), Timeout: s.Cfg.PrometheusTimeout.D()},
		DS:        reg,
		Readiness: &readiness.Store{DB: s.Store.Pool, Gauges: gauges},
	}
	o.Runner = &readiness.Runner{
		Clusters: s.clusterNames,
		Check:    s.runHubChecks,
		Interval: s.Cfg.Onboarding.CheckInterval.D(),
		Log:      s.Log,
	}
	s.Onb = o
	names, err := s.clusterNames(ctx)
	if err == nil {
		err = o.Readiness.Refresh(ctx, names)
	}
	return err
}

// RecheckTimeout ends a re-check request the agent did not answer (no agent, or an agent without
// self-checks), so it is not shown as pending forever.
const RecheckTimeout = 15 * time.Minute

// RunReadiness runs the hourly metrics checks and, every minute, the cheap hub checks
// (agent.connected, metrics.recommendations). It blocks until ctx is done.
func (s *Server) RunReadiness(ctx context.Context) {
	if s.Onb == nil {
		return
	}
	go s.Onb.Runner.Run(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.refreshAgentChecks(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) clusterNames(ctx context.Context) ([]string, error) {
	cs, err := s.Store.Clusters(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out, nil
}

// metricsFor returns the querier and binding matcher of a cluster (nil matcher = unbound).
func (s *Server) metricsFor(ctx context.Context, cluster string) (recommender.Querier, *datasource.Matcher, error) {
	if s.Onb == nil {
		return s.Prom, nil, nil
	}
	return s.Onb.Bind.ForRecommender(ctx, cluster)
}

func (s *Server) docsBase() string { return s.Cfg.Onboarding.DocsBaseURL }

// historyWindow is the longest profile window (metrics.history).
func (s *Server) historyWindow() time.Duration {
	w := time.Duration(0)
	for _, p := range s.RecCfg.Profiles {
		w = max(w, p.Window)
	}
	return w
}

// hubChecks returns the cheap hub checks of a cluster: agent.connected and
// metrics.recommendations.
func (s *Server) hubChecks(ctx context.Context, cluster string) ([]hubapi.ReadinessCheck, error) {
	c, err := s.Store.GetCluster(ctx, cluster)
	if err != nil {
		return nil, err
	}
	live, recommended, err := s.Store.RecommendationCounts(ctx, cluster)
	if err != nil {
		return nil, err
	}
	now := s.now()
	return []hubapi.ReadinessCheck{
		readiness.AgentConnectedCheck(c.ConfigPulledAt, s.Cfg.Onboarding.AgentPullInterval.D(), now, s.docsBase()),
		readiness.RecommendationsCheck(live, recommended, now, s.docsBase()),
	}, nil
}

// runHubChecks runs every hub check of a cluster against its datasource and stores them.
func (s *Server) runHubChecks(ctx context.Context, cluster string) error {
	checks, err := s.hubChecks(ctx, cluster)
	if err != nil {
		return err
	}
	res, err := s.Onb.Bind.Resolve(ctx, cluster)
	if err != nil {
		checks = append(checks, readiness.NewCheck(rd.CheckDatasourceReachable, rd.StatusFail, "datasource: "+err.Error(),
			"Fix the cluster's metrics binding or its datasource (admin → Metrics sources).", hubapi.RemediationText, s.docsBase(), s.now(), nil))
	} else {
		mc := &readiness.MetricsChecker{Q: res.Q, API: res.API, M: res.Matcher, Jobs: res.Binding.JobNames, Local: s.Prom,
			HistoryWindow: s.historyWindow(), DocsBase: s.docsBase(), Now: s.now}
		checks = append(checks, mc.Run(ctx).Checks...)
	}
	a, err := s.Onb.Readiness.Apply(ctx, cluster, readiness.Update{Checks: checks, ReplaceIDs: readiness.MetricsCheckIDs(), At: s.now()})
	if err != nil {
		return err
	}
	s.logTransition(ctx, cluster, a)
	return nil
}

func (s *Server) logTransition(ctx context.Context, cluster string, a readiness.Applied) {
	if a.Transitioned() {
		s.Log.InfoContext(ctx, "cluster readiness changed", "cluster", cluster, "from", a.Prev, "to", a.State, "blockedBy", a.BlockedBy)
	}
}

// refreshAgentChecks updates agent.connected and metrics.recommendations of every cluster when
// their status changed.
func (s *Server) refreshAgentChecks(ctx context.Context) {
	names, err := s.clusterNames(ctx)
	if err != nil {
		s.Log.WarnContext(ctx, "readiness: list clusters", "err", err)
		return
	}
	if err := s.Onb.Readiness.ExpireRechecks(ctx, s.now().Add(-RecheckTimeout)); err != nil {
		s.Log.WarnContext(ctx, "readiness: expire re-checks", "err", err)
	}
	for _, c := range names {
		checks, err := s.hubChecks(ctx, c)
		if err != nil {
			s.Log.WarnContext(ctx, "readiness: hub checks", "cluster", c, "err", err)
			continue
		}
		cur, err := s.Onb.Readiness.Get(ctx, c)
		if err != nil {
			continue
		}
		changed := false
		for _, n := range checks {
			i := slices.IndexFunc(cur.Checks, func(o hubapi.ReadinessCheck) bool { return o.ID == n.ID })
			if i < 0 || cur.Checks[i].Status != n.Status || s.now().Sub(cur.Checks[i].CheckedAt) > s.Cfg.Onboarding.CheckInterval.D() {
				changed = true
			}
		}
		if !changed {
			continue
		}
		a, err := s.Onb.Readiness.Apply(ctx, c, readiness.Update{Checks: checks, At: s.now()})
		if err != nil {
			s.Log.WarnContext(ctx, "readiness: store", "cluster", c, "err", err)
			continue
		}
		s.logTransition(ctx, c, a)
	}
}

// recordAgentChecks stores the self-checks of a status batch (absent = unchanged).
func (s *Server) recordAgentChecks(ctx context.Context, cluster string, batch hubapi.WorkloadStatusBatch) error {
	if s.Onb == nil || (batch.Checks == nil && batch.Capabilities == nil) {
		return nil
	}
	u := readiness.Update{At: s.now(), Capabilities: batch.Capabilities}
	if batch.Checks != nil {
		for _, c := range batch.Checks {
			if cat, ok := rd.Lookup(c.ID); ok && cat.Source == rd.SourceAgent {
				u.Checks = append(u.Checks, c)
			}
		}
		u.ReplaceIDs = readiness.AgentCheckIDs()
	}
	if u.Capabilities == nil && batch.Capabilities != nil {
		u.Capabilities = []hubapi.AgentCapability{}
	}
	a, err := s.Onb.Readiness.Apply(ctx, cluster, u)
	if err != nil {
		return err
	}
	s.logTransition(ctx, cluster, a)
	return nil
}

// readinessFlags adds the pending re-check and versions to an agent's config.
func (s *Server) readinessFlags(ctx context.Context, cfg *hubapi.ClusterConfig) error {
	cfg.HubVersion, cfg.MinAgentVersion = s.Cfg.Onboarding.HubVersion, s.Cfg.Onboarding.MinAgentVersion
	if s.Onb == nil {
		return nil
	}
	at, err := s.Onb.Readiness.RecheckRequestedAt(ctx, cfg.Cluster)
	if err != nil {
		return err
	}
	if at != nil {
		cfg.Flags.Recheck, cfg.Flags.RecheckRequestedAt = true, at
	}
	return nil
}

// readinessSummary is the cluster's readiness for lists.
func (s *Server) readinessSummary(ctx context.Context, cluster string) (hubapi.ReadinessSummary, error) {
	if s.Onb == nil {
		return hubapi.ReadinessSummary{State: string(rd.StateRegistered)}, nil
	}
	r, err := s.Onb.Readiness.Get(ctx, cluster)
	if err != nil {
		return hubapi.ReadinessSummary{}, err
	}
	return hubapi.ReadinessSummary{State: r.State, Blocking: r.Summary.Blocking, Warnings: r.Summary.Warnings, CheckedAt: r.CheckedAt}, nil
}

// triggerChecks queues the hub checks of clusters (registration, binding or datasource change).
func (s *Server) triggerChecks(clusters ...string) {
	if s.Onb == nil {
		return
	}
	for _, c := range clusters {
		s.Onb.Runner.Trigger(c)
	}
}

// ---- auto-patch gating (D-025) ----

// readinessGate answers 409 cluster_not_automation_ready when auto-patch (or an approve-once)
// targets a cluster that is not AutomationReady, unless an admin overrides it. clusters empty =
// every cluster. It returns the audit extra for an override.
func (s *Server) readinessGate(w http.ResponseWriter, r *http.Request, clusters []string, gated, override bool) (map[string]any, bool) {
	if !gated || s.Onb == nil {
		return nil, true
	}
	if len(clusters) == 0 {
		names, err := s.clusterNames(r.Context())
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		clusters = names
	}
	states, err := s.Onb.Readiness.States(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	notReady := readiness.NotAutomationReady(states, clusters)
	if len(notReady) == 0 {
		return nil, true
	}
	if override && PrincipalFrom(r.Context()).IsAdmin() {
		return map[string]any{"overrideReadiness": true}, true
	}
	writeProblem(w, r, http.StatusConflict, hubapi.CodeClusterNotAutomationReady, readiness.GateDetail(notReady))
	return nil, false
}

// ---- datasources ----

func (s *Server) dsError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *datasource.ValidationError
	switch {
	case errors.As(err, &ve):
		badRequest(w, r, "invalid_datasource", ve.Error())
	case errors.Is(err, datasource.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", "Unknown datasource.")
	case errors.Is(err, datasource.ErrVersionConflict):
		writeProblem(w, r, http.StatusPreconditionFailed, hubapi.CodeVersionConflict, "The datasource was changed by someone else; reload it.")
	case errors.Is(err, datasource.ErrInUse):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeDatasourceInUse, "The datasource is "+err.Error()+".")
	case errors.Is(err, datasource.ErrBuiltin):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeBuiltin, "The built-in datasource local is configured by the hub (prometheusURL).")
	default:
		s.internal(w, r, err)
	}
}

func (s *Server) onb(w http.ResponseWriter, r *http.Request) bool {
	if s.Onb == nil {
		notImplemented(w, r)
		return false
	}
	return true
}

func (s *Server) dsChange(r *http.Request, action, reason string, d hubapi.Datasource, before, after any) store.Change {
	c := s.change(r, action, reason)
	c.Target = hubapi.ConfigTarget{Type: hubapi.TargetDatasource, ID: d.ID, Name: d.Name}
	c.Version, c.Before, c.After = d.Version, before, after
	return c
}

// listDatasources serves GET /v1/admin/datasources.
func (s *Server) listDatasources(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	items, err := s.Onb.DS.List(r.Context())
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	writeJSON(w, hubapi.DatasourceList{Items: items})
}

// getDatasource serves GET /v1/admin/datasources/{datasourceId}.
func (s *Server) getDatasource(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	d, err := s.Onb.DS.Get(r.Context(), r.PathValue("datasourceId"))
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	setETag(w, d.Version)
	writeJSON(w, d)
}

// createDatasource serves POST /v1/admin/datasources.
func (s *Server) createDatasource(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	var in hubapi.DatasourceInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	d, err := s.Onb.DS.Create(r.Context(), in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	if err := s.Store.RecordChange(r.Context(), s.dsChange(r, hubapi.ActionDatasourceCreated, in.Reason, d, nil, d)); err != nil {
		s.Log.WarnContext(r.Context(), "config audit", "err", err)
	}
	setETag(w, d.Version)
	writeJSONStatus(w, http.StatusCreated, d)
}

// updateDatasource serves PUT /v1/admin/datasources/{datasourceId}.
func (s *Server) updateDatasource(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.DatasourceInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	before, after, err := s.Onb.DS.Update(r.Context(), r.PathValue("datasourceId"), ver, in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	if err := s.Store.RecordChange(r.Context(), s.dsChange(r, hubapi.ActionDatasourceUpdated, in.Reason, after, before, after)); err != nil {
		s.Log.WarnContext(r.Context(), "config audit", "err", err)
	}
	s.triggerChecks(after.UsedBy...)
	setETag(w, after.Version)
	writeJSON(w, after)
}

// deleteDatasource serves DELETE /v1/admin/datasources/{datasourceId}.
func (s *Server) deleteDatasource(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	d, err := s.Onb.DS.Delete(r.Context(), r.PathValue("datasourceId"), ver, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	if err := s.Store.RecordChange(r.Context(), s.dsChange(r, hubapi.ActionDatasourceDeleted, in.Reason, d, d, nil)); err != nil {
		s.Log.WarnContext(r.Context(), "config audit", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// testDatasource serves POST /v1/admin/datasources/{datasourceId}/test: the metrics checks of an
// (unsaved) binding, or only reachability without one. Nothing is stored.
func (s *Server) testDatasource(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	id := r.PathValue("datasourceId")
	var in hubapi.DatasourceTestRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	if _, err := s.Onb.DS.Get(r.Context(), id); err != nil {
		s.dsError(w, r, err)
		return
	}
	now := s.now()
	if in.Binding == nil {
		_, api, err := s.Onb.Bind.Datasource(r.Context(), id)
		if err != nil {
			s.dsError(w, r, err)
			return
		}
		p := api.Probe(r.Context())
		st, msg, rem := rd.StatusPass, "query API answered", ""
		d := map[string]string{"latencyMs": strconv.FormatInt(p.Latency.Milliseconds(), 10)}
		if p.HTTPStatus != 0 {
			d["httpStatus"] = strconv.Itoa(p.HTTPStatus)
		}
		if !p.OK {
			st, msg, rem = rd.StatusFail, p.Error, p.Hint
			d["error"] = p.Error
		}
		out := hubapi.DatasourceTestResult{OK: p.OK, LatencyMs: int(p.Latency.Milliseconds()),
			Checks: []hubapi.ReadinessCheck{readiness.NewCheck(rd.CheckDatasourceReachable, st, msg, rem, hubapi.RemediationText, s.docsBase(), now, d)}}
		if p.OK {
			out.LabelValues, _ = api.LabelValues(r.Context(), datasource.ClusterLabel, nil, now.Add(-time.Hour), 50)
		}
		writeJSON(w, out)
		return
	}
	b := *in.Binding
	b.DatasourceID = id
	name := b.ClusterLabelValue
	if !hubapi.ClusterNameRE.MatchString(name) {
		name = "test"
	}
	res, err := s.Onb.Bind.ResolveBinding(r.Context(), name, b)
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	mc := &readiness.MetricsChecker{Q: res.Q, API: res.API, M: res.Matcher, Jobs: b.JobNames, Local: s.Prom,
		HistoryWindow: s.historyWindow(), DocsBase: s.docsBase(), Now: s.now}
	rep := mc.Run(r.Context())
	out := hubapi.DatasourceTestResult{OK: rep.OK(), LatencyMs: int(rep.Latency.Milliseconds()), LabelValues: rep.LabelValues, Checks: rep.Checks}
	if rep.Found != (hubapi.DatasourceTestFound{}) {
		found := rep.Found
		out.Found = &found // absent = nothing found
	}
	writeJSON(w, out)
}

// ---- readiness ----

// getClusterReadiness serves GET /v1/clusters/{cluster}/readiness.
func (s *Server) getClusterReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	out, err := s.Onb.Readiness.Get(r.Context(), r.PathValue("cluster"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, out)
}

// recheckClusterReadiness serves POST /v1/clusters/{cluster}/readiness/recheck.
func (s *Server) recheckClusterReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	cluster := r.PathValue("cluster")
	at := s.now().UTC()
	if err := s.Onb.Readiness.RequestRecheck(r.Context(), cluster, at); err != nil {
		s.internal(w, r, err)
		return
	}
	s.triggerChecks(cluster)
	writeJSONStatus(w, http.StatusAccepted, hubapi.RecheckAccepted{RequestedAt: at, HubChecks: true, AgentRecheck: true})
}

// getClusterReadinessHistory serves GET /v1/clusters/{cluster}/readiness/history.
func (s *Server) getClusterReadinessHistory(w http.ResponseWriter, r *http.Request) {
	if !s.onb(w, r) {
		return
	}
	q := r.URL.Query()
	var from, to time.Time
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				badRequest(w, r, "invalid_time_range", p.name+" must be RFC 3339.")
				return
			}
			*p.dst = t
		}
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			badRequest(w, r, "invalid_limit", "limit must be 1..1000.")
			return
		}
		limit = n
	}
	out, err := s.Onb.Readiness.History(r.Context(), r.PathValue("cluster"), from, to, limit, q.Get("continue"))
	if err != nil {
		if q.Get("continue") != "" {
			badRequest(w, r, "invalid_continue", "Invalid continue token.")
			return
		}
		s.internal(w, r, err)
		return
	}
	writeJSON(w, out)
}

// ---- install ----

// getClusterInstall serves GET /v1/admin/clusters/{cluster}/install.
func (s *Server) getClusterInstall(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = hubapi.InstallHelm
	}
	if format != hubapi.InstallHelm && format != hubapi.InstallValues && format != hubapi.InstallArgoCD {
		badRequest(w, r, "invalid_format", "format must be helm, values or argocd.")
		return
	}
	cluster := r.PathValue("cluster")
	o := s.Cfg.Onboarding
	hubURL := o.HubURL
	if hubURL == "" {
		hubURL = "https://" + r.Host
	}
	opts := install.Options{
		Cluster: cluster, HubURL: hubURL, AgentChart: o.AgentChart, WebhookChart: o.WebhookChart, ChartVersion: o.ChartVersion,
		Webhook: q.Get("webhook") == "true", CertManager: q.Get("certManager") == "true",
	}
	if s.Onb != nil {
		if res, err := s.Onb.Bind.Resolve(r.Context(), cluster); err == nil && res.Bound {
			opts.Health = &install.HealthMetrics{ClusterLabel: res.Matcher.Label, ClusterLabelValue: res.Matcher.Value, ExtraMatchers: res.Matcher.Extra}
			if res.Binding.DatasourceID != hubapi.LocalDatasourceID {
				if d, err := s.Onb.DS.Get(r.Context(), res.Binding.DatasourceID); err == nil && d.Auth.Type == hubapi.AuthNone {
					opts.Health.MetricsURL = d.URL
				}
			}
		}
	}
	snip, err := install.Render(format, opts)
	if err != nil {
		writeProblem(w, r, http.StatusConflict, "install_not_configured", "Install snippets are not configured on the hub: "+err.Error())
		return
	}
	writeJSON(w, snip)
}
