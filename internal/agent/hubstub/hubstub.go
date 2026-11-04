// Package hubstub is an in-process stand-in for the hub's agent endpoints
// (api/openapi/hub.yaml, tag "agent"). It is used by the agent's tests and by `agent stub-hub`
// on the local k3d clusters until the real hub (WO-04) is deployed.
//
// Implemented: bearer-token auth bound to one cluster, ETag / If-None-Match on recommendations,
// limit/continue pagination, workload-status upserts and idempotent event storage by id.
package hubstub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

// Server is the stub hub. Zero value is not usable; use New.
type Server struct {
	mu       sync.Mutex
	tokens   map[string]string // token -> cluster ("" = admin, any cluster)
	recs     map[string][]hubclient.WorkloadRecommendation
	statuses map[string]map[string]hubclient.WorkloadStatus // cluster -> ns/kind/name
	events   map[string][]hubclient.Event                   // cluster -> events in arrival order
	eventIDs map[string]bool
	down     bool
	requests map[string]int
}

// New returns an empty stub hub.
func New() *Server {
	return &Server{
		tokens:   map[string]string{},
		recs:     map[string][]hubclient.WorkloadRecommendation{},
		statuses: map[string]map[string]hubclient.WorkloadStatus{},
		events:   map[string][]hubclient.Event{},
		eventIDs: map[string]bool{},
		requests: map[string]int{},
	}
}

// AddToken registers an agent token for cluster ("" = admin token valid for every cluster).
func (s *Server) AddToken(token, cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = cluster
}

// SetRecommendations replaces the recommendations of a cluster.
func (s *Server) SetRecommendations(cluster string, recs []hubclient.WorkloadRecommendation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[cluster] = slices.Clone(recs)
}

// SetDown makes every request fail with 503 (simulates an unreachable hub).
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Statuses returns the stored workload statuses of a cluster, keyed "ns/Kind/name".
func (s *Server) Statuses(cluster string) map[string]hubclient.WorkloadStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]hubclient.WorkloadStatus{}
	for k, v := range s.statuses[cluster] {
		out[k] = v
	}
	return out
}

// Events returns the stored events of a cluster in arrival order.
func (s *Server) Events(cluster string) []hubclient.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events[cluster])
}

// Requests returns how many requests each operation received.
func (s *Server) Requests(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[op]
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/clusters/{cluster}/recommendations", s.auth("listRecommendations", s.listRecommendations))
	mux.HandleFunc("POST /v1/clusters/{cluster}/workload-status", s.auth("reportWorkloadStatus", s.reportStatus))
	mux.HandleFunc("POST /v1/clusters/{cluster}/events", s.auth("reportEvents", s.reportEvents))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (s *Server) auth(op string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests[op]++
		down := s.down
		tok, hasAuth := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		bound, known := s.tokens[tok]
		open := len(s.tokens) == 0
		s.mu.Unlock()
		if down {
			problem(w, http.StatusServiceUnavailable, "unavailable", "stub hub is down")
			return
		}
		if !open && (!hasAuth || !known) {
			problem(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		if !open && bound != "" && bound != r.PathValue("cluster") {
			problem(w, http.StatusForbidden, "cluster_forbidden", "token is not valid for this cluster")
			return
		}
		next(w, r)
	}
}

func (s *Server) listRecommendations(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	ns := r.URL.Query().Get("namespace")
	s.mu.Lock()
	var items []hubclient.WorkloadRecommendation
	for _, rec := range s.recs[cluster] {
		if ns == "" || rec.Workload.Namespace == ns {
			items = append(items, rec)
		}
	}
	s.mu.Unlock()

	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	start := 0
	if c := r.URL.Query().Get("continue"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 0 || n > len(items) {
			problem(w, http.StatusBadRequest, "invalid_continue", "bad continue token")
			return
		}
		start = n
	}
	end := len(items)
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > 5000 {
			problem(w, http.StatusBadRequest, "invalid_limit", "limit must be 1..5000")
			return
		}
		end = min(start+n, len(items))
	}
	out := hubclient.RecommendationList{Cluster: cluster, Items: items[start:end]}
	if out.Items == nil {
		out.Items = []hubclient.WorkloadRecommendation{}
	}
	if end < len(items) {
		out.Continue = strconv.Itoa(end)
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, out)
}

func (s *Server) reportStatus(w http.ResponseWriter, r *http.Request) {
	var batch hubclient.WorkloadStatusBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		problem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if len(batch.Items) > hubclient.MaxBatch {
		problem(w, http.StatusRequestEntityTooLarge, "too_large", "more than 1000 items")
		return
	}
	for _, it := range batch.Items {
		if it.RecommendationID != "" && !isUUID(it.RecommendationID) {
			problem(w, http.StatusBadRequest, "invalid_body", "recommendationID must be a UUID")
			return
		}
		if it.Workload.Name == "" || it.Phase == "" || it.Modes.CPU == "" || it.Modes.Memory == "" {
			problem(w, http.StatusBadRequest, "invalid_body", "workload, modes and phase are required")
			return
		}
	}
	cluster := r.PathValue("cluster")
	s.mu.Lock()
	if s.statuses[cluster] == nil {
		s.statuses[cluster] = map[string]hubclient.WorkloadStatus{}
	}
	for _, it := range batch.Items {
		s.statuses[cluster][it.Workload.Namespace+"/"+it.Workload.Kind+"/"+it.Workload.Name] = it
	}
	s.mu.Unlock()
	writeJSON(w, hubclient.BatchResult{Accepted: len(batch.Items)})
}

func (s *Server) reportEvents(w http.ResponseWriter, r *http.Request) {
	var batch hubclient.EventBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		problem(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if len(batch.Items) > hubclient.MaxBatch {
		problem(w, http.StatusRequestEntityTooLarge, "too_large", "more than 1000 items")
		return
	}
	for _, e := range batch.Items {
		if !isUUID(e.ID) || (e.RecommendationID != "" && !isUUID(e.RecommendationID)) {
			problem(w, http.StatusBadRequest, "invalid_body", "id and recommendationID must be UUIDs")
			return
		}
		if e.ID == "" || e.Type == "" || e.Reason == "" || e.Workload.Name == "" || e.Time.IsZero() {
			problem(w, http.StatusBadRequest, "invalid_body", fmt.Sprintf("event %q misses a required field", e.ID))
			return
		}
	}
	cluster := r.PathValue("cluster")
	res := hubclient.BatchResult{}
	s.mu.Lock()
	for _, e := range batch.Items {
		if s.eventIDs[e.ID] {
			res.Duplicates++
			continue
		}
		s.eventIDs[e.ID] = true
		s.events[cluster] = append(s.events[cluster], e)
		res.Accepted++
	}
	s.mu.Unlock()
	writeJSON(w, res)
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isUUID mirrors the hub's validation of `format: uuid` fields.
func isUUID(s string) bool { return uuidRe.MatchString(s) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(hubclient.Problem{Title: http.StatusText(status), Status: status, Detail: detail, Code: code})
}

// FileRecommendations is the format of the file served by `agent stub-hub --recommendations`:
// recommendations per cluster. Missing ids, generatedAt and dataCoverage are filled in so that a
// hand-written file stays short.
type FileRecommendations map[string][]hubclient.WorkloadRecommendation

// Normalize fills defaults into hand-written recommendations.
func Normalize(recs []hubclient.WorkloadRecommendation, now time.Time) []hubclient.WorkloadRecommendation {
	out := slices.Clone(recs)
	for i := range out {
		r := &out[i]
		if r.ID == "" {
			// Stable across restarts: derived from the workload and the recommended values.
			body, _ := json.Marshal(struct {
				W hubclient.WorkloadRef
				C []hubclient.ContainerRecommendation
				S hubclient.ValueSettings
			}{r.Workload, r.Containers, r.Settings})
			sum := sha256.Sum256(body)
			h := hex.EncodeToString(sum[:16])
			r.ID = h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
		}
		if r.GeneratedAt.IsZero() {
			r.GeneratedAt = now.UTC().Truncate(time.Second)
		}
		if r.DataCoverage.Window == "" {
			r.DataCoverage = hubclient.DataCoverage{Ratio: 1, Window: "336h0m0s", Covered: "336h0m0s"}
		}
		if r.RiskFlags == nil {
			r.RiskFlags = []string{}
		}
	}
	return out
}
