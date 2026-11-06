// Package hub is the central service: HTTP API (api/openapi/hub.yaml), recommender scheduler,
// Postgres persistence and the kerf_recommendation_* metrics.
package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/notify"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Server serves the hub API.
type Server struct {
	Cfg     Config
	Store   *store.Store
	Prom    recommender.Querier
	RecCfg  recommender.Config
	Metrics *Metrics
	Log     *slog.Logger
	// Job runs the recommender; POST /v1/admin/recompute queues runs on it.
	Job *Job
	// Onb serves datasources, readiness and install (D-025); nil answers 501 (InitOnboarding).
	Onb *Onboarding
	// Coord is the leader election and notification channel shared with the other hub replicas
	// (WO-18a); nil = this process is the only hub and leads (tests).
	Coord *Elector
	// Now is the clock (tests).
	Now func() time.Time
	// Version is this hub's version (main.version), Identity its replica name (POD_NAME) and
	// Started its start time: the Maintenance page (WO-33).
	Version  string
	Identity string
	Started  time.Time
	// Updates checks for a newer Kerf release (WO-33); nil = off.
	Updates *UpdateChecker
	// Notify manages notification channels, rules and the digest; its Run is the leader's
	// notification work. nil answers 501 (InitNotifications).
	Notify *notify.Service

	mu         sync.Mutex
	tokens     map[string]tokenEntry
	heartbeats map[string]time.Time
	// componentSeen throttles recording component versions (recordPricing).
	componentSeen map[string]time.Time
	ready         bool
	ipLimiter     *auth.Limiter
	userLimiter   *auth.Limiter
}

type tokenEntry struct {
	tok     store.Token
	expires time.Time
}

type ctxKey struct{}

// TokenFrom returns the authenticated token of a request.
func TokenFrom(ctx context.Context) store.Token {
	t, _ := ctx.Value(ctxKey{}).(store.Token)
	return t
}

// SetReady marks the server ready (after migrations).
func (s *Server) SetReady() {
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// apiRoute is one /v1 endpoint. access nil = public: only sign-in and sign-out (route inventory test,
// server_routes_test.go). Every /v1 route is registered through apiRoutes, never on the mux directly.
type apiRoute struct {
	pattern string
	access  *access
	handler http.HandlerFunc
}

// apiRoutes lists every /v1 endpoint and who may call it.
func (s *Server) apiRoutes() []apiRoute {
	return []apiRoute{
		// Agent.
		{"GET /v1/clusters/{cluster}/recommendations", &accessAgent, s.listRecommendations},
		{"POST /v1/clusters/{cluster}/workload-status", &accessAgent, s.reportWorkloadStatus},
		{"POST /v1/clusters/{cluster}/events", &accessAgent, s.reportEvents},
		{"GET /v1/clusters/{cluster}/config", &accessAgent, s.getClusterConfig},
		{"GET /v1/audit", &accessAudit, s.queryAudit},
		// UI (read).
		{"GET /v1/summary", &accessViewer, s.getSummary},
		{"GET /v1/clusters", &accessViewer, s.listClusters},
		{"GET /v1/workloads", &accessViewer, s.listWorkloads},
		{"GET /v1/workloads/facets", &accessViewer, s.getWorkloadFacets},
		{"GET /v1/workloads/{id}", &accessViewer, s.getWorkload},
		{"GET /v1/workloads/{id}/usage", &accessViewer, s.getWorkloadUsage},
		{"GET /v1/workloads/{id}/history", &accessViewer, s.getWorkloadHistory},
		{"GET /v1/savings/trend", &accessViewer, s.getSavingsTrend},
		{"POST /v1/admin/recompute", &accessAdmin, s.recompute},
		// Auth.
		{"POST /v1/auth/login", nil, s.login},
		{"POST /v1/auth/logout", nil, s.logout},
		{"GET /v1/me", &accessViewer, s.getMe},
		{"POST /v1/me/password", &accessSession, s.changeOwnPassword},
		// Settings.
		{"GET /v1/admin/settings", &accessViewer, s.getSettings},
		{"GET /v1/admin/settings/recommender", &accessViewer, s.getRecommenderSettings},
		{"PUT /v1/admin/settings/recommender", &accessAdmin, s.putRecommenderSettings},
		{"GET /v1/admin/settings/pricing", &accessViewer, s.getPricingSettings},
		{"PUT /v1/admin/settings/pricing", &accessAdmin, s.putPricingSettings},
		{"GET /v1/admin/settings/{section}/versions", &accessViewer, s.listSettingsVersions},
		// Policies.
		{"GET /v1/admin/policies", &accessViewer, s.listPolicies},
		{"POST /v1/admin/policies", &accessAdmin, s.createPolicy},
		{"GET /v1/admin/policies/{policyId}", &accessViewer, s.getPolicy},
		{"PUT /v1/admin/policies/{policyId}", &accessAdmin, s.updatePolicy},
		{"DELETE /v1/admin/policies/{policyId}", &accessAdmin, s.deletePolicy},
		{"GET /v1/admin/policies/{policyId}/versions", &accessViewer, s.listPolicyVersions},
		{"POST /v1/admin/policies/{policyId}/revert", &accessAdmin, s.revertPolicy},
		{"GET /v1/admin/policy-inventory", &accessViewer, s.getPolicyInventory},
		{"POST /v1/admin/preview", &accessOperator, s.previewPolicyChange},
		{"GET /v1/workloads/{id}/effective-policy", &accessViewer, s.getEffectivePolicy},
		{"GET /v1/workloads/{id}/override", &accessViewer, s.getWorkloadOverride},
		{"PUT /v1/workloads/{id}/override", &accessOperator, s.putWorkloadOverride},
		{"DELETE /v1/workloads/{id}/override", &accessOperator, s.deleteWorkloadOverride},
		{"POST /v1/workloads/{id}/approve", &accessOperator, s.approveRecommendation},
		// Clusters and automation.
		{"POST /v1/admin/clusters", &accessAdmin, s.createCluster},
		{"GET /v1/admin/clusters/{cluster}", &accessViewer, s.getClusterAdmin},
		{"PATCH /v1/admin/clusters/{cluster}", &accessAdmin, s.updateCluster},
		{"DELETE /v1/admin/clusters/{cluster}", &accessAdmin, s.deleteCluster},
		{"PUT /v1/clusters/{cluster}/automation", &accessOperator, s.setClusterAutomation},
		{"GET /v1/admin/automation", &accessViewer, s.getFleetAutomation},
		{"PUT /v1/admin/automation", &accessOperator, s.setFleetAutomation},
		// Tokens and users.
		{"GET /v1/admin/tokens", &accessAdmin, s.listTokens},
		{"POST /v1/admin/tokens", &accessAdmin, s.createToken},
		{"DELETE /v1/admin/tokens/{tokenId}", &accessAdmin, s.revokeToken},
		{"GET /v1/admin/users", &accessAdmin, s.listUsers},
		{"POST /v1/admin/users", &accessAdmin, s.createUser},
		{"GET /v1/admin/users/{username}", &accessAdmin, s.getUser},
		{"PATCH /v1/admin/users/{username}", &accessAdmin, s.updateUser},
		{"DELETE /v1/admin/users/{username}", &accessAdmin, s.deleteUser},
		{"POST /v1/admin/users/{username}/reset-password", &accessAdmin, s.resetUserPassword},
		{"GET /v1/audit/config", &accessViewer, s.queryConfigAudit},
		// Onboarding and readiness.
		{"GET /v1/admin/datasources", &accessViewer, s.listDatasources},
		{"POST /v1/admin/datasources", &accessAdmin, s.createDatasource},
		{"GET /v1/admin/datasources/{datasourceId}", &accessViewer, s.getDatasource},
		{"PUT /v1/admin/datasources/{datasourceId}", &accessAdmin, s.updateDatasource},
		{"DELETE /v1/admin/datasources/{datasourceId}", &accessAdmin, s.deleteDatasource},
		{"POST /v1/admin/datasources/{datasourceId}/test", &accessAdmin, s.testDatasource},
		{"GET /v1/clusters/{cluster}/readiness", &accessViewer, s.getClusterReadiness},
		{"POST /v1/clusters/{cluster}/readiness/recheck", &accessOperator, s.recheckClusterReadiness},
		{"GET /v1/clusters/{cluster}/readiness/history", &accessViewer, s.getClusterReadinessHistory},
		{"GET /v1/admin/clusters/{cluster}/install", &accessAdmin, s.getClusterInstall},
		// Maintenance.
		{"GET /v1/admin/maintenance", &accessViewer, s.getMaintenance},
		{"POST /v1/admin/maintenance/update-check", &accessAdmin, s.checkForUpdate},
		// Notifications.
		{"GET /v1/admin/notifications/status", &accessViewer, s.getNotificationStatus},
		{"GET /v1/admin/notifications/channels", &accessViewer, s.listNotificationChannels},
		{"POST /v1/admin/notifications/channels", &accessAdmin, s.createNotificationChannel},
		{"GET /v1/admin/notifications/channels/{channelId}", &accessViewer, s.getNotificationChannel},
		{"PUT /v1/admin/notifications/channels/{channelId}", &accessAdmin, s.updateNotificationChannel},
		{"DELETE /v1/admin/notifications/channels/{channelId}", &accessAdmin, s.deleteNotificationChannel},
		{"POST /v1/admin/notifications/channels/{channelId}/test", &accessAdmin, s.testNotificationChannel},
		{"GET /v1/admin/notifications/rules", &accessViewer, s.listNotificationRules},
		{"POST /v1/admin/notifications/rules", &accessAdmin, s.createNotificationRule},
		{"PUT /v1/admin/notifications/rules/{ruleId}", &accessAdmin, s.updateNotificationRule},
		{"DELETE /v1/admin/notifications/rules/{ruleId}", &accessAdmin, s.deleteNotificationRule},
		{"GET /v1/admin/notifications/digest", &accessViewer, s.getNotificationDigest},
		{"PUT /v1/admin/notifications/digest", &accessAdmin, s.updateNotificationDigest},
		{"POST /v1/admin/notifications/digest/send", &accessAdmin, s.sendNotificationDigest},
		{"GET /v1/admin/notifications/deliveries", &accessViewer, s.listNotificationDeliveries},
		// Pricing service.
		{"GET /v1/internal/metrics-bindings", &accessPricing, s.listMetricsBindings},
		{"GET /v1/internal/datasources/{datasourceId}/api/v1/query", &accessPricing, s.proxyQuery},
		{"POST /v1/internal/datasources/{datasourceId}/api/v1/query", &accessPricing, s.proxyQuery},
		{"GET /v1/internal/datasources/{datasourceId}/api/v1/query_range", &accessPricing, s.proxyQuery},
		{"POST /v1/internal/datasources/{datasourceId}/api/v1/query_range", &accessPricing, s.proxyQuery},
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.apiRoutes() {
		var h http.Handler = rt.handler
		if rt.access != nil {
			h = s.authenticate(*rt.access, rt.handler)
		}
		mux.Handle(rt.pattern, s.instrument(rt.pattern, h))
	}

	// Probes and metrics: outside /v1, no credentials.
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.Metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		ready := s.ready
		s.mu.Unlock()
		if !ready || s.Store.Pool.Ping(r.Context()) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	return s.recoverer(s.cors(mux))
}

// cors allows the configured browser origins (dev: the UI dev server).
func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.Cfg.CORSOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowed[origin] || allowed["*"]) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			// Sessions (D-023): credentials only for explicitly listed origins, never for "*".
			if allowed[origin] {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			h.Set("Access-Control-Expose-Headers", "ETag, Retry-After")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match, If-Match, "+hubapi.CSRFHeader)
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.Log.ErrorContext(r.Context(), "panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	w.code = c
	w.ResponseWriter.WriteHeader(c)
}

func (s *Server) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		s.Metrics.observeRequest(route, sw.code, time.Since(start))
		s.Log.DebugContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path, "status", sw.code, "duration", time.Since(start))
	})
}

// heartbeat records the agent's last request, at most every 15 s per cluster unless the agent
// version is given.
func (s *Server) heartbeat(ctx context.Context, cluster, version string) {
	now := s.now()
	s.mu.Lock()
	if s.heartbeats == nil {
		s.heartbeats = map[string]time.Time{}
	}
	last := s.heartbeats[cluster]
	if version == "" && now.Sub(last) < 15*time.Second {
		s.mu.Unlock()
		return
	}
	s.heartbeats[cluster] = now
	s.mu.Unlock()
	if err := s.Store.HeartbeatAt(ctx, cluster, version, now); err != nil {
		s.Log.WarnContext(ctx, "heartbeat", "cluster", cluster, "err", err)
	}
}

// ---- responses ----

// recompute serves POST /v1/admin/recompute (D-019).
func (s *Server) recompute(w http.ResponseWriter, r *http.Request) {
	cluster := r.URL.Query().Get("cluster")
	if cluster != "" {
		if !hubapi.ClusterNameRE.MatchString(cluster) {
			badRequest(w, r, "invalid_cluster", "Invalid cluster name.")
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
	}
	if s.Job == nil { // always set by cmd/hub
		s.internal(w, r, errors.New("no recommender job"))
		return
	}
	id, at, coalesced, err := s.Job.Trigger(r.Context(), cluster, PrincipalFrom(r.Context()).Actor().Name)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.Log.InfoContext(r.Context(), "recompute requested", "run", id, "cluster", cluster, "coalesced", coalesced)
	writeJSONStatus(w, http.StatusAccepted, hubapi.RecomputeAccepted{RunID: id.String(), Cluster: cluster, RequestedAt: at.UTC(), Coalesced: coalesced})
}

// notImplemented answers routes of later work-orders (D-025, WO-25).
func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusNotImplemented, hubapi.CodeNotImplemented, "Not implemented yet (WO-25).")
}

// writeJSON writes a 200 JSON response.
func writeJSON(w http.ResponseWriter, v any) { writeJSONStatus(w, http.StatusOK, v) }

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(hubapi.Problem{
		Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Instance: r.URL.Path, Code: code,
	})
}

func badRequest(w http.ResponseWriter, r *http.Request, code, detail string) {
	writeProblem(w, r, http.StatusBadRequest, code, detail)
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	// The client went away (e.g. the UI navigated): nothing to answer, nothing failed.
	if r.Context().Err() != nil && errors.Is(err, context.Canceled) {
		s.Log.DebugContext(r.Context(), "request canceled by client", "path", r.URL.Path)
		return
	}
	s.Log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
	writeProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
}

// ---- pagination ----

type cursor struct {
	Offset int       `json:"o,omitempty"`
	Key    string    `json:"k,omitempty"`
	Time   time.Time `json:"t,omitzero"`
	ID     string    `json:"i,omitempty"`
	// Q binds the cursor to the query it was issued for.
	Q string `json:"q,omitempty"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}
