// Package dynatrace reads Kubernetes metrics from a Dynatrace environment with DQL (Grail query
// API) and maps them to the inputs of the recommender, the readiness checks and pricing. The
// mapping table (Inputs) says which Kerf input comes from which Dynatrace metric and which have
// no equivalent. docs/install/dynatrace.md.
package dynatrace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Query API paths (relative to the environment's platform URL).
const (
	executePath = "/platform/storage/query/v1/query:execute"
	pollPath    = "/platform/storage/query/v1/query:poll"
)

// Defaults of the client.
const (
	// DefaultMaxRecords is maxResultRecords of every query; one record is one series.
	DefaultMaxRecords = 200_000
	// DefaultRetries is how often a query is retried after HTTP 429 or 503.
	DefaultRetries = 4
	// requestTimeout is how long the execute and poll calls wait for a result before the query
	// is polled again.
	requestTimeout = 20 * time.Second
	// maxBody bounds a response.
	maxBody = 512 << 20
)

// Client runs DQL queries against one Dynatrace environment. Its HTTP client adds the token
// (Authorization: Bearer); Client never sees or logs it.
type Client struct {
	// Base is the platform base URL, e.g. https://abc12345.apps.dynatrace.com, or the ActiveGate
	// or proxy that forwards to it.
	Base string
	HTTP *http.Client
	// MaxRecords is maxResultRecords; 0 = DefaultMaxRecords.
	MaxRecords int
	// Retries after 429/503; 0 = DefaultRetries, < 0 = none.
	Retries int
	// Sleep waits between retries and polls; nil = a timer that honours ctx. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Backoff is the first retry delay without Retry-After (doubled per attempt); 0 = 1s.
	Backoff time.Duration
}

// PlatformURL returns the base URL of the query API for an environment URL: a classic
// *.live.dynatrace.com URL is changed to its *.apps.dynatrace.com platform URL; others are
// kept (managed and ActiveGate URLs).
func PlatformURL(env string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(env), "/"))
	if err != nil || u.Host == "" {
		return strings.TrimRight(env, "/")
	}
	if h := u.Hostname(); strings.HasSuffix(h, ".live.dynatrace.com") {
		u.Host = strings.TrimSuffix(h, ".live.dynatrace.com") + ".apps.dynatrace.com"
		if p := u.Port(); p != "" {
			u.Host += ":" + p
		}
	}
	return u.String()
}

// Error is an error answer of the query API.
type Error struct {
	Status int
	// Message is the API's error text (never the token).
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("dynatrace query API: HTTP %d: %s", e.Status, e.Message)
}

// Hint says what to fix for an API error.
func Hint(err error) string {
	var e *Error
	if !errors.As(err, &e) {
		return "check the environment URL and network access from the hub (an egress proxy or the ActiveGate URL)"
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return "the token is not valid for the query API: use a platform token (dt0s16…) of this environment, not a classic API token"
	case http.StatusForbidden:
		return "the token lacks a scope: it needs storage:metrics:read, storage:smartscape:read and storage:buckets:read"
	case http.StatusNotFound:
		return "the URL must be the environment URL, e.g. https://<environment-id>.apps.dynatrace.com (a .live. URL is changed to .apps. automatically)"
	case http.StatusTooManyRequests:
		return "the environment's query limits were reached: the hub retried with backoff; lower the number of clusters re-checked at once or raise the check interval"
	case http.StatusBadRequest:
		return "the query was rejected: check that the environment has Grail and Kubernetes monitoring (dt.kubernetes.* metrics)"
	}
	return "the query API answered with an error; see the message"
}

// Result is a finished query.
type Result struct {
	Records []map[string]json.RawMessage
	// Units maps metric keys and result field names to the unit the API reports.
	Units map[string]string
	// Notes are the API's notifications (e.g. a truncated result).
	Notes []string
}

type queryResponse struct {
	State        string `json:"state"`
	RequestToken string `json:"requestToken"`
	Result       *struct {
		Records  []map[string]json.RawMessage `json:"records"`
		Metadata struct {
			Grail struct {
				Notifications []struct {
					Severity         string `json:"severity"`
					NotificationType string `json:"notificationType"`
					Message          string `json:"message"`
				} `json:"notifications"`
			} `json:"grail"`
			Metrics []struct {
				MetricKey string `json:"metric.key"`
				FieldName string `json:"fieldName"`
				Unit      string `json:"unit"`
			} `json:"metrics"`
		} `json:"metadata"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
		Details struct {
			ErrorMessage string `json:"errorMessage"`
		} `json:"details"`
	} `json:"error"`
}

// Query runs a DQL query and waits for its result (polling while it runs).
func (c *Client) Query(ctx context.Context, dql string) (Result, error) {
	maxRecords := c.MaxRecords
	if maxRecords <= 0 {
		maxRecords = DefaultMaxRecords
	}
	body, err := json.Marshal(map[string]any{
		"query":                      dql,
		"requestTimeoutMilliseconds": requestTimeout.Milliseconds(),
		"maxResultRecords":           maxRecords,
		"timezone":                   "UTC",
		"locale":                     "en_US",
	})
	if err != nil {
		return Result{}, err
	}
	base := strings.TrimRight(c.Base, "/")
	r, err := c.do(ctx, http.MethodPost, base+executePath+"?enrich=metric-metadata", body)
	for err == nil && r.State != "SUCCEEDED" {
		switch r.State {
		case "RUNNING", "NOT_STARTED":
			if r.RequestToken == "" {
				return Result{}, errors.New("dynatrace query API: query running without a request token")
			}
			q := url.Values{"request-token": {r.RequestToken}, "request-timeout-milliseconds": {strconv.FormatInt(requestTimeout.Milliseconds(), 10)}, "enrich": {"metric-metadata"}}
			r, err = c.do(ctx, http.MethodGet, base+pollPath+"?"+q.Encode(), nil)
		default:
			msg := "query " + strings.ToLower(r.State)
			if r.Error != nil {
				msg += ": " + firstNonEmpty(r.Error.Details.ErrorMessage, r.Error.Message)
			}
			return Result{}, &Error{Status: http.StatusOK, Message: msg}
		}
	}
	if err != nil {
		return Result{}, err
	}
	out := Result{Units: map[string]string{}}
	if r.Result == nil {
		return out, nil
	}
	out.Records = r.Result.Records
	for _, m := range r.Result.Metadata.Metrics {
		if m.Unit == "" {
			continue
		}
		if m.MetricKey != "" {
			out.Units[m.MetricKey] = m.Unit
		}
		if m.FieldName != "" {
			out.Units[m.FieldName] = m.Unit
		}
	}
	for _, n := range r.Result.Metadata.Grail.Notifications {
		out.Notes = append(out.Notes, strings.TrimSpace(n.NotificationType+": "+n.Message))
	}
	return out, nil
}

// do sends one request, retrying 429 and 503 with backoff (Retry-After when the API sends it).
func (c *Client) do(ctx context.Context, method, u string, body []byte) (queryResponse, error) {
	retries := c.Retries
	if retries == 0 {
		retries = DefaultRetries
	}
	backoff := c.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return queryResponse{}, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return queryResponse{}, err
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
		_ = res.Body.Close()
		if err != nil {
			return queryResponse{}, err
		}
		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusServiceUnavailable) && attempt < retries {
			wait := backoff << attempt
			if s, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && s >= 0 {
				wait = time.Duration(s) * time.Second
			}
			if err := c.sleep(ctx, min(wait, time.Minute)); err != nil {
				return queryResponse{}, err
			}
			continue
		}
		var r queryResponse
		jerr := json.Unmarshal(b, &r)
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
			msg := truncate(string(b))
			if jerr == nil && r.Error != nil {
				msg = firstNonEmpty(r.Error.Details.ErrorMessage, r.Error.Message, msg)
			}
			return queryResponse{}, &Error{Status: res.StatusCode, Message: msg}
		}
		if jerr != nil {
			return queryResponse{}, &Error{Status: res.StatusCode, Message: "not a query API response: " + truncate(string(b))}
		}
		return r, nil
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncate(s string) string {
	const n = 300
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
