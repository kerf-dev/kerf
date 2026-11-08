package readiness

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// fakeQ answers instant queries by the first rule whose substrings all occur in the query.
type fakeQ struct {
	rules   []rule
	queries []string
	err     error
}

type rule struct {
	has []string
	res []recommender.VectorSample
}

func (f *fakeQ) on(v float64, has ...string) *fakeQ {
	f.rules = append(f.rules, rule{has: has, res: []recommender.VectorSample{{Sample: recommender.Sample{V: v}}}})
	return f
}

func (f *fakeQ) onSamples(res []recommender.VectorSample, has ...string) *fakeQ {
	f.rules = append(f.rules, rule{has: has, res: res})
	return f
}

func (f *fakeQ) Instant(_ context.Context, q string, _ time.Time) ([]recommender.VectorSample, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
next:
	for _, r := range f.rules {
		for _, h := range r.has {
			if !strings.Contains(q, h) {
				continue next
			}
		}
		return r.res, nil
	}
	return nil, nil
}

func (f *fakeQ) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]recommender.Series, error) {
	return nil, errors.New("not used")
}

func nodeSample(node string, full bool) recommender.VectorSample {
	l := recommender.Labels{"node": node, "label_node_kubernetes_io_instance_type": "m6i.large", "label_topology_kubernetes_io_zone": "eu-west-1a"}
	if full {
		l["label_karpenter_sh_capacity_type"] = "on-demand"
		l["label_topology_kubernetes_io_region"] = "eu-west-1"
	}
	return recommender.VectorSample{Labels: l, Sample: recommender.Sample{V: 1}}
}

// healthy is a datasource where every check passes.
func healthy() *fakeQ {
	f := &fakeQ{}
	spans, counts := coverageSamples(regularSeries(20, 30*time.Second, 2*time.Hour))
	f.onSamples(spans, "min_over_time(").onSamples(counts, "count_over_time(container_memory_working_set_bytes")
	f.on(0, "nvidia_com_gpu"). // no GPUs in the cluster
					on(1, "vector(1)").
					on(1500, `__name__=~`).
					on(float64(now.Add(-30*time.Second).Unix()), "timestamp(container_memory_working_set_bytes").
					on(float64(now.Add(-20*time.Second).Unix()), "timestamp(kube_pod_owner").
					on(200, " and on (namespace, pod) ").
					on(200, "count(count by (namespace, pod) (last_over_time(container_memory_working_set_bytes").
					on(180, "count(count by (namespace, pod) (last_over_time(kube_pod_owner").
					on(5, "kube_pod_owner", `owner_kind="ReplicaSet"`).
					on(2, "kube_pod_owner", `owner_kind="Job"`).
					on(4, "count(last_over_time(kube_replicaset_owner").
					on(2, "count(last_over_time(kube_job_owner").
					on(400, "container_cpu_usage_seconds_total").
					on(400, "count(last_over_time(container_memory_working_set_bytes").
					on(300, "kube_pod_container_resource_requests").
					on(100, "kube_pod_container_resource_limits").
					on(50, "container_cpu_cfs_periods_total").
					on(50, "container_cpu_cfs_throttled_periods_total").
					on(400, "kube_pod_container_status_restarts_total").
					on(0, "kube_pod_container_status_last_terminated_reason").
					on(400, "container_oom_events_total").
					on(3, "kube_horizontalpodautoscaler_info").
					on(3, "kube_horizontalpodautoscaler_spec_target_metric").
					onSamples([]recommender.VectorSample{nodeSample("n1", true), nodeSample("n2", true)}, "kube_node_labels").
					on(2, "kube_node_status_allocatable")
	return f
}

func byID(rep Report) map[string]hubapi.ReadinessCheck {
	m := map[string]hubapi.ReadinessCheck{}
	for _, c := range rep.Checks {
		m[c.ID] = c
	}
	return m
}

func TestMetricsCheckerHealthy(t *testing.T) {
	q := healthy()
	local := (&fakeQ{}).on(2, "kerf_node_hourly_cost_usd")
	m, _ := datasource.NewMatcher("ro-spoke-2", &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod", ExtraMatchers: []string{`env="prod"`}})
	c := &MetricsChecker{Q: q, M: m, Local: local, HistoryWindow: 7 * 24 * time.Hour, DocsBase: "https://docs/", Now: func() time.Time { return now }}
	rep := c.Run(context.Background())
	if len(rep.Checks) != len(MetricsCheckIDs()) {
		t.Fatalf("%d checks, want %d", len(rep.Checks), len(MetricsCheckIDs()))
	}
	for _, ck := range rep.Checks {
		if ck.Status != string(rd.StatusPass) {
			t.Errorf("%s = %s: %s", ck.ID, ck.Status, ck.Message)
		}
		if ck.Remediation != "" {
			t.Errorf("%s: remediation on a pass", ck.ID)
		}
		cat, _ := rd.Lookup(ck.ID)
		if ck.Severity != string(cat.Severity) || ck.Category != string(cat.Category) || ck.Source != "hub" || ck.DocsURL != "https://docs/readiness#"+ck.ID || !ck.CheckedAt.Equal(now) {
			t.Errorf("%s: catalog fields %+v", ck.ID, ck)
		}
	}
	if !rep.OK() || !rep.Reachable || rep.Found.ContainerSeries != 400 || rep.Found.Pods != 180 || rep.Found.Nodes != 2 {
		t.Errorf("report %+v", rep)
	}
	// every datasource query selects the binding, with one selector per metric
	for _, qs := range q.queries {
		if strings.Contains(qs, "}{") {
			t.Errorf("two selectors in a row: %s", qs)
		}
		if qs != "vector(1)" && !strings.Contains(qs, `k8s_cluster="prod", env="prod"`) {
			t.Errorf("query without the binding: %s", qs)
		}
	}
	if !strings.Contains(local.queries[0], `kerf_node_hourly_cost_usd{cluster="ro-spoke-2"}`) {
		t.Errorf("pricing query %s", local.queries[0])
	}
}

func TestMetricsCheckerKSMDown(t *testing.T) {
	// kube-state-metrics scaled to 0: its series are stale (last sample 20 min ago) and the
	// instant counts over the last hour still exist; freshness fails.
	q := healthy()
	q.rules = append([]rule{{has: []string{"timestamp(kube_pod_owner"}, res: []recommender.VectorSample{{Sample: recommender.Sample{V: float64(now.Add(-20 * time.Minute).Unix())}}}}}, q.rules...)
	rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("ro-spoke-2"), Now: func() time.Time { return now }}).Run(context.Background())
	got := byID(rep)
	if f := got[rd.CheckFreshness]; f.Status != "fail" || !strings.Contains(f.Message, "20m0s old") || f.Remediation == "" || f.Details["kube_pod_owner"] != "20m0s" {
		t.Errorf("freshness %+v", f)
	}
	if rep.OK() {
		t.Error("report OK with stale KSM")
	}
	if p := got[rd.CheckPricingCoverage]; p.Status != "unknown" {
		t.Errorf("pricing without local: %+v", p)
	}
}

func TestMetricsCheckerKSMMissing(t *testing.T) {
	q := &fakeQ{}
	spans, counts := coverageSamples(regularSeries(3, 30*time.Second, time.Hour))
	q.onSamples(spans, "min_over_time(").onSamples(counts, "count_over_time(container_memory_working_set_bytes")
	q.on(1, "vector(1)").on(10, `__name__=~`).
		on(float64(now.Unix()), "timestamp(container_memory_working_set_bytes").
		on(400, "container_cpu_usage_seconds_total").on(400, "count(last_over_time(container_memory_working_set_bytes")
	rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
	got := byID(rep)
	for id, want := range map[string]string{
		rd.CheckCAdvisor: "pass", rd.CheckKSMOwners: "fail", rd.CheckKSMResources: "fail", rd.CheckFreshness: "fail",
		rd.CheckNodeLabels: "warn", rd.CheckNodeAllocatable: "warn", rd.CheckHPASeries: "unknown", rd.CheckRestartsOOM: "warn",
		rd.CheckThrottling: "warn", rd.CheckOwnerJoin: "unknown", rd.CheckHistory: "pass",
	} {
		if got[id].Status != want {
			t.Errorf("%s = %s (%s), want %s", id, got[id].Status, got[id].Message, want)
		}
		if want != "pass" && want != "unknown" && got[id].Remediation == "" {
			t.Errorf("%s: no remediation", id)
		}
	}
	if f := got[rd.CheckFreshness]; f.Message != "no samples of kube_pod_owner found in the last 1h" || f.Details["kube_pod_owner"] != Absent ||
		f.Details["lastSampleAge"] != "0s" || !strings.Contains(f.Remediation, "kube-state-metrics") {
		t.Errorf("freshness with kube_pod_owner absent: %+v", f)
	}
	for _, c := range rep.Checks {
		if (c.Status == "warn" || c.Status == "fail") && !strings.HasSuffix(c.Remediation, RecheckHint("c")) {
			t.Errorf("%s: remediation does not end with the re-check hint: %q", c.ID, c.Remediation)
		}
	}
	if !strings.Contains(got[rd.CheckKSMOwners].Remediation, "kube-state-metrics") {
		t.Errorf("ksm remediation %q", got[rd.CheckKSMOwners].Remediation)
	}
	if got[rd.CheckNodeLabels].RemediationType != hubapi.RemediationYAML || !strings.Contains(got[rd.CheckNodeLabels].Remediation, "metricLabelsAllowlist") {
		t.Errorf("node labels remediation %+v", got[rd.CheckNodeLabels])
	}
}

func TestMetricsCheckerPartial(t *testing.T) {
	q := healthy()
	coarseSpans, coarseCounts := coverageSamples(regularSeries(5, 2*time.Minute, 2*time.Hour))
	// pods owned by Jobs, but no kube_job_owner; cAdvisor without container label; coarse scrape;
	// 3 days of history; one node without capacity type; poor owner join; 1 of 2 nodes priced.
	q.rules = append([]rule{
		{has: []string{"count(last_over_time(kube_job_owner"}, res: nil},
		{has: []string{"container_cpu_usage_seconds_total", `container!=""`}, res: nil},
		{has: []string{"min_over_time("}, res: coarseSpans},
		{has: []string{"count_over_time(container_memory_working_set_bytes"}, res: coarseCounts},
		{has: []string{"offset 167h"}, res: nil},
		{has: []string{" and on (namespace, pod) "}, res: []recommender.VectorSample{{Sample: recommender.Sample{V: 100}}}},
		{has: []string{"kube_node_labels"}, res: []recommender.VectorSample{nodeSample("n1", true), nodeSample("n2", false)}},
	}, q.rules...)
	local := (&fakeQ{}).on(1, "kerf_node_hourly_cost_usd")
	rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Local: local, Now: func() time.Time { return now }}).Run(context.Background())
	got := byID(rep)
	if c := got[rd.CheckKSMOwners]; c.Status != "fail" || !strings.Contains(c.Message, "kube_job_owner") {
		t.Errorf("owners %+v", c)
	}
	if c := got[rd.CheckCAdvisor]; c.Status != "fail" || !strings.Contains(c.Message, "container label") {
		t.Errorf("cadvisor %+v", c)
	}
	if c := got[rd.CheckScrapeInterval]; c.Status != "warn" || c.Details["interval"] != "2m0s" {
		t.Errorf("scrape %+v", c)
	}
	if c := got[rd.CheckHistory]; c.Status != "warn" || c.Details["history"] != "≥3d" || c.Details["enoughDataAt"] != now.Add(4*24*time.Hour).Format(time.RFC3339) {
		t.Errorf("history %+v", c)
	}
	if c := got[rd.CheckOwnerJoin]; c.Status != "warn" || c.Details["coverage"] != "50.0" {
		t.Errorf("join %+v", c)
	}
	if c := got[rd.CheckNodeLabels]; c.Status != "warn" || c.Details["capacitytypeMissing"] != "1" || c.Details["regionMissing"] != "" {
		t.Errorf("node labels %+v", c)
	}
	if c := got[rd.CheckPricingCoverage]; c.Status != "warn" || !strings.Contains(c.Message, "1 of 2") {
		t.Errorf("pricing %+v", c)
	}
}

func TestPricingCoverageCUR(t *testing.T) {
	src := func(source string, v float64) recommender.VectorSample {
		return recommender.VectorSample{Labels: recommender.Labels{"source": source}, Sample: recommender.Sample{V: v}}
	}
	tests := []struct {
		name       string
		local      *fakeQ
		wantStatus string
		wantMsg    string
		wantDetail map[string]string
	}{
		{
			name: "cur off",
			local: (&fakeQ{}).onSamples([]recommender.VectorSample{src("list", 2)}, "count by (source)").
				on(2, "kerf_node_hourly_cost_usd"),
			wantStatus: "pass", wantMsg: "all 2 nodes priced",
			wantDetail: map[string]string{"source.list": "2", "curLastReadOK": ""},
		},
		{
			name: "cur in use",
			local: (&fakeQ{}).onSamples([]recommender.VectorSample{src("cur", 1), src("list", 1)}, "count by (source)").
				on(1, "kerf_pricing_cur_last_read_ok").on(2, "kerf_node_hourly_cost_usd"),
			wantStatus: "pass", wantMsg: "all 2 nodes priced",
			wantDetail: map[string]string{"source.cur": "1", "source.list": "1", "curLastReadOK": "true"},
		},
		{
			name: "cur unreadable",
			local: (&fakeQ{}).onSamples([]recommender.VectorSample{src("list", 2)}, "count by (source)").
				on(0, "kerf_pricing_cur_last_read_ok").on(2, "kerf_node_hourly_cost_usd"),
			wantStatus: "warn", wantMsg: "Cost and Usage Report cannot be read",
			wantDetail: map[string]string{"curLastReadOK": "false"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := healthy() // two nodes with labels
			rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Local: tt.local, Now: func() time.Time { return now }}).Run(context.Background())
			c := byID(rep)[rd.CheckPricingCoverage]
			if c.Status != tt.wantStatus || !strings.Contains(c.Message, tt.wantMsg) {
				t.Fatalf("pricing.coverage = %s %q, want %s %q", c.Status, c.Message, tt.wantStatus, tt.wantMsg)
			}
			if tt.wantStatus == "warn" && !strings.Contains(c.Remediation, "Cost and Usage Report") {
				t.Errorf("remediation %q", c.Remediation)
			}
			for k, v := range tt.wantDetail {
				if c.Details[k] != v {
					t.Errorf("details[%s] = %q, want %q (%v)", k, c.Details[k], v, c.Details)
				}
			}
		})
	}
}

func TestMetricsCheckerUnreachableAndLabel(t *testing.T) {
	// datasource down
	q := &fakeQ{err: errors.New("dial tcp: connection refused")}
	rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
	got := byID(rep)
	if got[rd.CheckDatasourceReachable].Status != "fail" || rep.Reachable || len(rep.Checks) != len(MetricsCheckIDs()) {
		t.Fatalf("unreachable %+v", rep)
	}
	for _, c := range rep.Checks[1:] {
		if c.Status != "unknown" {
			t.Errorf("%s = %s", c.ID, c.Status)
		}
	}

	// reachable through the API probe, but the label matches nothing: suggestions
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/label/k8s_cluster/") {
			_, _ = w.Write([]byte(`{"status":"success","data":["prod-eu2","prod-eu1"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()
	m, _ := datasource.NewMatcher("ro-spoke-2", &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu3"})
	rep = (&MetricsChecker{Q: &fakeQ{}, API: datasource.NewAPI(srv.URL, http.DefaultClient), M: m, Now: func() time.Time { return now }}).Run(context.Background())
	got = byID(rep)
	if got[rd.CheckDatasourceReachable].Status != "pass" || got[rd.CheckDatasourceReachable].Details["httpStatus"] != "200" {
		t.Errorf("reachable %+v", got[rd.CheckDatasourceReachable])
	}
	cl := got[rd.CheckClusterLabel]
	if cl.Status != "fail" || cl.Details["suggestions"] != "prod-eu1,prod-eu2" || !strings.Contains(cl.Message, `k8s_cluster="prod-eu3"`) {
		t.Errorf("cluster label %+v", cl)
	}
	if strings.Join(rep.LabelValues, ",") != "prod-eu1,prod-eu2" {
		t.Errorf("label values %v", rep.LabelValues)
	}
	if got[rd.CheckCAdvisor].Status != "unknown" {
		t.Errorf("cadvisor after label failure %+v", got[rd.CheckCAdvisor])
	}

	// probe fails with a hint
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer bad.Close()
	rep = (&MetricsChecker{Q: &fakeQ{}, API: datasource.NewAPI(bad.URL, http.DefaultClient), M: m, Now: func() time.Time { return now }}).Run(context.Background())
	if c := byID(rep)[rd.CheckDatasourceReachable]; c.Status != "fail" || c.Details["httpStatus"] != "401" || !strings.Contains(c.Remediation, "credentials") {
		t.Errorf("401 %+v", c)
	}
}

func TestMetricsCheckerQueryErrors(t *testing.T) {
	// reachable, label ok, then every other query fails: unknown, not a crash
	q := &fakeQ{}
	q.on(1, "vector(1)").on(10, `__name__=~`)
	failing := &failAfter{fakeQ: q, ok: 2}
	rep := (&MetricsChecker{Q: failing, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
	for _, c := range rep.Checks[2:] {
		if c.Status != "unknown" && c.ID != rd.CheckPricingCoverage {
			t.Errorf("%s = %s", c.ID, c.Status)
		}
	}
}

type failAfter struct {
	*fakeQ
	ok, n int
}

func (f *failAfter) Instant(ctx context.Context, q string, t time.Time) ([]recommender.VectorSample, error) {
	f.n++
	if f.n > f.ok {
		return nil, errors.New("boom")
	}
	return f.fakeQ.Instant(ctx, q, t)
}

// gpuQ is a healthy cluster with GPU nodes (2) and pods that request GPUs (3); series lists the DCGM
// series present and covered the number of pods that DCGM series map to.
func gpuQ(series []string, covered float64) *fakeQ {
	q := healthy()
	rules := []rule{
		{has: []string{"kube_node_status_allocatable", "nvidia_com_gpu"}, res: []recommender.VectorSample{{Sample: recommender.Sample{V: 2}}}},
		{has: []string{" and on (namespace, pod) ", "exported_pod", "nvidia_com_gpu"}, res: []recommender.VectorSample{{Sample: recommender.Sample{V: covered}}}},
		{has: []string{"kube_pod_container_resource_requests", "nvidia_com_gpu"}, res: []recommender.VectorSample{{Sample: recommender.Sample{V: 3}}}},
	}
	for _, m := range recommender.DCGMSeries {
		v := 0.0
		if slicesContains(series, m) {
			v = 4
		}
		rules = append(rules, rule{has: []string{"count(last_over_time(" + m}, res: []recommender.VectorSample{{Sample: recommender.Sample{V: v}}}})
	}
	q.rules = append(rules, q.rules...)
	return q
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestMetricsCheckerDCGM(t *testing.T) {
	tests := []struct {
		name    string
		q       *fakeQ
		status  string
		missing string
		msg     string
	}{
		{"no GPUs in the cluster", healthy(), "pass", "", "nothing to measure"},
		{"no DCGM series at all", gpuQ(nil, 0), "warn", "DCGM_FI_DEV_GPU_UTIL,DCGM_FI_DEV_FB_USED,DCGM_FI_DEV_FB_FREE", "series are missing: DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_FB_USED, DCGM_FI_DEV_FB_FREE"},
		{"memory series missing", gpuQ([]string{recommender.DCGMUtilization}, 3), "warn", "DCGM_FI_DEV_FB_USED,DCGM_FI_DEV_FB_FREE", "series are missing: DCGM_FI_DEV_FB_USED, DCGM_FI_DEV_FB_FREE"},
		{"free memory series missing", gpuQ([]string{recommender.DCGMUtilization, recommender.DCGMFramebufUse}, 3), "warn", "DCGM_FI_DEV_FB_FREE", "series are missing: DCGM_FI_DEV_FB_FREE"},
		{"series without pod mapping", gpuQ(recommender.DCGMSeries, 0), "warn", "", "pod mapping is off"},
		{"complete", gpuQ(recommender.DCGMSeries, 3), "pass", "", "DCGM series for 3 of 3 pods"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := (&MetricsChecker{Q: tc.q, M: datasource.DefaultMatcher("gpu"), DocsBase: "https://docs/", Now: func() time.Time { return now }}).Run(context.Background())
			c := byID(rep)[rd.CheckDCGM]
			if c.Status != tc.status || !strings.Contains(c.Message, tc.msg) || c.Details["missing"] != tc.missing {
				t.Fatalf("%+v", c)
			}
			if c.Severity != "warning" || c.Gates != "" {
				t.Errorf("a DCGM check must not gate: %+v", c)
			}
			if tc.status == "warn" {
				if !strings.Contains(c.Remediation, "https://docs/gpu") || c.RemediationType != hubapi.RemediationText || c.DocsURL != "https://docs/readiness#metrics.dcgm" {
					t.Errorf("remediation/docs: %+v", c)
				}
			}
			if !rep.OK() {
				t.Error("a warning must not fail the report")
			}
		})
	}
}
