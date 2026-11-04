package hub

import (
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// facetOracle computes facet counts from GET /v1/workloads rows: each facet uses the query
// without its own filter.
func facetOracle(t *testing.T, e *testEnv, q url.Values) hubapi.WorkloadFacets {
	t.Helper()
	rows := func(drop string) []hubapi.WorkloadRow {
		qq := url.Values{}
		for k, v := range q {
			if k != drop {
				qq[k] = v
			}
		}
		qq.Set("limit", "500")
		var l hubapi.WorkloadList
		e.getJSON("/v1/workloads?"+qq.Encode(), tokUI, &l)
		return l.Items
	}
	count := func(drop string, values func(hubapi.WorkloadRow) []string) []hubapi.FacetCount {
		m := map[string]int{}
		for _, r := range rows(drop) {
			seen := map[string]bool{}
			for _, v := range values(r) {
				if !seen[v] {
					seen[v] = true
					m[v]++
				}
			}
		}
		out := []hubapi.FacetCount{}
		for v, n := range m {
			out = append(out, hubapi.FacetCount{Value: v, Count: n})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
		return out
	}
	var all hubapi.WorkloadList
	qq := url.Values{}
	for k, v := range q {
		qq[k] = v
	}
	e.getJSON("/v1/workloads?"+qq.Encode(), tokUI, &all)
	return hubapi.WorkloadFacets{
		Total:      all.Total,
		Clusters:   count("cluster", func(r hubapi.WorkloadRow) []string { return []string{r.Cluster} }),
		Namespaces: count("namespace", func(r hubapi.WorkloadRow) []string { return []string{r.Namespace} }),
		Kinds:      count("kind", func(r hubapi.WorkloadRow) []string { return []string{r.Kind} }),
		Modes:      count("mode", func(r hubapi.WorkloadRow) []string { return []string{r.Modes.CPU, r.Modes.Memory} }),
		RiskFlags:  count("riskFlag", func(r hubapi.WorkloadRow) []string { return r.RiskFlags }),
	}
}

func TestWorkloadFacets(t *testing.T) {
	e := newEnv(t)
	// One workload in auto-patch for CPU, so the modes facet has two values.
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{{
		Workload: kFrontend.Ref(), Phase: "Applied",
		Modes: hubapi.EffectiveModes{CPU: hubapi.ModeAutoPatch, Memory: hubapi.ModeAudit, CPUSource: "annotation", MemorySource: "global-default"},
	}}}, &res)

	cases := []url.Values{
		{},
		{"cluster": {"c1"}},
		{"cluster": {"c1", "c2"}, "kind": {"Deployment"}},
		{"namespace": {"payments"}},
		{"mode": {"auto-patch"}},
		{"riskFlag": {"low-confidence", "throttled"}},
		{"q": {"PAY"}},
		{"minSavings": {"0.01"}},
		{"cluster": {"c2"}, "mode": {"off"}}, // matches nothing
	}
	for _, q := range cases {
		t.Run(q.Encode(), func(t *testing.T) {
			var got hubapi.WorkloadFacets
			e.getJSON("/v1/workloads/facets?"+q.Encode(), tokUI, &got)
			want := facetOracle(t, e, q)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("facets\n got %+v\nwant %+v", got, want)
			}
		})
	}

	// Unfiltered: every live workload, both modes present, arrays never null.
	var f hubapi.WorkloadFacets
	r := e.getJSON("/v1/workloads/facets", tokUI, &f)
	if f.Total == 0 || !slices.ContainsFunc(f.Modes, func(c hubapi.FacetCount) bool { return c.Value == "auto-patch" && c.Count == 1 }) {
		t.Errorf("facets: %+v", f)
	}
	for _, field := range []string{`"clusters":[`, `"namespaces":[`, `"kinds":[`, `"modes":[`, `"riskFlags":[`} {
		if !strings.Contains(string(r.body), field) {
			t.Errorf("response lacks %s: %s", field, r.body)
		}
	}
	// A cluster filter does not narrow the cluster facet (multi-select), but narrows the others.
	var c1 hubapi.WorkloadFacets
	e.getJSON("/v1/workloads/facets?cluster=c1", tokUI, &c1)
	if !reflect.DeepEqual(c1.Clusters, f.Clusters) || c1.Total >= f.Total {
		t.Errorf("cluster=c1: clusters %+v total %d (unfiltered %+v %d)", c1.Clusters, c1.Total, f.Clusters, f.Total)
	}

	// Validation and auth like /v1/workloads.
	for _, tc := range []struct{ query, code string }{
		{"?kind=Pod", "invalid_kind"}, {"?mode=on", "invalid_mode"}, {"?riskFlag=x", "invalid_risk_flag"},
		{"?minSavings=abc", "invalid_min_savings"}, {"?cluster=Bad_Name", "invalid_cluster"},
	} {
		e.problem(e.do("GET", "/v1/workloads/facets"+tc.query, tokUI, nil), 400, tc.code)
	}
	e.problem(e.do("GET", "/v1/workloads/facets", tokC1, nil), 403, "role_forbidden")
	e.problem(e.do("GET", "/v1/workloads/facets", "", nil), 401, "unauthorized")
}

// TestPricingAndConfidenceFactors checks the D-016 fields: pricing on every recommendation shape,
// confidence factors only in the detail explanation, and that the factors add up.
func TestPricingAndConfidenceFactors(t *testing.T) {
	e := newEnv(t)
	var list hubapi.RecommendationList
	e.getJSON("/v1/clusters/c1/recommendations", tokC1, &list)
	if len(list.Items) == 0 {
		t.Fatal("no recommendations")
	}
	for _, it := range list.Items {
		p := it.Pricing
		if p == nil || (p.Source != hubapi.PricingSourceMetrics && p.Source != hubapi.PricingSourceDefault) || p.CPUCoreHourUSD <= 0 || p.MemoryGiBHourUSD <= 0 {
			t.Errorf("%s: pricing %+v", it.Workload.Name, p)
		}
		for _, c := range it.Containers {
			if c.Explanation != nil {
				t.Errorf("%s: explanation in the agent list", it.Workload.Name)
			}
		}
	}

	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Recommendation == nil || d.Recommendation.Pricing == nil {
		t.Fatalf("detail without pricing: %+v", d.Recommendation)
	}
	minConf := 1.0
	for _, c := range d.Recommendation.Containers {
		f := c.Explanation.ConfidenceFactors
		if f == nil {
			t.Fatalf("%s: no confidenceFactors", c.Name)
		}
		sumW := f.Coverage.Weight + f.Samples.Weight + f.Variance.Weight
		sum := f.Coverage.Score*f.Coverage.Weight + f.Samples.Score*f.Samples.Weight + f.Variance.Score*f.Variance.Weight
		for _, x := range []float64{f.Coverage.Score, f.Samples.Score, f.Variance.Score, f.Confidence} {
			if x < 0 || x > 1 {
				t.Errorf("%s: factor out of range %+v", c.Name, f)
			}
		}
		if !approx(sumW, 1, 1e-9) || !approx(sum, f.Confidence, 0.001) {
			t.Errorf("%s: weights %v, sum %v != confidence %v", c.Name, sumW, sum, f.Confidence)
		}
		minConf = min(minConf, f.Confidence)
	}
	if !approx(d.Recommendation.Confidence, minConf, 0.006) {
		t.Errorf("workload confidence %v, min container confidence %v", d.Recommendation.Confidence, minConf)
	}

	// Rows before D-016 (pricing '{}' in the database) have no pricing field.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE recommendations SET pricing = '{}'`); err != nil {
		t.Fatal(err)
	}
	var old hubapi.WorkloadDetail
	r := e.getJSON(wlPath(kCheckout, ""), tokUI, &old)
	if old.Recommendation.Pricing != nil || strings.Contains(string(r.body), `"pricing"`) {
		t.Errorf("old row has pricing: %s", r.body)
	}
}
