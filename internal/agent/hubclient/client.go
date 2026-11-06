package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Client calls the agent endpoints of the hub API for one cluster.
type Client struct {
	baseURL *url.URL
	cluster string
	token   TokenSource
	http    *http.Client

	clockMu sync.Mutex
	hubDate time.Time // Date header of the last hub response
	localAt time.Time // local time it was received
}

// HubClock returns the Date header of the last hub response and the local time it arrived (zero
// before the first response): the agent.clock-skew self-check (D-025).
func (c *Client) HubClock() (hubDate, localAt time.Time) {
	c.clockMu.Lock()
	defer c.clockMu.Unlock()
	return c.hubDate, c.localAt
}

// TokenSource returns the bearer token for each request.
type TokenSource func() (string, error)

// StaticToken returns a TokenSource for a fixed token.
func StaticToken(t string) TokenSource { return func() (string, error) { return t, nil } }

// FileToken reads the token from a file on every call, so a rotated Secret is picked up
// without a restart. An empty path or missing file yields an empty token.
func FileToken(path string) TokenSource {
	return func() (string, error) {
		if path == "" {
			return "", nil
		}
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
}

// New returns a client for baseURL (e.g. http://hub-api.hub.svc:8080) and cluster.
func New(baseURL, cluster string, token TokenSource, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("hub url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("hub url %q: scheme must be http or https", baseURL)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	if token == nil {
		token = StaticToken("")
	}
	return &Client{baseURL: u, cluster: cluster, token: token, http: hc}, nil
}

// APIError is a non-2xx response.
type APIError struct {
	StatusCode int
	Problem    Problem
}

func (e *APIError) Error() string {
	msg := e.Problem.Detail
	if msg == "" {
		msg = e.Problem.Title
	}
	if e.Problem.Code != "" {
		msg = e.Problem.Code + ": " + msg
	}
	return fmt.Sprintf("hub: HTTP %d: %s", e.StatusCode, msg)
}

// ErrNotModified is returned by ListRecommendations when the ETag still matches.
var ErrNotModified = errors.New("hub: not modified")

// ListRecommendations fetches every page of the cluster's recommendations. etag is the ETag of
// the previous successful call ("" for none); on 304 it returns ErrNotModified.
func (c *Client) ListRecommendations(ctx context.Context, etag string) ([]WorkloadRecommendation, string, error) {
	var (
		all      []WorkloadRecommendation
		cont     string
		gotETag  string
		firstReq = true
	)
	for {
		q := url.Values{}
		if cont != "" {
			q.Set("continue", cont)
		}
		hdr := http.Header{}
		if firstReq && etag != "" {
			hdr.Set("If-None-Match", etag)
		}
		var page RecommendationList
		status, respHdr, err := c.do(ctx, http.MethodGet, c.path("recommendations"), q, hdr, nil, &page)
		if err != nil {
			return nil, "", err
		}
		if status == http.StatusNotModified {
			return nil, etag, ErrNotModified
		}
		if firstReq {
			gotETag = respHdr.Get("ETag")
		}
		firstReq = false
		all = append(all, page.Items...)
		if page.Continue == "" {
			return all, gotETag, nil
		}
		cont = page.Continue
	}
}

// ReportWorkloadStatus upserts workload statuses, split into batches of MaxBatch. The batch
// fields besides Items (agentVersion, configVersion, policyInventory, capabilities, checks) are
// sent with the first request; configVersion and agentVersion with every one. A batch without
// items is still sent when it carries an inventory, capabilities or checks.
func (c *Client) ReportWorkloadStatus(ctx context.Context, batch WorkloadStatusBatch) error {
	items := batch.Items
	if len(items) == 0 && batch.PolicyInventory == nil && batch.Capabilities == nil && batch.Checks == nil && batch.Components == nil {
		return nil
	}
	for start := 0; start == 0 || start < len(items); start += MaxBatch {
		end := min(start+MaxBatch, len(items))
		body := WorkloadStatusBatch{AgentVersion: batch.AgentVersion, ConfigVersion: batch.ConfigVersion,
			Items: append([]WorkloadStatus{}, items[start:end]...)}
		if start == 0 {
			body.PolicyInventory, body.Capabilities, body.Checks = batch.PolicyInventory, batch.Capabilities, batch.Checks
			body.Components = batch.Components
		}
		var res BatchResult
		if _, _, err := c.do(ctx, http.MethodPost, c.path("workload-status"), nil, nil, body, &res); err != nil {
			return err
		}
		if end >= len(items) {
			break
		}
	}
	return nil
}

// GetConfig fetches the cluster's hub configuration (D-022). etag is the ETag of the previous
// successful call ("" for none); on 304 it returns ErrNotModified. A hub that predates the admin
// area answers 404 (see IsNotFound).
func (c *Client) GetConfig(ctx context.Context, etag string) (*ClusterConfig, string, error) {
	hdr := http.Header{}
	if etag != "" {
		hdr.Set("If-None-Match", etag)
	}
	var cfg ClusterConfig
	status, respHdr, err := c.do(ctx, http.MethodGet, c.path("config"), nil, hdr, nil, &cfg)
	if err != nil {
		return nil, "", err
	}
	if status == http.StatusNotModified {
		return nil, etag, ErrNotModified
	}
	return &cfg, respHdr.Get("ETag"), nil
}

// IsNotFound reports whether err is a 404 or 501 from the hub (an endpoint it does not serve).
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusNotImplemented)
}

// ReportEvents sends one batch of at most MaxBatch events.
func (c *Client) ReportEvents(ctx context.Context, items []Event) (BatchResult, error) {
	var res BatchResult
	if len(items) > MaxBatch {
		return res, fmt.Errorf("hub: %d events exceed the batch limit %d", len(items), MaxBatch)
	}
	_, _, err := c.do(ctx, http.MethodPost, c.path("events"), nil, nil, EventBatch{Items: items}, &res)
	return res, err
}

func (c *Client) path(suffix string) string {
	return "/v1/clusters/" + url.PathEscape(c.cluster) + "/" + suffix
}

// do performs one request and decodes a 2xx body into out. It returns the status and headers;
// the body is always closed here.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, hdr http.Header, in, out any) (int, http.Header, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tok, err := c.token()
	if err != nil {
		return 0, nil, fmt.Errorf("hub token: %w", err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		c.clockMu.Lock()
		c.hubDate, c.localAt = d, time.Now()
		c.clockMu.Unlock()
	}
	if resp.StatusCode == http.StatusNotModified {
		return resp.StatusCode, resp.Header, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		_ = json.Unmarshal(data, &apiErr.Problem)
		if apiErr.Problem.Title == "" && apiErr.Problem.Detail == "" {
			apiErr.Problem.Detail = strings.TrimSpace(string(data[:min(len(data), 256)]))
		}
		return 0, nil, apiErr
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return 0, nil, fmt.Errorf("hub: decode %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, resp.Header, nil
}
