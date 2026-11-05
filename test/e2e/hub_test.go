//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// hubAPI reads the hub's UI endpoints (role `ui`, api/openapi/hub.yaml) and, when an admin token
// is available, triggers recommender runs (D-019, POST /v1/admin/recompute).
type hubAPI struct {
	Base       string
	Token      string
	AdminToken string
	// RecomputeEvery limits the suite's recompute requests per cluster (E2E_RECOMPUTE_EVERY).
	RecomputeEvery time.Duration

	mu            sync.Mutex
	lastRecompute map[string]time.Time // per cluster
	noRecompute   bool
	holds         map[string]int // cluster -> > 0: no recompute requests (holdRecompute)
}

// holdRecompute stops the suite's recompute requests for the cluster until the returned function
// is called (or the test ends). Every recommender run gives every workload of the cluster a new
// recommendation id, even with the same values; a scenario that depends on the id staying the
// same for a minute (an approval, drift repair of an applied recommendation) holds the suite's
// extra runs for its cluster off meanwhile. The hub's periodic run still happens.
func (h *hubAPI) holdRecompute(t *testing.T, cluster string) (release func()) {
	h.mu.Lock()
	if h.holds == nil {
		h.holds = map[string]int{}
	}
	h.holds[cluster]++
	h.mu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			h.mu.Lock()
			h.holds[cluster]--
			h.mu.Unlock()
		})
	}
	t.Cleanup(release)
	return release
}

// holdWait bounds how long recomputeNow waits for other scenarios' holds.
const holdWait = 4 * time.Minute

// defaultRecomputeEvery: a run of one dev spoke takes a few seconds, and with the dev hub's
// recommender.endAlignment (30s) every run sees the last half minute of usage.
const defaultRecomputeEvery = 30 * time.Second

// maybeRecompute asks the hub for a recommender run of the cluster, at most every RecomputeEvery
// per cluster across the suite. It is a best-effort speed-up: without an admin token, or when the
// hub has no such endpoint (404/405), the suite relies on the periodic run.
func (h *hubAPI) maybeRecompute(ctx context.Context, cluster string, logf func(string, ...any)) {
	h.mu.Lock()
	if h.lastRecompute == nil {
		h.lastRecompute = map[string]time.Time{}
	}
	if h.noRecompute || h.AdminToken == "" || h.holds[cluster] > 0 || time.Since(h.lastRecompute[cluster]) < h.RecomputeEvery {
		h.mu.Unlock()
		return
	}
	h.lastRecompute[cluster] = time.Now()
	h.mu.Unlock()
	h.recompute(ctx, cluster, logf)
}

// recomputeNow asks for a recommender run of the cluster now, outside the rate limit. It first
// waits (up to holdWait) until no other scenario holds the cluster's recomputes: theirs would
// survive a run (approvals are re-approved, the D-027 self-heal check ends early), but they would
// check less. It reports false when the hub has no recompute trigger.
func (h *hubAPI) recomputeNow(ctx context.Context, cluster string, logf func(string, ...any)) bool {
	start := time.Now()
	for {
		h.mu.Lock()
		held := h.holds[cluster] > 0
		h.mu.Unlock()
		if !held || time.Since(start) > holdWait {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if d := time.Since(start); d > time.Second {
		logf("recompute of %s waited %s for other scenarios' holds", cluster, d.Round(time.Second))
	}
	h.mu.Lock()
	if h.noRecompute || h.AdminToken == "" {
		h.mu.Unlock()
		return false
	}
	if h.lastRecompute == nil {
		h.lastRecompute = map[string]time.Time{}
	}
	h.lastRecompute[cluster] = time.Now()
	h.mu.Unlock()
	h.recompute(ctx, cluster, logf)
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.noRecompute
}

func (h *hubAPI) recompute(ctx context.Context, cluster string, logf func(string, ...any)) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/v1/admin/recompute?cluster="+url.QueryEscape(cluster), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logf("recompute request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusNotFound && cluster != "" && strings.Contains(string(b), "cluster_not_found"):
		logf("recompute: unknown cluster %s", cluster)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		h.mu.Lock()
		h.noRecompute = true
		h.mu.Unlock()
		logf("hub has no recompute trigger (%s); waiting for its periodic run", resp.Status)
	case resp.StatusCode/100 != 2:
		logf("recompute %s: %s: %s", cluster, resp.Status, b)
	default:
		logf("hub recompute of %s triggered (%s)", cluster, resp.Status)
	}
}

type resourceValues struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type resources struct {
	Requests resourceValues `json:"requests"`
	Limits   resourceValues `json:"limits"`
}

type containerRec struct {
	Name        string    `json:"name"`
	Init        bool      `json:"init"`
	Current     resources `json:"current"`
	Recommended resources `json:"recommended"`
	RiskFlags   []string  `json:"riskFlags"`
}

type workloadRec struct {
	ID           string         `json:"id"`
	GeneratedAt  time.Time      `json:"generatedAt"`
	Containers   []containerRec `json:"containers"`
	Confidence   float64        `json:"confidence"`
	RiskFlags    []string       `json:"riskFlags"`
	DataCoverage struct {
		Covered string `json:"covered"`
		Samples int    `json:"samples"`
	} `json:"dataCoverage"`
}

type workloadDetail struct {
	ID             string       `json:"id"`
	Phase          string       `json:"phase"`
	Recommendation *workloadRec `json:"recommendation"`
}

type auditEvent struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	Time             time.Time `json:"time"`
	Reason           string    `json:"reason"`
	Strategy         string    `json:"strategy"`
	Resources        []string  `json:"resources"`
	RecommendationID string    `json:"recommendationID"`
	Message          string    `json:"message"`
}

func (h *hubAPI) get(ctx context.Context, path string, q url.Values, out any) (int, error) {
	u := h.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("GET %s: %s: %s", path, resp.Status, b)
	}
	return resp.StatusCode, json.Unmarshal(b, out)
}

// workload returns the hub's view of a workload, or nil when the hub doesn't know it yet.
func (h *hubAPI) workload(ctx context.Context, cluster, ns, kind, name string) (*workloadDetail, error) {
	id := fmt.Sprintf("%s/%s/%s/%s", cluster, ns, kind, name)
	var d workloadDetail
	code, err := h.get(ctx, "/v1/workloads/"+url.PathEscape(id), nil, &d)
	if code == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// audit returns the workload's events of the given types (all types when none), oldest first.
func (h *hubAPI) audit(ctx context.Context, cluster, ns, kind, name string, types ...string) ([]auditEvent, error) {
	q := url.Values{"cluster": {cluster}, "namespace": {ns}, "kind": {kind}, "workload": {name}, "limit": {"1000"}}
	for _, t := range types {
		q.Add("type", t)
	}
	var out struct {
		Items []auditEvent `json:"items"`
	}
	_, err := h.get(ctx, "/v1/audit", q, &out)
	return out.Items, err
}
