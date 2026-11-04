//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// hubAPI reads the hub's UI endpoints (role `ui`, api/openapi/hub.yaml) and, when an admin token
// is available, triggers recommender runs (D-019, POST /v1/admin/recompute).
type hubAPI struct {
	Base       string
	Token      string
	AdminToken string

	mu            sync.Mutex
	lastRecompute time.Time
	noRecompute   bool
}

// recomputeEvery limits suite-wide recompute requests: every run covers every cluster.
const recomputeEvery = 2 * time.Minute

// maybeRecompute asks the hub for a recommender run, at most every recomputeEvery across the
// suite. It is a best-effort speed-up: without an admin token, or when the hub has no such
// endpoint (404/405), the suite relies on the periodic run.
func (h *hubAPI) maybeRecompute(ctx context.Context, logf func(string, ...any)) {
	h.mu.Lock()
	if h.noRecompute || h.AdminToken == "" || time.Since(h.lastRecompute) < recomputeEvery {
		h.mu.Unlock()
		return
	}
	h.lastRecompute = time.Now()
	h.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/v1/admin/recompute", nil)
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
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		h.mu.Lock()
		h.noRecompute = true
		h.mu.Unlock()
		logf("hub has no recompute trigger (%s); waiting for its periodic run", resp.Status)
	case resp.StatusCode/100 != 2:
		logf("recompute: %s: %s", resp.Status, b)
	default:
		logf("hub recompute triggered (%s)", resp.Status)
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
