package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/pricing"
	"github.com/kerf-dev/kerf/internal/recommender"
	"github.com/kerf-dev/kerf/tools/synth/synth"
)

// fakeProm serves the Prometheus HTTP API from a fixtureQuerier: POST /api/v1/query_range and
// /api/v1/query (the recommender), GET /api/v1/query (node discovery for prices). check runs on
// every request first; it may reject with a status code.
type fakeProm struct {
	*httptest.Server
	fq *fixtureQuerier
	// nodes answers the node-discovery queries; nil = no nodes.
	nodes func(query string) []map[string]any

	mu       sync.Mutex
	requests []seenRequest
}

type seenRequest struct {
	Method, Path string
	Header       http.Header
	Query        string
}

func newFakeProm(t *testing.T, fq *fixtureQuerier, check func(r *http.Request, body []byte) int, tls bool) *fakeProm {
	t.Helper()
	p := &fakeProm{fq: fq}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if check != nil {
			if code := check(r, body); code != 0 {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"status":"error","errorType":"forbidden","error":"rejected"}`))
				return
			}
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		query := r.Form.Get("query")
		p.mu.Lock()
		p.requests = append(p.requests, seenRequest{r.Method, r.URL.Path, r.Header.Clone(), query})
		p.mu.Unlock()
		var res any
		var typ string
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/query_range"):
			start, _ := strconv.ParseFloat(r.Form.Get("start"), 64)
			end, _ := strconv.ParseFloat(r.Form.Get("end"), 64)
			step, _ := strconv.ParseFloat(r.Form.Get("step"), 64)
			series, _ := fq.Range(r.Context(), query, time.UnixMilli(int64(start*1000)).UTC(), time.UnixMilli(int64(end*1000)).UTC(), time.Duration(step*float64(time.Second)))
			var out []map[string]any
			for _, s := range series {
				vals := make([][2]any, 0, len(s.Samples))
				for _, x := range s.Samples {
					vals = append(vals, [2]any{float64(x.T.Unix()), strconv.FormatFloat(x.V, 'g', -1, 64)})
				}
				out = append(out, map[string]any{"metric": s.Labels, "values": vals})
			}
			res, typ = out, "matrix"
		case strings.HasSuffix(r.URL.Path, "/api/v1/query"):
			typ = "vector"
			if strings.Contains(query, "kube_node_labels") || strings.Contains(query, "kube_node_status_allocatable") {
				if p.nodes != nil {
					res = p.nodes(query)
				}
				break
			}
			vec, _ := fq.Instant(r.Context(), query, time.Now())
			var out []map[string]any
			for _, v := range vec {
				out = append(out, map[string]any{"metric": v.Labels, "value": [2]any{float64(v.Sample.T.Unix()), strconv.FormatFloat(v.Sample.V, 'g', -1, 64)}})
			}
			res = out
		default:
			http.NotFound(w, r)
			return
		}
		if res == nil {
			res = []any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": typ, "result": res}})
	})
	p.Server = httptest.NewUnstartedServer(h)
	p.Config.ErrorLog = log.New(io.Discard, "", 0) // expected TLS handshake failures
	if tls {
		p.StartTLS()
	} else {
		p.Start()
	}
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProm) all() []seenRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]seenRequest(nil), p.requests...)
}

// fixtureSetup serves the first n fixtures (0 = all); fewer series make the auth tests quick.
func fixtureSetup(t *testing.T, profile string, n int) (*fixtureQuerier, []*synth.Fixture) {
	t.Helper()
	fxs := loadFixtures(t)
	if n > 0 {
		fxs = fxs[:n]
	}
	cfg := recommender.DefaultConfig()
	q := recommender.Queries{Cluster: fxs[0].Cluster, RateWindow: cfg.RateWindow}
	return newFixtureQuerier(t, q, cfg, cfg.Profiles[profile].Window, fxs), fxs
}

func baseOptions(srv *fakeProm, fxs []*synth.Fixture) Options {
	return Options{
		MetricsURL: srv.URL, ClusterLabel: "cluster", ClusterValue: fxs[0].Cluster, Profile: hubapi.ProfileProduction,
		Now: fixtureEnd(fxs), Timeout: 30 * time.Second,
	}
}

// TestRunOverHTTP runs the report through the real HTTP client: it must equal the in-process run.
func TestRunOverHTTP(t *testing.T) {
	fq, fxs := fixtureSetup(t, hubapi.ProfileProduction, 0)
	srv := newFakeProm(t, fq, nil, false)
	got, err := Run(context.Background(), baseOptions(srv, fxs), Deps{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := reportFor(t, fxs, hubapi.ProfileProduction)
	if len(got.Rows) != len(want.Rows) || got.Totals != want.Totals {
		t.Fatalf("over HTTP: %d workloads %+v, in process: %d workloads %+v", len(got.Rows), got.Totals, len(want.Rows), want.Totals)
	}
	for i := range got.Rows {
		if got.Rows[i].MonthlySavingsUSD != want.Rows[i].MonthlySavingsUSD || got.Rows[i].Workload != want.Rows[i].Workload {
			t.Errorf("row %d differs: %+v vs %+v", i, got.Rows[i], want.Rows[i])
		}
	}
	// A long range is split in chunks (Prometheus's 11000-point limit): 14 days at 5 minutes is two requests per range query.
	var ranges int
	for _, r := range srv.all() {
		if strings.HasSuffix(r.Path, "/query_range") {
			ranges++
		}
	}
	if ranges == 0 {
		t.Error("no range queries were sent")
	}
	// The report only reads: every request is a query.
	for _, r := range srv.all() {
		if !strings.Contains(r.Path, "/api/v1/query") {
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
		}
	}
}

func TestAuthModes(t *testing.T) {
	fq, fxs := fixtureSetup(t, hubapi.ProfileProduction, 2)
	type tc struct {
		name   string
		mutate func(o *Options)
		check  func(r *http.Request) bool // true = accepted
		tls    bool
		fails  bool
	}
	cases := []tc{
		{name: "no auth", check: func(r *http.Request) bool { return r.Header.Get("Authorization") == "" }},
		{name: "bearer token", mutate: func(o *Options) { o.Auth.Token = "s3cr3t" },
			check: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer s3cr3t" }},
		{name: "basic auth", mutate: func(o *Options) { o.Auth.Username, o.Auth.Password = "kerf", "pw" },
			check: func(r *http.Request) bool { u, p, ok := r.BasicAuth(); return ok && u == "kerf" && p == "pw" }},
		{name: "tenant header (Mimir, Cortex)", mutate: func(o *Options) { o.Auth.Headers = map[string]string{"X-Scope-OrgID": "tenant-1"} },
			check: func(r *http.Request) bool { return r.Header.Get("X-Scope-OrgID") == "tenant-1" }},
		{name: "wrong token is rejected", mutate: func(o *Options) { o.Auth.Token = "wrong" }, fails: true,
			check: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer s3cr3t" }},
		{name: "TLS with the CA", tls: true, check: func(*http.Request) bool { return true }, mutate: func(o *Options) {}},
		{name: "TLS without the CA fails", tls: true, fails: true, check: func(*http.Request) bool { return true }, mutate: func(o *Options) {}},
		{name: "TLS skip verify", tls: true, check: func(*http.Request) bool { return true }, mutate: func(o *Options) { o.Auth.InsecureSkipVerify = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newFakeProm(t, fq, func(r *http.Request, _ []byte) int {
				if !c.check(r) {
					return http.StatusForbidden
				}
				return 0
			}, c.tls)
			o := baseOptions(srv, fxs)
			if c.mutate != nil {
				c.mutate(&o)
			}
			if c.tls && c.name == "TLS with the CA" {
				o.Auth.CACertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
			}
			rep, err := Run(context.Background(), o, Deps{})
			if c.fails {
				if err == nil {
					t.Fatal("expected an error")
				}
				if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "wrong") {
					t.Errorf("the error leaks a secret: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Rows) == 0 {
				t.Error("no rows")
			}
		})
	}
}

var testCreds = aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "tok"}

func staticCreds(context.Context, hubapi.DatasourceSigV4) (aws.CredentialsProvider, error) {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return testCreds, nil }), nil
}

// verifySigV4 recomputes the signature of a request the way Amazon Managed Service for Prometheus
// does and reports why it does not match ("" = valid).
func verifySigV4(r *http.Request, body []byte, region, service string) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return "not an AWS4-HMAC-SHA256 Authorization header: " + auth
	}
	wantScope := "/" + region + "/" + service + "/aws4_request"
	if !strings.Contains(auth, "Credential="+testCreds.AccessKeyID+"/") || !strings.Contains(auth, wantScope) {
		return "credential scope is not " + wantScope + ": " + auth
	}
	when, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "missing or bad X-Amz-Date"
	}
	if d := time.Since(when); d > time.Minute || d < -time.Minute {
		return "request time is too far from now: " + d.String()
	}
	if r.Header.Get("X-Amz-Security-Token") != testCreds.SessionToken {
		return "session token missing"
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = "http", r.Host
	clone.RequestURI = ""
	clone.Header.Del("Authorization")
	clone.Header.Del("X-Amz-Date")
	clone.Header.Del("X-Amz-Security-Token")
	// Only the headers named in SignedHeaders are part of the signature (the HTTP transport adds
	// Accept-Encoding after signing).
	_, signedList, _ := strings.Cut(auth, "SignedHeaders=")
	signedList, _, _ = strings.Cut(signedList, ",")
	signedSet := map[string]bool{}
	for _, h := range strings.Split(signedList, ";") {
		signedSet[h] = true
	}
	for name := range clone.Header {
		if !signedSet[strings.ToLower(name)] {
			clone.Header.Del(name)
		}
	}
	clone.Body = io.NopCloser(strings.NewReader(string(body)))
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(r.Context(), testCreds, clone, hex.EncodeToString(sum[:]), service, region, when); err != nil {
		return err.Error()
	}
	if got := clone.Header.Get("Authorization"); got != auth {
		return fmt.Sprintf("signature mismatch:\n got  %s\n want %s", auth, got)
	}
	return ""
}

// TestSigV4Signing checks, against a fake server that verifies the signature as AMP does, that every
// query (POST range and instant queries) is signed with the region, the service and the
// session token. It is not a test against a real Amazon Managed Service for Prometheus.
func TestSigV4Signing(t *testing.T) {
	fq, fxs := fixtureSetup(t, hubapi.ProfileProduction, 2)
	var mu sync.Mutex
	var bad []string
	signed := 0
	srv := newFakeProm(t, fq, func(r *http.Request, body []byte) int {
		mu.Lock()
		defer mu.Unlock()
		if msg := verifySigV4(r, body, "eu-west-1", "aps"); msg != "" {
			bad = append(bad, r.Method+" "+r.URL.Path+": "+msg)
			return http.StatusForbidden
		}
		signed++
		return 0
	}, false)
	o := baseOptions(srv, fxs)
	o.Auth.SigV4 = &hubapi.DatasourceSigV4{Region: "eu-west-1"}
	rep, err := Run(context.Background(), o, Deps{Credentials: staticCreds})
	if err != nil {
		t.Fatalf("%v\nrejected: %v", err, bad)
	}
	if len(bad) > 0 {
		t.Fatalf("invalid signatures: %v", bad)
	}
	if signed < 20 || len(rep.Rows) == 0 {
		t.Errorf("%d signed requests, %d rows", signed, len(rep.Rows))
	}

	t.Run("wrong region is rejected", func(t *testing.T) {
		o := baseOptions(srv, fxs)
		o.Auth.SigV4 = &hubapi.DatasourceSigV4{Region: "us-east-1"}
		if _, err := Run(context.Background(), o, Deps{Credentials: staticCreds}); err == nil {
			t.Fatal("expected the fake AMP to reject a signature for another region")
		}
	})
	t.Run("no credentials", func(t *testing.T) {
		o := baseOptions(srv, fxs)
		o.Auth.SigV4 = &hubapi.DatasourceSigV4{Region: "eu-west-1"}
		_, err := Run(context.Background(), o, Deps{Credentials: func(context.Context, hubapi.DatasourceSigV4) (aws.CredentialsProvider, error) {
			return nil, fmt.Errorf("no credentials in the environment")
		}})
		if err == nil || !strings.Contains(err.Error(), "no credentials") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAMPRegionIsInferred(t *testing.T) {
	o := Options{MetricsURL: "https://aps-workspaces.eu-central-1.amazonaws.com/workspaces/ws-1234", Profile: "production"}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.Auth.SigV4 == nil || o.Auth.SigV4.Region != "eu-central-1" {
		t.Fatalf("sigv4 = %+v, want eu-central-1", o.Auth.SigV4)
	}
	if c := o.datasourceConfig(); c.AuthType != hubapi.AuthSigV4 {
		t.Errorf("auth type = %s", c.AuthType)
	}
	other := Options{MetricsURL: "https://prometheus.example.com", Profile: "production", Auth: Auth{SigV4: &hubapi.DatasourceSigV4{}}}
	if err := other.Validate(); err == nil || !strings.Contains(err.Error(), "sigv4-region") {
		t.Errorf("SigV4 without a region on a non-AMP URL: err = %v", err)
	}
}

// TestMatcherInQueries: the cluster label and the extra matchers reach every query, and a cluster
// label other than `cluster` is relabelled like the hub's metrics bindings.
func TestMatcherInQueries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		label, val  string
		extra       []string
		want, wantN []string
	}{
		{"cluster label", "cluster", "prod-eu-1", nil, []string{`cluster="prod-eu-1"`}, []string{"label_replace(container_cpu"}},
		{"another label is relabelled", "k8s_cluster", "prod-eu-1", []string{`env=~"prod|stage"`}, []string{`k8s_cluster="prod-eu-1"`, `env=~"prod|stage"`, `label_replace(`}, nil},
		{"no cluster label", "", "", nil, []string{`container_cpu_usage_seconds_total{container!=""`}, []string{`cluster="`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fq := &fixtureQuerier{}
			o := Options{MetricsURL: "http://x.invalid", ClusterLabel: tc.label, ClusterValue: tc.val, ExtraMatchers: tc.extra, Now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
			rep, err := Run(context.Background(), o, Deps{Querier: fq})
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Rows) != 0 {
				t.Errorf("rows from an empty source: %d", len(rep.Rows))
			}
			var cpu string
			for _, q := range fq.seen {
				if strings.Contains(q, "container_cpu_usage_seconds_total") && strings.Contains(q, "max by") {
					cpu = q
					break
				}
			}
			if cpu == "" {
				t.Fatalf("no CPU query among %d queries", len(fq.seen))
			}
			for _, w := range tc.want {
				if !strings.Contains(cpu, w) {
					t.Errorf("the CPU query lacks %s:\n%s", w, cpu)
				}
			}
			for _, w := range tc.wantN {
				if strings.Contains(cpu, w) {
					t.Errorf("the CPU query has %s:\n%s", w, cpu)
				}
			}
			if wantName := (&o).ClusterName(); rep.Cluster != wantName {
				t.Errorf("cluster = %q, want %q", rep.Cluster, wantName)
			}
		})
	}
}

const staticPrices = `
instanceTypes:
  m6i.xlarge: {vcpu: 4, memoryGiB: 16}
regions:
  eu-west-1:
    m6i.xlarge: {onDemand: 0.214}
`

// TestStaticPrices: nodes found in the metrics source are priced from the static table and the
// rates replace the default ones, like the pricing service's series would.
func TestStaticPrices(t *testing.T) {
	fq, fxs := fixtureSetup(t, hubapi.ProfileProduction, 0)
	srv := newFakeProm(t, fq, nil, false)
	srv.nodes = func(q string) []map[string]any {
		var out []map[string]any
		for _, n := range []string{"node-a", "node-b"} {
			m := map[string]string{"cluster": fxs[0].Cluster, "node": n}
			var v string
			switch {
			case strings.Contains(q, "kube_node_labels"):
				m["label_node_kubernetes_io_instance_type"] = "m6i.xlarge"
				m["label_topology_kubernetes_io_region"] = "eu-west-1"
				m["label_topology_kubernetes_io_zone"] = "eu-west-1a"
				v = "1"
				out = append(out, map[string]any{"metric": m, "value": [2]any{0, v}})
			default:
				m["resource"] = "cpu"
				out = append(out, map[string]any{"metric": m, "value": [2]any{0, "3.92"}})
				m2 := map[string]string{"cluster": fxs[0].Cluster, "node": n, "resource": "memory"}
				out = append(out, map[string]any{"metric": m2, "value": [2]any{0, strconv.FormatFloat(14.5*(1<<30), 'f', 0, 64)}})
			}
		}
		return out
	}
	file := filepath.Join(t.TempDir(), "static.yaml")
	if err := os.WriteFile(file, []byte(staticPrices), 0o600); err != nil {
		t.Fatal(err)
	}
	o := baseOptions(srv, fxs)
	o.Prices = file
	rep, err := Run(context.Background(), o, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	cpu, mem, _ := pricing.Split(0.214, 3.92, 14.5, pricing.DefaultCPUWeight)
	if rep.Pricing.Source != PriceSourceStatic || rep.Pricing.Nodes != 2 ||
		math.Abs(rep.Pricing.CPUCoreHourUSD-cpu) > 1e-12 || math.Abs(rep.Pricing.MemoryGiBHourUSD-mem) > 1e-12 {
		t.Fatalf("pricing = %+v, want cpu %v memory %v from %s", rep.Pricing, cpu, mem, PriceSourceStatic)
	}
	for _, w := range rep.Warnings {
		t.Errorf("unexpected warning: %s", w)
	}
	// The same history with default rates saves a different amount, so the rates are really applied.
	def, _ := reportFor(t, fxs, hubapi.ProfileProduction)
	if def.Totals.MonthlySavingsUSD == rep.Totals.MonthlySavingsUSD {
		t.Errorf("savings did not change with the prices: %v", rep.Totals.MonthlySavingsUSD)
	}
	for _, row := range rep.Rows {
		if row.Workload == "synth-flat-0" {
			want := row.MonthlySavingsUSD.CPU
			if want <= 0 {
				t.Errorf("flat: no CPU saving")
			}
		}
	}

	t.Run("nodes without a price", func(t *testing.T) {
		o := baseOptions(srv, fxs)
		empty := filepath.Join(t.TempDir(), "p.yaml")
		_ = os.WriteFile(empty, []byte("instanceTypes:\n  c5.large: {vcpu: 2, memoryGiB: 4}\nregions:\n  eu-west-1:\n    c5.large: {onDemand: 0.1}\n"), 0o600)
		o.Prices = empty
		rep, err := Run(context.Background(), o, Deps{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Pricing.Source != PriceSourceDefault || len(rep.Warnings) == 0 {
			t.Errorf("pricing %+v, warnings %v; want the default rates and a warning", rep.Pricing, rep.Warnings)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		o := baseOptions(srv, fxs)
		o.Prices = filepath.Join(t.TempDir(), "none.yaml")
		if _, err := Run(context.Background(), o, Deps{}); err == nil || !strings.Contains(err.Error(), "--prices") {
			t.Errorf("err = %v", err)
		}
	})
}
