package recommender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Labels are the labels of a series.
type Labels map[string]string

// VectorSample is one element of an instant-query result.
type VectorSample struct {
	Labels Labels
	Sample Sample
}

// Series is one element of a range-query result.
type Series struct {
	Labels  Labels
	Samples []Sample
}

// Querier runs PromQL queries. Implementations: PromClient (HTTP) and fakes in tests.
type Querier interface {
	Instant(ctx context.Context, query string, t time.Time) ([]VectorSample, error)
	Range(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error)
}

// PromClient queries the Prometheus HTTP API (Prometheus and VictoriaMetrics).
type PromClient struct {
	BaseURL string
	HTTP    *http.Client
	// MaxPointsPerQuery splits range queries into chunks (Prometheus rejects > 11000 points).
	MaxPointsPerQuery int
}

// NewPromClient returns a client for baseURL (e.g. http://victoria-metrics:8428).
func NewPromClient(baseURL string, timeout time.Duration) *PromClient {
	return &PromClient{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		HTTP:              &http.Client{Timeout: timeout},
		MaxPointsPerQuery: 2016, // 7 days at 5m; smaller chunks keep VictoriaMetrics within its memory budget
	}
}

type apiResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type rawVector []struct {
	Metric Labels    `json:"metric"`
	Value  promPoint `json:"value"`
}

type rawMatrix []struct {
	Metric Labels      `json:"metric"`
	Values []promPoint `json:"values"`
}

// Instant implements Querier.
func (c *PromClient) Instant(ctx context.Context, query string, t time.Time) ([]VectorSample, error) {
	form := url.Values{"query": {query}, "time": {formatTime(t)}}
	resp, err := c.do(ctx, "/api/v1/query", form)
	if err != nil {
		return nil, err
	}
	if resp.Data.ResultType != "vector" {
		return nil, fmt.Errorf("query returned %s, want vector", resp.Data.ResultType)
	}
	var rv rawVector
	if err := json.Unmarshal(resp.Data.Result, &rv); err != nil {
		return nil, err
	}
	out := make([]VectorSample, 0, len(rv))
	for _, r := range rv {
		s := Sample(r.Value)
		if math.IsNaN(s.V) || math.IsInf(s.V, 0) {
			continue
		}
		out = append(out, VectorSample{Labels: r.Metric, Sample: s})
	}
	return out, nil
}

// Range implements Querier. Long ranges are split so each request has at most
// MaxPointsPerQuery steps; series are merged by label set.
func (c *PromClient) Range(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error) {
	maxPts := c.MaxPointsPerQuery
	if maxPts <= 0 {
		maxPts = 10000
	}
	chunkSteps := maxPts
	total := int(end.Sub(start)/step) + 1
	merged := map[string]*Series{}
	var order []string
	for s := start; !s.After(end); {
		e := s.Add(step * time.Duration(chunkSteps-1))
		if e.After(end) {
			e = end
		}
		form := url.Values{"query": {query}, "start": {formatTime(s)}, "end": {formatTime(e)}, "step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)}}
		resp, err := c.do(ctx, "/api/v1/query_range", form)
		if err != nil {
			// VictoriaMetrics refuses queries that need too much memory: retry with smaller chunks.
			if isResourceLimit(err) && chunkSteps > minChunkSteps {
				chunkSteps = max(minChunkSteps, chunkSteps/2)
				continue
			}
			return nil, err
		}
		if resp.Data.ResultType != "matrix" {
			return nil, fmt.Errorf("query_range returned %s, want matrix", resp.Data.ResultType)
		}
		var rm rawMatrix
		if err := json.Unmarshal(resp.Data.Result, &rm); err != nil {
			return nil, err
		}
		for _, r := range rm {
			k := labelsKey(r.Metric)
			ser, ok := merged[k]
			if !ok {
				ser = &Series{Labels: r.Metric, Samples: make([]Sample, 0, total)}
				merged[k] = ser
				order = append(order, k)
			}
			for _, v := range r.Values {
				if math.IsNaN(v.V) || math.IsInf(v.V, 0) {
					continue
				}
				ser.Samples = append(ser.Samples, Sample(v))
			}
		}
		s = e.Add(step)
	}
	out := make([]Series, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out, nil
}

// minChunkSteps is the smallest range-query chunk after memory-limit retries.
const minChunkSteps = 48

// isResourceLimit reports a backend refusal for resource reasons (VictoriaMetrics HTTP 422
// "not enough memory", Prometheus "too many samples"/"exceeded maximum resolution").
func isResourceLimit(err error) bool {
	m := err.Error()
	return strings.Contains(m, "not enough memory") || strings.Contains(m, "too many samples") ||
		strings.Contains(m, "exceeded maximum resolution") || strings.Contains(m, "cannot select more than")
}

func (c *PromClient) do(ctx context.Context, path string, form url.Values) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("%s: HTTP %d: %.200s", path, res.StatusCode, body)
	}
	if ar.Status != "success" {
		return nil, fmt.Errorf("%s: %s: %s", path, ar.ErrorType, ar.Error)
	}
	return &ar, nil
}

// promPoint decodes a `[<unix seconds>, "<value>"]` pair without intermediate allocations;
// range results can hold millions of them.
type promPoint Sample

// UnmarshalJSON implements json.Unmarshaler.
func (p *promPoint) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return fmt.Errorf("invalid sample %.40s", b)
	}
	tsRaw, valRaw, ok := bytes.Cut(b[1:len(b)-1], []byte(","))
	if !ok {
		return fmt.Errorf("invalid sample %.40s", b)
	}
	ts, err := strconv.ParseFloat(string(bytes.TrimSpace(tsRaw)), 64)
	if err != nil {
		return fmt.Errorf("invalid sample timestamp: %w", err)
	}
	valRaw = bytes.TrimSpace(valRaw)
	if len(valRaw) < 2 || valRaw[0] != '"' || valRaw[len(valRaw)-1] != '"' {
		return fmt.Errorf("invalid sample value %.40s", valRaw)
	}
	v, err := strconv.ParseFloat(string(valRaw[1:len(valRaw)-1]), 64)
	if err != nil {
		return fmt.Errorf("invalid sample value: %w", err)
	}
	sec, frac := math.Modf(ts)
	*p = promPoint{T: time.Unix(int64(sec), int64(math.Round(frac*1e3))*1e6).UTC(), V: v}
	return nil
}

func formatTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', 3, 64)
}

func labelsKey(l Labels) string {
	b, _ := json.Marshal(l) // map keys are sorted by encoding/json
	return string(b)
}
