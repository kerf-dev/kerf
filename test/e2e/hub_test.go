//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// hubAPI reads the hub's UI endpoints (role `ui`, api/openapi/hub.yaml).
type hubAPI struct {
	Base  string
	Token string
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
