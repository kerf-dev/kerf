// Package dynatracetest is a fake Dynatrace query API for tests: it answers DQL queries by
// matching substrings of the query text against rules, with records from fixtures
// (internal/hub/datasource/dynatrace/testdata) or inline JSON.
package dynatracetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Token is the platform token the fake expects (Authorization: Bearer Token).
const Token = "dt0s16.TESTTOKEN.synthetic"

// Rule answers every query that contains all of Contains.
type Rule struct {
	Contains []string
	// Records is the JSON array of result records (inline, or Fixture(name)).
	Records string
	// Units maps metric keys to the unit reported in the result metadata.
	Units map[string]string
	// Status, when set, answers with this HTTP status and Body instead.
	Status int
	Body   string
}

// Server is a running fake.
type Server struct {
	*httptest.Server
	t     *testing.T
	mu    sync.Mutex
	rules []Rule
	// Queries are the DQL queries received, in order.
	Queries []string
	// Running makes execute answer RUNNING with a request token; poll then answers.
	Running bool
	// TooMany answers this many requests with 429 (Retry-After: 0) before the real answer.
	TooMany int
	pending map[string]string
	polls   int
}

// New starts a fake; it is closed when the test ends.
func New(t *testing.T, rules ...Rule) *Server {
	t.Helper()
	s := &Server{t: t, rules: rules, pending: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Add appends rules (checked after the existing ones).
func (s *Server) Add(rules ...Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, rules...)
}

// Count returns how many queries contained every one of sub.
func (s *Server) Count(sub ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.Queries {
		if containsAll(q, sub) {
			n++
		}
	}
	return n
}

// Polls is the number of poll requests.
func (s *Server) Polls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.polls
}

func containsAll(q string, sub []string) bool {
	for _, c := range sub {
		if !strings.Contains(q, c) {
			return false
		}
	}
	return true
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"Token is missing required scope or is invalid"}}`)
		return
	}
	s.mu.Lock()
	if s.TooMany > 0 {
		s.TooMany--
		s.mu.Unlock()
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Too many requests"}}`)
		return
	}
	s.mu.Unlock()
	var query string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/platform/storage/query/v1/query:execute":
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		query = body.Query
		s.mu.Lock()
		s.Queries = append(s.Queries, query)
		if s.Running {
			tok := fmt.Sprintf("tok-%d", len(s.Queries))
			s.pending[tok] = query
			s.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "RUNNING", "requestToken": tok})
			return
		}
		s.mu.Unlock()
	case r.Method == http.MethodGet && r.URL.Path == "/platform/storage/query/v1/query:poll":
		s.mu.Lock()
		s.polls++
		q, ok := s.pending[r.URL.Query().Get("request-token")]
		s.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":{"code":410,"message":"unknown request token"}}`, http.StatusGone)
			return
		}
		query = q
	default:
		http.NotFound(w, r)
		return
	}
	s.answer(w, query)
}

func (s *Server) answer(w http.ResponseWriter, query string) {
	s.mu.Lock()
	var rule *Rule
	for i := range s.rules {
		if containsAll(query, s.rules[i].Contains) {
			rule = &s.rules[i]
			break
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if rule == nil {
		_, _ = io.WriteString(w, `{"state":"SUCCEEDED","result":{"records":[],"types":[],"metadata":{}}}`)
		return
	}
	if rule.Status != 0 {
		w.WriteHeader(rule.Status)
		_, _ = io.WriteString(w, rule.Body)
		return
	}
	records := rule.Records
	if records == "" {
		records = "[]"
	}
	var metrics []map[string]string
	for k, u := range rule.Units {
		metrics = append(metrics, map[string]string{"metric.key": k, "unit": u})
	}
	meta, _ := json.Marshal(map[string]any{"metrics": metrics})
	_, _ = fmt.Fprintf(w, `{"state":"SUCCEEDED","result":{"records":%s,"types":[],"metadata":%s}}`, records, meta)
}

// Fixture returns the content of testdata/<name> of the dynatrace package.
func Fixture(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// BearerClient is an HTTP client that sends Token, like the hub's datasource client.
func BearerClient() *http.Client {
	return &http.Client{Transport: bearer{}}
}

type bearer struct{}

func (bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+Token)
	return http.DefaultTransport.RoundTrip(r)
}

// Healthy returns rules for a cluster whose Kubernetes monitoring records everything Kerf maps
// (the fixtures plus series counts, a fresh 1-minute series and 30 days of history at now).
// CPU metrics report cpuUnit (e.g. MilliCores).
func Healthy(t *testing.T, now time.Time, cpuUnit string) []Rule {
	t.Helper()
	cpu := map[string]string{
		"dt.kubernetes.container.cpu_usage": cpuUnit, "dt.kubernetes.container.requests_cpu": cpuUnit,
		"dt.kubernetes.container.limits_cpu": cpuUnit, "dt.kubernetes.node.cpu_allocatable": cpuUnit,
	}
	bytes := map[string]string{"dt.kubernetes.container.memory_working_set": "Byte", "dt.kubernetes.container.requests_memory": "Byte",
		"dt.kubernetes.container.limits_memory": "Byte", "dt.kubernetes.node.memory_allocatable": "Byte"}
	rows := func(n int, dims ...string) string {
		var out []map[string]string
		for i := 0; i < n; i++ {
			r := map[string]string{}
			for _, d := range dims {
				r[d] = fmt.Sprintf("%s-%d", strings.ReplaceAll(d, ".", "-"), i)
			}
			out = append(out, r)
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	owners, _ := json.Marshal([]map[string]string{
		{"k8s.namespace.name": "shop", "k8s.pod.name": "web-7d4f-a", "k8s.workload.kind": "deployment", "k8s.workload.name": "web"},
		{"k8s.namespace.name": "shop", "k8s.pod.name": "web-7d4f-b", "k8s.workload.kind": "deployment", "k8s.workload.name": "web"},
		{"k8s.namespace.name": "shop", "k8s.pod.name": "db-0", "k8s.workload.kind": "statefulset", "k8s.workload.name": "db"},
	})
	minute := now.Truncate(time.Minute)
	fresh := make([]any, 60)
	for i := range fresh {
		fresh[i] = 1.0e8
	}
	fresh[59] = nil // the newest minute is still being ingested
	freshRec, _ := json.Marshal([]map[string]any{{"timeframe": map[string]string{"start": minute.Add(-time.Hour).UTC().Format(time.RFC3339)}, "interval": "60000000000", "v": fresh}})
	day := 24 * time.Hour
	end := now.Truncate(day).Add(day)
	hist := make([]any, 31)
	for i := range hist {
		hist[i] = 1000.0
	}
	histRec, _ := json.Marshal([]map[string]any{{"timeframe": map[string]string{"start": end.Add(-31 * day).UTC().Format(time.RFC3339)}, "interval": fmt.Sprint(int64(day)), "v": hist}})
	container := []string{"k8s.namespace.name", "k8s.workload.kind", "k8s.workload.name", "k8s.pod.name", "k8s.container.name"}
	count := func(key string, n int) Rule {
		return Rule{Contains: []string{"count(" + key + ", scalar: true), by: {k8s.namespace.name, k8s.workload.kind"}, Records: rows(n, container...)}
	}
	return []Rule{
		{Contains: []string{"data record"}, Records: `[{"ok":"1"}]`},
		// usage, pods and inventory (recommender)
		{Contains: []string{"avg(dt.kubernetes.container.cpu_usage)", "vsum"}, Records: Fixture(t, "usage_cpu.json"), Units: cpu},
		{Contains: []string{"max(dt.kubernetes.container.memory_working_set)", "vsum"}, Records: Fixture(t, "usage_memory.json"), Units: bytes},
		{Contains: []string{"summarize pods"}, Records: Fixture(t, "pods.json")},
		{Contains: []string{"timeseries v = max(dt.kubernetes.container.requests_cpu)"}, Records: Fixture(t, "resource_cpu.json"), Units: cpu},
		{Contains: []string{"timeseries v = max(dt.kubernetes.container.limits_cpu)"}, Records: Fixture(t, "resource_cpu.json"), Units: cpu},
		{Contains: []string{"timeseries v = max(dt.kubernetes.container.requests_memory)"}, Records: Fixture(t, "resource_memory.json"), Units: bytes},
		{Contains: []string{"timeseries v = max(dt.kubernetes.container.limits_memory)"}, Records: Fixture(t, "resource_memory.json"), Units: bytes},
		{Contains: []string{"avg(dt.containers.cpu.throttling_ratio)"}, Records: Fixture(t, "throttle.json"), Units: map[string]string{"dt.containers.cpu.throttling_ratio": "Percent"}},
		{Contains: []string{"sum(dt.kubernetes.container.oom_kills, scalar: true)"}, Records: Fixture(t, "oom_kills.json")},
		{Contains: []string{"sum(dt.kubernetes.container.restarts, scalar: true)"}, Records: Fixture(t, "restarts.json")},
		// nodes
		{Contains: []string{"smartscapeNodes K8S_NODE"}, Records: Fixture(t, "node_labels.json")},
		{Contains: []string{"max(dt.kubernetes.node.cpu_allocatable)"}, Records: Fixture(t, "node_cpu_allocatable.json"), Units: cpu},
		{Contains: []string{"max(dt.kubernetes.node.memory_allocatable)"}, Records: Fixture(t, "node_memory_allocatable.json"), Units: bytes},
		// readiness: series counts, owners, cluster names, freshness, history
		{Contains: []string{"by: {k8s.cluster.name}"}, Records: `[{"k8s.cluster.name":"prod-eu1"},{"k8s.cluster.name":"staging-eu1"}]`},
		{Contains: []string{"count(dt.kubernetes.container.memory_working_set, scalar: true), by: {k8s.namespace.name, k8s.pod.name"}, Records: string(owners)},
		count("dt.kubernetes.container.cpu_usage", 4),
		count("dt.kubernetes.container.memory_working_set", 4),
		count("dt.kubernetes.container.requests_cpu", 4),
		count("dt.kubernetes.container.requests_memory", 4),
		count("dt.kubernetes.container.limits_cpu", 3),
		count("dt.kubernetes.container.limits_memory", 3),
		count("dt.containers.cpu.throttling_ratio", 3),
		count("dt.kubernetes.container.restarts", 2),
		count("dt.kubernetes.container.oom_kills", 1),
		{Contains: []string{"count(dt.kubernetes.node.cpu_allocatable, scalar: true)"}, Records: rows(3, "k8s.node.name")},
		{Contains: []string{"count(dt.kubernetes.node.memory_allocatable, scalar: true)"}, Records: rows(3, "k8s.node.name")},
		{Contains: []string{"timeseries v = max(dt.kubernetes.container.memory_working_set), filter", "interval: 1m"}, Records: string(freshRec), Units: bytes},
		{Contains: []string{"timeseries v = count(dt.kubernetes.container.memory_working_set), filter", "interval: 1d"}, Records: string(histRec)},
	}
}
