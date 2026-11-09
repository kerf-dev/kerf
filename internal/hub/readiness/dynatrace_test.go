package readiness

import (
	"context"
	"strings"
	"testing"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace"
	dtt "github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace/dynatracetest"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// prometheusOnly are words of remediations that only apply to a Prometheus-compatible store.
var prometheusOnly = []string{"kube-state-metrics", "scrape", "cAdvisor", "cadvisor", "relabel", "metricLabelsAllowlist", "helm install", "remote-write", "honorLabels", "vmagent", "job_name"}

func dynatraceChecker(t *testing.T, now time.Time, value string, rules ...dtt.Rule) (*MetricsChecker, *dtt.Server) {
	t.Helper()
	srv := dtt.New(t, append(rules, dtt.Healthy(t, now, "MilliCores")...)...)
	src := dynatrace.New(srv.URL, "", dtt.BearerClient())
	src.Client.Sleep = func(context.Context, time.Duration) error { return nil }
	return &MetricsChecker{
		Q: src, M: datasource.Matcher{Cluster: "prod", Label: "cluster", Value: value},
		Local: fakeLocal{}, HistoryWindow: 14 * 24 * time.Hour, Now: func() time.Time { return now },
	}, srv
}

// fakeLocal is the hub's own store with every node priced.
type fakeLocal struct{}

func (fakeLocal) Instant(_ context.Context, q string, _ time.Time) ([]recommender.VectorSample, error) {
	if strings.Contains(q, "kerf_node_hourly_cost_usd") && strings.HasPrefix(q, "count(max by (node)") {
		return []recommender.VectorSample{{Sample: recommender.Sample{V: 3}}}, nil
	}
	return nil, nil
}

func (fakeLocal) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]recommender.Series, error) {
	return nil, nil
}

func checksByID(checks []hubapi.ReadinessCheck) map[string]hubapi.ReadinessCheck {
	out := map[string]hubapi.ReadinessCheck{}
	for _, c := range checks {
		out[c.ID] = c
	}
	return out
}

func TestDynatraceChecks(t *testing.T) {
	now := time.Date(2026, 11, 9, 11, 20, 30, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		rules []dtt.Rule
		want  map[string]rd.Status
		// in the message of a check
		msg map[string]string
	}{
		{
			name:  "healthy cluster",
			value: "prod-eu1",
			want: map[string]rd.Status{
				rd.CheckDatasourceReachable: rd.StatusPass, rd.CheckClusterLabel: rd.StatusPass, rd.CheckCAdvisor: rd.StatusPass,
				rd.CheckKSMOwners: rd.StatusPass, rd.CheckKSMResources: rd.StatusPass, rd.CheckFreshness: rd.StatusPass,
				rd.CheckOwnerJoin: rd.StatusPass, rd.CheckScrapeInterval: rd.StatusPass, rd.CheckHistory: rd.StatusPass,
				rd.CheckThrottling: rd.StatusPass, rd.CheckRestartsOOM: rd.StatusPass, rd.CheckHPASeries: rd.StatusUnknown,
				rd.CheckNodeLabels: rd.StatusWarn, rd.CheckNodeAllocatable: rd.StatusPass, rd.CheckDCGM: rd.StatusUnknown,
				rd.CheckPricingCoverage: rd.StatusPass,
			},
			msg: map[string]string{
				rd.CheckThrottling:   "derived throttling ratio",
				rd.CheckHPASeries:    "HPA coupling not available",
				rd.CheckNodeLabels:   "instance type on 1 of 3 nodes",
				rd.CheckClusterLabel: `k8s.cluster.name="prod-eu1"`,
			},
		},
		{
			name:  "wrong cluster name",
			value: "prod",
			rules: []dtt.Rule{{Contains: []string{`k8s.cluster.name == "prod"`}, Records: "[]"}},
			want: map[string]rd.Status{
				rd.CheckDatasourceReachable: rd.StatusPass, rd.CheckClusterLabel: rd.StatusFail, rd.CheckCAdvisor: rd.StatusUnknown, rd.CheckHPASeries: rd.StatusUnknown,
			},
			msg: map[string]string{rd.CheckClusterLabel: "existing values of k8s.cluster.name: prod-eu1, staging-eu1"},
		},
		{
			name:  "no Kubernetes monitoring",
			value: "prod-eu1",
			rules: []dtt.Rule{
				{Contains: []string{"count(dt.kubernetes.container.cpu_usage, scalar: true)"}, Records: "[]"},
				{Contains: []string{"count(dt.kubernetes.container.requests_cpu, scalar: true)"}, Records: "[]"},
				{Contains: []string{"count(dt.containers.cpu.throttling_ratio, scalar: true)"}, Records: "[]"},
				{Contains: []string{"count(dt.kubernetes.container.restarts, scalar: true)"}, Records: "[]"},
				{Contains: []string{"count(dt.kubernetes.container.oom_kills, scalar: true)"}, Records: "[]"},
			},
			want: map[string]rd.Status{
				rd.CheckClusterLabel: rd.StatusPass, rd.CheckCAdvisor: rd.StatusFail, rd.CheckKSMResources: rd.StatusFail,
				rd.CheckThrottling: rd.StatusWarn, rd.CheckRestartsOOM: rd.StatusWarn,
			},
			msg: map[string]string{rd.CheckCAdvisor: "missing dt.kubernetes.container.cpu_usage"},
		},
		{
			name:  "token without a scope",
			value: "prod-eu1",
			rules: []dtt.Rule{{Contains: []string{"data record"}, Status: 403, Body: `{"error":{"code":403,"message":"missing scope"}}`}},
			want:  map[string]rd.Status{rd.CheckDatasourceReachable: rd.StatusFail, rd.CheckClusterLabel: rd.StatusUnknown},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc, _ := dynatraceChecker(t, now, tc.value, tc.rules...)
			rep := mc.Run(context.Background())
			if len(rep.Checks) != len(metricsCheckIDs) {
				t.Fatalf("%d checks, want %d", len(rep.Checks), len(metricsCheckIDs))
			}
			for i, c := range rep.Checks {
				if c.ID != metricsCheckIDs[i] {
					t.Errorf("check %d is %s, want %s (catalog order)", i, c.ID, metricsCheckIDs[i])
				}
			}
			got := checksByID(rep.Checks)
			for id, st := range tc.want {
				if got[id].Status != string(st) {
					t.Errorf("%s = %s (%s), want %s", id, got[id].Status, got[id].Message, st)
				}
			}
			for id, sub := range tc.msg {
				if !strings.Contains(got[id].Message, sub) {
					t.Errorf("%s message %q, want %q", id, got[id].Message, sub)
				}
			}
			for _, c := range rep.Checks {
				for _, w := range prometheusOnly {
					if strings.Contains(c.Remediation, w) {
						t.Errorf("%s remediation suggests a Prometheus-only step (%q): %s", c.ID, w, c.Remediation)
					}
				}
				if (c.Status == string(rd.StatusFail) || c.Status == string(rd.StatusWarn)) && c.Remediation == "" {
					t.Errorf("%s %s without a remediation", c.ID, c.Status)
				}
			}
		})
	}
}

func TestDynatraceReportMetrics(t *testing.T) {
	now := time.Date(2026, 11, 9, 11, 20, 30, 0, time.UTC)
	mc, _ := dynatraceChecker(t, now, "prod-eu1")
	mc.Preview = true
	rep := mc.Run(context.Background())
	if !rep.OK() || !rep.Reachable {
		t.Fatalf("report not ok: %+v", rep.Checks)
	}
	if len(rep.Metrics) != len(dynatrace.Inputs) || rep.Found.ContainerSeries != 4 || rep.Found.Pods != 3 || rep.Found.Nodes != 3 {
		t.Errorf("metrics %d, found %+v", len(rep.Metrics), rep.Found)
	}
	if h := checksByID(rep.Checks)[rd.CheckHPASeries]; h.Remediation != DynatraceHPAUnknown {
		t.Errorf("hpa remediation = %q", h.Remediation)
	}
}
