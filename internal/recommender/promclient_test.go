package recommender

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// promServer is a fake Prometheus HTTP API that records the form of every request.
type promServer struct {
	t      *testing.T
	mu     sync.Mutex
	path   string
	forms  []url.Values
	handle func(n int, form url.Values) (status int, body string)
}

func (p *promServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		p.t.Errorf("method = %s, want POST", r.Method)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		p.t.Errorf("Content-Type = %q", ct)
	}
	if r.URL.Path != p.path {
		p.t.Errorf("path = %s, want %s", r.URL.Path, p.path)
	}
	if err := r.ParseForm(); err != nil {
		p.t.Errorf("parse form: %v", err)
	}
	p.mu.Lock()
	n := len(p.forms)
	p.forms = append(p.forms, r.PostForm)
	p.mu.Unlock()
	status, body := p.handle(n, r.PostForm)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func newPromServer(t *testing.T, path string, handle func(n int, form url.Values) (int, string)) (*promServer, *PromClient) {
	t.Helper()
	ps := &promServer{t: t, path: path, handle: handle}
	srv := httptest.NewServer(ps)
	t.Cleanup(srv.Close)
	c := NewPromClient(srv.URL+"/", 5*time.Second)
	return ps, c
}

func parseFormTime(t *testing.T, s string) time.Time {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return time.UnixMilli(int64(math.Round(f * 1000))).UTC()
}

func TestPromClientInstant(t *testing.T) {
	at := time.Unix(1700000000, 123_000_000)
	cases := []struct {
		name    string
		status  int
		body    string
		want    []VectorSample
		wantErr string
	}{
		{
			name:   "vector with NaN and Inf skipped",
			status: 200,
			body: `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"__name__":"up","job":"a"},"value":[1700000000.5,"1.25"]},
				{"metric":{"job":"nan"},"value":[1700000000,"NaN"]},
				{"metric":{"job":"inf"},"value":[1700000000,"+Inf"]},
				{"metric":{"job":"ninf"},"value":[1700000000,"-Inf"]},
				{"metric":{},"value":[1700000001,"3"]}]}}`,
			want: []VectorSample{
				{Labels: Labels{"__name__": "up", "job": "a"}, Sample: Sample{T: time.Unix(1700000000, 500_000_000).UTC(), V: 1.25}},
				{Labels: Labels{}, Sample: Sample{T: time.Unix(1700000001, 0).UTC(), V: 3}},
			},
		},
		{
			name:   "empty vector",
			status: 200,
			body:   `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			want:   []VectorSample{},
		},
		{
			name:    "prometheus error",
			status:  400,
			body:    `{"status":"error","errorType":"bad_data","error":"parse error at char 3"}`,
			wantErr: "bad_data: parse error at char 3",
		},
		{
			name:    "non-JSON body",
			status:  502,
			body:    "<html>Bad Gateway</html>",
			wantErr: "HTTP 502",
		},
		{
			name:    "wrong result type",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
			wantErr: "returned matrix, want vector",
		},
		{
			name:    "scalar result type",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"scalar","result":[1,"2"]}}`,
			wantErr: "returned scalar, want vector",
		},
		{
			name:    "non-numeric value",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"abc"]}]}}`,
			wantErr: "invalid syntax",
		},
		{
			name:    "non-numeric timestamp",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":["x","1"]}]}}`,
			wantErr: "invalid sample timestamp",
		},
		{
			name:    "malformed result",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"vector","result":{"a":1}}}`,
			wantErr: "cannot unmarshal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, c := newPromServer(t, "/api/v1/query", func(int, url.Values) (int, string) { return tc.status, tc.body })
			got, err := c.Instant(context.Background(), `up{job="a"}`, at)
			if len(ps.forms) != 1 {
				t.Fatalf("requests = %d, want 1", len(ps.forms))
			}
			form := ps.forms[0]
			if q := form.Get("query"); q != `up{job="a"}` {
				t.Errorf("query = %q", q)
			}
			if ts := form.Get("time"); ts != "1700000000.123" {
				t.Errorf("time = %q, want 1700000000.123", ts)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestPromClientTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	c := NewPromClient(u, time.Second)
	if _, err := c.Instant(context.Background(), "up", time.Now()); err == nil {
		t.Error("Instant: want error from closed server")
	}
	if _, err := c.Range(context.Background(), "up", time.Now().Add(-time.Hour), time.Now(), time.Minute); err == nil {
		t.Error("Range: want error from closed server")
	}
}

func TestPromClientRangeResponses(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(2 * time.Minute)
	cases := []struct {
		name    string
		status  int
		body    string
		want    []Series
		wantErr string
	}{
		{
			name:   "matrix with NaN skipped",
			status: 200,
			body: `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"pod":"a"},"values":[[1700000000,"1"],[1700000060,"NaN"],[1700000120,"3"]]},
				{"metric":{"pod":"b"},"values":[[1700000060,"+Inf"],[1700000120,"0.5"]]}]}}`,
			want: []Series{
				{Labels: Labels{"pod": "a"}, Samples: []Sample{{T: start, V: 1}, {T: start.Add(2 * time.Minute), V: 3}}},
				{Labels: Labels{"pod": "b"}, Samples: []Sample{{T: start.Add(2 * time.Minute), V: 0.5}}},
			},
		},
		{
			name:   "empty matrix",
			status: 200,
			body:   `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
			want:   []Series{},
		},
		{
			name:    "prometheus error",
			status:  400,
			body:    `{"status":"error","errorType":"bad_data","error":"parse error at char 5"}`,
			wantErr: "/api/v1/query_range: bad_data: parse error at char 5",
		},
		{
			name:    "non-JSON body",
			status:  502,
			body:    "upstream connect error",
			wantErr: "HTTP 502: upstream connect error",
		},
		{
			name:    "wrong result type",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			wantErr: "returned vector, want matrix",
		},
		{
			name:    "non-numeric value",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1700000000,"x"]]}]}}`,
			wantErr: "invalid syntax",
		},
		{
			name:    "malformed result",
			status:  200,
			body:    `{"status":"success","data":{"resultType":"matrix","result":"nope"}}`,
			wantErr: "cannot unmarshal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, c := newPromServer(t, "/api/v1/query_range", func(int, url.Values) (int, string) { return tc.status, tc.body })
			got, err := c.Range(context.Background(), "rate(x[5m])", start, end, time.Minute)
			if len(ps.forms) != 1 {
				t.Fatalf("requests = %d, want 1", len(ps.forms))
			}
			f := ps.forms[0]
			if f.Get("query") != "rate(x[5m])" || f.Get("start") != "1700000000.000" || f.Get("end") != "1700000120.000" || f.Get("step") != "60" {
				t.Errorf("form = %v", f)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestPromClientRangeChunks checks that long ranges are split into contiguous, non-overlapping
// chunks of at most MaxPointsPerQuery points, and that series are merged by label set in time order.
func TestPromClientRangeChunks(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	cases := []struct {
		name       string
		maxPts     int
		step       time.Duration
		span       time.Duration
		wantChunks int
	}{
		{name: "split into five", maxPts: 5, step: time.Minute, span: 20 * time.Minute, wantChunks: 5},
		{name: "exact fit", maxPts: 5, step: time.Minute, span: 4 * time.Minute, wantChunks: 1},
		{name: "one past fit", maxPts: 5, step: time.Minute, span: 5 * time.Minute, wantChunks: 2},
		{name: "default limit", maxPts: 0, step: 5 * time.Minute, span: 24 * time.Hour, wantChunks: 1},
		{name: "two points per chunk", maxPts: 2, step: 30 * time.Second, span: 3 * time.Minute, wantChunks: 4},
		{name: "start equals end", maxPts: 5, step: time.Minute, span: 0, wantChunks: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := base, base.Add(tc.span)
			// Series "a" is present in every chunk, "b" only in even chunks, "c" only in the last.
			ps, c := newPromServer(t, "/api/v1/query_range", func(n int, f url.Values) (int, string) {
				s, e := parseFormTime(t, f.Get("start")), parseFormTime(t, f.Get("end"))
				stepSec, _ := strconv.ParseFloat(f.Get("step"), 64)
				st := time.Duration(stepSec * float64(time.Second))
				var a, b []string
				for ts := s; !ts.After(e); ts = ts.Add(st) {
					a = append(a, fmt.Sprintf(`[%d,"%d"]`, ts.Unix(), ts.Unix()))
					b = append(b, fmt.Sprintf(`[%d,"1"]`, ts.Unix()))
				}
				res := []string{fmt.Sprintf(`{"metric":{"s":"a","x":"1"},"values":[%s]}`, strings.Join(a, ","))}
				if n%2 == 0 {
					res = append(res, fmt.Sprintf(`{"metric":{"x":"1","s":"b"},"values":[%s]}`, strings.Join(b, ",")))
				}
				if e.Equal(end) {
					res = append(res, fmt.Sprintf(`{"metric":{"s":"c"},"values":[[%d,"7"]]}`, e.Unix()))
				}
				return 200, `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Join(res, ",") + `]}}`
			})
			c.MaxPointsPerQuery = tc.maxPts
			got, err := c.Range(context.Background(), "q", start, end, tc.step)
			if err != nil {
				t.Fatal(err)
			}

			// Chunks: contiguous on the step grid, non-overlapping, covering [start, end].
			if len(ps.forms) != tc.wantChunks {
				t.Fatalf("requests = %d, want %d", len(ps.forms), tc.wantChunks)
			}
			maxPts := tc.maxPts
			if maxPts <= 0 {
				maxPts = 10000
			}
			var prevEnd time.Time
			for i, f := range ps.forms {
				s, e := parseFormTime(t, f.Get("start")), parseFormTime(t, f.Get("end"))
				if f.Get("query") != "q" {
					t.Errorf("chunk %d: query = %q", i, f.Get("query"))
				}
				if e.Before(s) {
					t.Errorf("chunk %d: end %s before start %s", i, e, s)
				}
				if pts := int(e.Sub(s)/tc.step) + 1; pts > maxPts {
					t.Errorf("chunk %d: %d points > %d", i, pts, maxPts)
				}
				if i == 0 {
					if !s.Equal(start) {
						t.Errorf("first chunk start = %s, want %s", s, start)
					}
				} else if !s.Equal(prevEnd.Add(tc.step)) {
					t.Errorf("chunk %d start = %s, want previous end + step = %s", i, s, prevEnd.Add(tc.step))
				}
				prevEnd = e
			}
			if !prevEnd.Equal(end) {
				t.Errorf("last chunk end = %s, want %s", prevEnd, end)
			}

			// Merge: one series per label set, in order of first appearance, samples in time order.
			wantPoints := int(tc.span/tc.step) + 1
			wantLabels := []Labels{{"s": "a", "x": "1"}, {"s": "b", "x": "1"}, {"s": "c"}}
			if len(got) != len(wantLabels) {
				t.Fatalf("series = %d, want %d: %+v", len(got), len(wantLabels), got)
			}
			for i, l := range wantLabels {
				if !reflect.DeepEqual(got[i].Labels, l) {
					t.Errorf("series %d labels = %v, want %v", i, got[i].Labels, l)
				}
				for j := 1; j < len(got[i].Samples); j++ {
					if !got[i].Samples[j].T.After(got[i].Samples[j-1].T) {
						t.Errorf("series %v not in time order at %d", l, j)
					}
				}
			}
			a := got[0].Samples
			if len(a) != wantPoints {
				t.Errorf("series a: %d samples, want %d", len(a), wantPoints)
			}
			for j, s := range a {
				wt := start.Add(time.Duration(j) * tc.step)
				if !s.T.Equal(wt) || s.V != float64(wt.Unix()) {
					t.Errorf("series a sample %d = %+v, want T=%s V=%d", j, s, wt, wt.Unix())
				}
			}
			if len(got[2].Samples) != 1 || !got[2].Samples[0].T.Equal(end) {
				t.Errorf("series c = %+v, want one sample at end", got[2].Samples)
			}
		})
	}
}

func TestPromClientRangeChunkError(t *testing.T) {
	// The second chunk fails: the whole range fails.
	ps, c := newPromServer(t, "/api/v1/query_range", func(n int, _ url.Values) (int, string) {
		if n == 1 {
			return 503, `{"status":"error","errorType":"unavailable","error":"overloaded"}`
		}
		return 200, `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	})
	c.MaxPointsPerQuery = 3
	start := time.Unix(1700000000, 0)
	_, err := c.Range(context.Background(), "q", start, start.Add(10*time.Minute), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "unavailable: overloaded") {
		t.Fatalf("err = %v", err)
	}
	if len(ps.forms) != 2 {
		t.Errorf("requests = %d, want 2 (stop at the failing chunk)", len(ps.forms))
	}
}

// VictoriaMetrics answers 422 "not enough memory" for big range queries; the client retries the
// chunk with half the steps until it fits, and the merged result still covers the whole range.
func TestPromClientRangeShrinksChunksOnMemoryLimit(t *testing.T) {
	const step = 60
	var spans []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		start, _ := strconv.ParseFloat(r.Form.Get("start"), 64)
		end, _ := strconv.ParseFloat(r.Form.Get("end"), 64)
		points := (end-start)/step + 1
		if points > 100 {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"422","error":"not enough memory for processing max(x)"}`))
			return
		}
		spans = append(spans, points)
		var vals []string
		for ts := start; ts <= end; ts += step {
			vals = append(vals, fmt.Sprintf(`[%v,"1"]`, ts))
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"a":"b"},"values":[%s]}]}}`, strings.Join(vals, ","))
	}))
	defer srv.Close()
	c := NewPromClient(srv.URL, 5*time.Second)
	c.MaxPointsPerQuery = 400
	start := time.Unix(1_700_000_000, 0)
	end := start.Add(999 * step * time.Second)
	res, err := c.Range(context.Background(), "x", start, end, step*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Samples) != 1000 {
		t.Fatalf("got %d series / %d samples, want 1 / 1000", len(res), len(res[0].Samples))
	}
	for _, n := range spans {
		if n > 100 {
			t.Errorf("chunk of %v points was accepted", n)
		}
	}
}
