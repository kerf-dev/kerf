// Package hub is the central service: HTTP API (api/openapi/hub.yaml), recommender scheduler,
// Postgres persistence and the ro_recommendation_* metrics.
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

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// Server serves the hub API.
type Server struct {
	Cfg     Config
	Store   *store.Store
	Prom    recommender.Querier
	RecCfg  recommender.Config
	Metrics *Metrics
	Log     *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time

	mu         sync.Mutex
	tokens     map[string]tokenEntry
	heartbeats map[string]time.Time
	ready      bool
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

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	agent := []string{hubapi.RoleAgent}
	ui := []string{hubapi.RoleUI}
	route := func(pattern string, roles []string, h http.HandlerFunc) {
		name := pattern
		mux.Handle(pattern, s.instrument(name, s.authenticate(roles, h)))
	}
	route("GET /v1/clusters/{cluster}/recommendations", agent, s.listRecommendations)
	route("POST /v1/clusters/{cluster}/workload-status", agent, s.reportWorkloadStatus)
	route("POST /v1/clusters/{cluster}/events", agent, s.reportEvents)
	route("GET /v1/audit", []string{hubapi.RoleAgent, hubapi.RoleUI}, s.queryAudit)
	route("GET /v1/summary", ui, s.getSummary)
	route("GET /v1/clusters", ui, s.listClusters)
	route("GET /v1/workloads", ui, s.listWorkloads)
	route("GET /v1/workloads/{id}", ui, s.getWorkload)
	route("GET /v1/workloads/{id}/usage", ui, s.getWorkloadUsage)
	route("GET /v1/workloads/{id}/history", ui, s.getWorkloadHistory)
	route("GET /v1/savings/trend", ui, s.getSavingsTrend)

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
			h.Set("Access-Control-Expose-Headers", "ETag")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
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

// authenticate checks the bearer token and its role. Agent tokens are additionally restricted to
// their cluster for /v1/clusters/{cluster}/... routes; unknown clusters are 404.
func (s *Server) authenticate(roles []string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		raw, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="resource-optimizer"`)
			writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "Missing bearer token.")
			return
		}
		tok, err := s.lookupToken(r.Context(), strings.TrimSpace(raw))
		if errors.Is(err, store.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="resource-optimizer", error="invalid_token"`)
			writeProblem(w, r, http.StatusUnauthorized, "invalid_token", "Unknown, expired or revoked token.")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if tok.Role != hubapi.RoleAdmin && !hubapi.Contains(roles, tok.Role) {
			writeProblem(w, r, http.StatusForbidden, "role_forbidden", "Token role "+tok.Role+" may not use this endpoint.")
			return
		}
		if cluster := r.PathValue("cluster"); cluster != "" {
			if tok.Role == hubapi.RoleAgent && tok.Cluster != cluster {
				writeProblem(w, r, http.StatusForbidden, "cluster_forbidden", "Token is not allowed to access cluster "+cluster+".")
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
		if tok.Role == hubapi.RoleAgent {
			s.heartbeat(r.Context(), tok.Cluster, "")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, tok)))
	})
}

func (s *Server) lookupToken(ctx context.Context, raw string) (store.Token, error) {
	key := string(store.HashToken(raw))
	now := s.now()
	s.mu.Lock()
	if e, ok := s.tokens[key]; ok && now.Before(e.expires) {
		s.mu.Unlock()
		return e.tok, nil
	}
	s.mu.Unlock()
	tok, err := s.Store.LookupToken(ctx, raw)
	if err != nil {
		return tok, err
	}
	s.mu.Lock()
	if s.tokens == nil {
		s.tokens = map[string]tokenEntry{}
	}
	s.tokens[key] = tokenEntry{tok: tok, expires: now.Add(s.Cfg.TokenCacheTTL.D())}
	s.mu.Unlock()
	return tok, nil
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
	if err := s.Store.Heartbeat(ctx, cluster, version); err != nil {
		s.Log.WarnContext(ctx, "heartbeat", "cluster", cluster, "err", err)
	}
}

// ---- responses ----

// writeJSON writes a 200 JSON response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
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
