package hub

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/readiness"
)

// fakeProm is a Prometheus HTTP API answering instant queries by substring rules (value 0
// otherwise) and recording requests.
type fakeProm struct {
	mu     sync.Mutex
	auth   []string
	orgID  []string
	query  []string
	values map[string]float64
}

func (f *fakeProm) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.Form.Get("query")
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.orgID = append(f.orgID, r.Header.Get("X-Scope-OrgID"))
		f.query = append(f.query, q)
		v, found := 0.0, false
		for k, val := range f.values {
			if strings.Contains(q, k) {
				v, found = val, true
				break
			}
		}
		f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/v1/label/") {
			_, _ = w.Write([]byte(`{"status":"success","data":["prod-eu1"]}`))
			return
		}
		res := "[]"
		if found || q == "vector(1)" {
			res = fmt.Sprintf(`[{"metric":{},"value":[%d,"%g"]}]`, time.Now().Unix(), v)
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, res)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (e *testEnv) initOnboarding() {
	e.t.Helper()
	k := make([]byte, datasource.KeySize)
	_, _ = rand.Read(k)
	c, err := datasource.NewCipher(k)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.srv.InitOnboarding(e.ctx, c); err != nil {
		e.t.Fatal(err)
	}
}

func TestDatasourcesAPI(t *testing.T) {
	e := newEnv(t)
	e.initOnboarding()
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)
	viewer, _ := e.login("viewer", testPassword)
	fp := &fakeProm{values: map[string]float64{}}
	ps := fp.server(t)

	// local exists and is built in
	var list hubapi.DatasourceList
	e.getJSON("/v1/admin/datasources", tokUI, &list)
	if len(list.Items) != 1 || list.Items[0].ID != "local" || !list.Items[0].BuiltIn || len(list.Items[0].UsedBy) != 2 {
		t.Fatalf("datasources %+v", list)
	}
	in := hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: ps.URL, Reason: "EU metrics",
		Auth: &hubapi.DatasourceAuthInput{Type: "bearer", Token: "s3cret-token"}, Headers: map[string]string{"X-Scope-OrgID": "tenant-1"}}
	e.problem(e.sdo(viewer, http.MethodPost, "/v1/admin/datasources", in), 403, "role_forbidden")
	r := e.sdo(admin, http.MethodPost, "/v1/admin/datasources", in)
	expectCode(t, "create", r, 201)
	if strings.Contains(string(r.body), "s3cret") {
		t.Fatal("response contains the secret")
	}
	var d hubapi.Datasource
	mustJSON(t, r.body, &d)
	if !d.Auth.SecretConfigured || r.header.Get("ETag") != `"1"` {
		t.Errorf("created %+v %v", d, r.header)
	}
	r = e.sdo(viewer, http.MethodGet, "/v1/admin/datasources/"+d.ID, nil)
	expectCode(t, "get", r, 200)
	if strings.Contains(string(r.body), "s3cret") {
		t.Fatal("GET contains the secret")
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/datasources", hubapi.DatasourceInput{Name: "x", Type: "influx", URL: ps.URL, Reason: "r"}), 400, "invalid_datasource")

	// test without a binding: reachability only, with the datasource's auth and headers
	r = e.sdo(admin, http.MethodPost, "/v1/admin/datasources/"+d.ID+"/test", nil)
	expectCode(t, "test", r, 200)
	var tr hubapi.DatasourceTestResult
	mustJSON(t, r.body, &tr)
	if !tr.OK || len(tr.Checks) != 1 || tr.Checks[0].ID != rd.CheckDatasourceReachable || tr.Checks[0].Status != "pass" {
		t.Errorf("test %+v", tr)
	}
	if fp.auth[0] != "Bearer s3cret-token" || fp.orgID[0] != "tenant-1" {
		t.Errorf("datasource request auth %q org %q", fp.auth[0], fp.orgID[0])
	}
	// test with a binding: every metrics check, against the binding's matchers
	fp.mu.Lock()
	fp.values = map[string]float64{`__name__=~`: 100, "container_cpu_usage_seconds_total": 50, "count(last_over_time(container_memory_working_set_bytes": 50}
	fp.query = nil
	fp.mu.Unlock()
	r = e.sdo(admin, http.MethodPost, "/v1/admin/datasources/"+d.ID+"/test", hubapi.DatasourceTestRequest{Binding: &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu1"}})
	expectCode(t, "test binding", r, 200)
	mustJSON(t, r.body, &tr)
	if tr.OK || len(tr.Checks) != len(readiness.MetricsCheckIDs()) || tr.Found == nil || tr.Found.ContainerSeries != 50 || strings.Join(tr.LabelValues, ",") != "prod-eu1" {
		t.Errorf("test binding %+v", tr)
	}
	for _, q := range fp.query {
		if q != "" && q != "vector(1)" && !strings.Contains(q, `k8s_cluster="prod-eu1"`) {
			t.Errorf("query without the binding: %s", q)
		}
	}

	// bind ro c2 to it, then delete is refused
	var ca hubapi.ClusterAdmin
	r = e.sdo(admin, http.MethodGet, "/v1/admin/clusters/c2", nil)
	mustJSON(t, r.body, &ca)
	r = e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c2", hubapi.ClusterUpdate{Reason: "bind", Metrics: &hubapi.MetricsBinding{DatasourceID: d.ID, ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu1", ExtraMatchers: []string{` env = "prod"`}}},
		"If-Match", fmt.Sprintf(`"%d"`, ca.Version))
	expectCode(t, "bind", r, 200)
	mustJSON(t, r.body, &ca)
	if ca.Metrics.DatasourceID != d.ID || strings.Join(ca.Metrics.ExtraMatchers, ",") != `env="prod"` {
		t.Errorf("binding %+v", ca.Metrics)
	}
	e.problem(e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c2", hubapi.ClusterUpdate{Reason: "bad", Metrics: &hubapi.MetricsBinding{DatasourceID: d.ID, ClusterLabel: "c", ExtraMatchers: []string{"env=prod"}}},
		"If-Match", fmt.Sprintf(`"%d"`, ca.Version)), 400, "invalid_metrics")
	e.problem(e.sdo(admin, http.MethodDelete, "/v1/admin/datasources/"+d.ID, hubapi.ChangeReason{Reason: "gone"}, "If-Match", `"1"`), 409, hubapi.CodeDatasourceInUse)
	e.problem(e.sdo(admin, http.MethodDelete, "/v1/admin/datasources/local", hubapi.ChangeReason{Reason: "gone"}, "If-Match", `"1"`), 409, hubapi.CodeBuiltin)

	// the recommender and the usage endpoint resolve the binding
	q, m, err := e.srv.metricsFor(e.ctx, "c2")
	if err != nil || m == nil || m.Label != "k8s_cluster" || q == e.srv.Prom {
		t.Errorf("metricsFor c2: %v %+v", err, m)
	}
	if q, m, _ := e.srv.metricsFor(e.ctx, "c1"); m != nil || q != e.srv.Prom {
		t.Errorf("metricsFor c1 must be the default: %+v", m)
	}

	// pricing proxy (an admin token may call it; the pricing role is the intended caller)
	var bl hubapi.MetricsBindingList
	e.getJSON("/v1/internal/metrics-bindings", tokAdmin, &bl)
	if len(bl.Items) != 2 || bl.Items[0].Bound || !bl.Items[1].Bound || bl.Items[1].Binding.DatasourceID != d.ID {
		t.Errorf("bindings %+v", bl)
	}
	expectCode(t, "bindings ui", e.do(http.MethodGet, "/v1/internal/metrics-bindings", tokUI, nil), 403)
	fp.mu.Lock()
	fp.auth = nil
	fp.mu.Unlock()
	r = e.do(http.MethodGet, "/v1/internal/datasources/"+d.ID+"/api/v1/query?query=vector(1)&junk=1", tokAdmin, nil)
	expectCode(t, "proxy", r, 200)
	if !strings.Contains(string(r.body), `"status":"success"`) || fp.auth[0] != "Bearer s3cret-token" {
		t.Errorf("proxy %s %v", r.body, fp.auth)
	}
	e.problem(e.do(http.MethodGet, "/v1/internal/datasources/"+d.ID+"/api/v1/query", tokAdmin, nil), 400, "invalid_query")
	expectCode(t, "proxy missing", e.do(http.MethodGet, "/v1/internal/datasources/nope/api/v1/query?query=up", tokAdmin, nil), 404)

	// unbind, delete; audited
	r = e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c2", hubapi.ClusterUpdate{Reason: "unbind", Metrics: &hubapi.MetricsBinding{DatasourceID: "local", ClusterLabel: "cluster"}},
		"If-Match", fmt.Sprintf(`"%d"`, ca.Version))
	expectCode(t, "unbind", r, 200)
	expectCode(t, "delete", e.sdo(admin, http.MethodDelete, "/v1/admin/datasources/"+d.ID, hubapi.ChangeReason{Reason: "gone"}, "If-Match", `"1"`), 204)
	var audit hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetType=datasource", tokUI, &audit)
	var actions []string
	for _, it := range audit.Items {
		actions = append(actions, it.Action)
		if strings.Contains(fmt.Sprint(it.After)+fmt.Sprint(it.Before), "s3cret") {
			t.Error("audit contains the secret")
		}
	}
	if !strings.Contains(strings.Join(actions, ","), hubapi.ActionDatasourceCreated) || !strings.Contains(strings.Join(actions, ","), hubapi.ActionDatasourceDeleted) {
		t.Errorf("audit actions %v", actions)
	}
}

// pass returns passing results for checks.
func passing(ids []string, at time.Time) []hubapi.ReadinessCheck {
	out := make([]hubapi.ReadinessCheck, 0, len(ids))
	for _, id := range ids {
		out = append(out, readiness.NewCheck(id, rd.StatusPass, "ok", "", "", "", at, nil))
	}
	return out
}

func agentChecks(at time.Time, failing ...string) []hubapi.ReadinessCheck {
	var out []hubapi.ReadinessCheck
	for _, id := range readiness.AgentCheckIDs() {
		st := rd.StatusPass
		for _, f := range failing {
			if f == id {
				st = rd.StatusFail
			}
		}
		c := readiness.NewCheck(id, st, "m", "", "", "", at, nil)
		c.Severity, c.Category = "info", "x" // the hub takes these from the catalog
		out = append(out, c)
	}
	return out
}

func TestReadinessAndGating(t *testing.T) {
	e := newEnv(t)
	e.initOnboarding()
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)
	operator, _ := e.login("operator", testPassword)

	var cr hubapi.ClusterReadiness
	e.getJSON("/v1/clusters/c1/readiness", tokUI, &cr)
	if cr.State != "Registered" || cr.NextState != "AgentConnected" {
		t.Fatalf("initial %+v", cr)
	}

	// auto-patch is refused while c1 is not AutomationReady
	p := autoPatchPolicy("ap", 100, []string{"c1"}, "shop")
	pr := e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/policies", p), 409, hubapi.CodeClusterNotAutomationReady)
	if !strings.Contains(pr.Detail, "c1 (Registered)") {
		t.Errorf("detail %q", pr.Detail)
	}
	// a global default with auto-patch is gated by every cluster
	gd := hubapi.PolicyInput{Type: v1alpha1.HubPolicyTypeGlobalDefault, Reason: "fleet",
		Spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch}}}
	pr = e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/policies", gd), 409, hubapi.CodeClusterNotAutomationReady)
	if !strings.Contains(pr.Detail, "c2") {
		t.Errorf("global detail %q", pr.Detail)
	}
	// admin override, audited
	p.OverrideReadiness = true
	r := e.sdo(admin, http.MethodPost, "/v1/admin/policies", p)
	expectCode(t, "override", r, 201)
	var audit hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetType=policy", tokUI, &audit)
	if len(audit.Items) == 0 || !strings.Contains(string(audit.Items[0].After), `"overrideReadiness":true`) {
		t.Errorf("override not audited: %+v", audit.Items)
	}
	// operator: approve-once is gated; overrideReadiness is admin-only
	var wl hubapi.WorkloadList
	e.getJSON("/v1/workloads?cluster=c1", tokUI, &wl)
	var target *hubapi.WorkloadRow
	for i := range wl.Items {
		if wl.Items[i].RecommendationID != "" {
			target = &wl.Items[i]
			break
		}
	}
	if target == nil {
		t.Fatal("no workload with a recommendation")
	}
	path := "/v1/workloads/" + url.PathEscape(target.ID) + "/approve"
	body := hubapi.ApprovalRequest{RecommendationID: target.RecommendationID, Reason: "ok"}
	e.problem(e.sdo(operator, http.MethodPost, path, body), 409, hubapi.CodeClusterNotAutomationReady)
	body.OverrideReadiness = true
	e.problem(e.sdo(operator, http.MethodPost, path, body), 403, hubapi.CodeAdminRequired)

	// readiness: hub checks (agent pulled, recommendations exist, metrics pass) and agent checks
	e.config(tokC1, "c1", "")
	now := e.now
	if _, err := e.srv.Onb.Readiness.Apply(e.ctx, "c1", readiness.Update{Checks: passing(readiness.MetricsCheckIDs(), now), ReplaceIDs: readiness.MetricsCheckIDs(), At: now}); err != nil {
		t.Fatal(err)
	}
	e.srv.refreshAgentChecks(e.ctx)
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{
		Capabilities: []hubapi.AgentCapability{{Name: "resize", Granted: true}}, Checks: agentChecks(now)}, &res)
	e.getJSON("/v1/clusters/c1/readiness", tokUI, &cr)
	if cr.State != "AutomationReady" || len(cr.Capabilities) != 1 {
		t.Fatalf("all pass: %+v", cr)
	}
	for _, c := range cr.Checks {
		if c.ID == rd.CheckRBACResize && (c.Severity != "blocking" || c.Category != "rbac") {
			t.Errorf("agent check not normalised: %+v", c)
		}
	}
	// now auto-patch passes without override
	p2 := autoPatchPolicy("ap2", 101, []string{"c1"}, "shop")
	expectCode(t, "ready", e.sdo(admin, http.MethodPost, "/v1/admin/policies", p2), 201)

	// the agent loses pods/resize: ReadyAudit, gating again, gauges and history follow
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Checks: agentChecks(now, rd.CheckRBACResize)}, &res)
	e.getJSON("/v1/clusters/c1/readiness", tokUI, &cr)
	if cr.State != "ReadyAudit" || strings.Join(cr.BlockedBy, ",") != rd.CheckRBACResize || cr.Summary.Blocking != 1 {
		t.Fatalf("resize lost: %+v", cr)
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/policies", autoPatchPolicy("ap3", 102, []string{"c1"}, "shop")), 409, hubapi.CodeClusterNotAutomationReady)
	var cl hubapi.ClusterList
	e.getJSON("/v1/clusters", tokUI, &cl)
	for _, c := range cl.Items {
		if c.Name == "c1" && (c.Readiness == nil || c.Readiness.State != "ReadyAudit" || c.Readiness.Blocking != 1) {
			t.Errorf("cluster list readiness %+v", c.Readiness)
		}
	}
	var hist hubapi.ReadinessHistory
	e.getJSON("/v1/clusters/c1/readiness/history?limit=50", tokUI, &hist)
	var states []string
	for _, h := range hist.Items {
		if h.Kind == "state" {
			states = append(states, h.FromState+">"+h.ToState)
		}
	}
	if len(states) < 2 || states[0] != "AutomationReady>ReadyAudit" {
		t.Errorf("history states %v", states)
	}
	e.problem(e.do(http.MethodGet, "/v1/clusters/c1/readiness/history?limit=0", tokUI, nil), 400, "invalid_limit")
	m := e.metrics()
	if v, ok := m.find("ro_cluster_readiness", map[string]string{"cluster": "c1", "check": rd.CheckRBACResize}); !ok || v != "0" {
		t.Errorf("ro_cluster_readiness resize = %v %v", v, ok)
	}
	if v, ok := m.find("ro_cluster_readiness_state", map[string]string{"cluster": "c1", "state": "ReadyAudit"}); !ok || v != "1" {
		t.Errorf("state gauge = %v %v", v, ok)
	}

	// re-check: 202, and the next config carries the flag until newer agent checks arrive
	r = e.sdo(operator, http.MethodPost, "/v1/clusters/c1/readiness/recheck", nil)
	expectCode(t, "recheck", r, 202)
	var acc hubapi.RecheckAccepted
	mustJSON(t, r.body, &acc)
	cfg, _ := e.config(tokC1, "c1", "")
	if !cfg.Flags.Recheck || cfg.Flags.RecheckRequestedAt == nil || !cfg.Flags.RecheckRequestedAt.Equal(acc.RequestedAt) {
		t.Errorf("config flags %+v", cfg.Flags)
	}
	e.advance(time.Minute)
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Checks: agentChecks(e.now)}, &res)
	cfg2, _ := e.config(tokC1, "c1", "")
	if cfg2.Flags.Recheck || cfg2.ConfigVersion == cfg.ConfigVersion {
		t.Errorf("recheck not cleared: %+v", cfg2.Flags)
	}
	expectCode(t, "recheck viewer", e.do(http.MethodPost, "/v1/clusters/c1/readiness/recheck", tokUI, nil), 403)
}

func TestInstallSnippetAPI(t *testing.T) {
	e := newEnv(t)
	e.initOnboarding()
	e.problem(e.do(http.MethodGet, "/v1/admin/clusters/c1/install", tokAdmin, nil), 409, "install_not_configured")
	e.srv.Cfg.Onboarding.AgentChart = "oci://ghcr.io/x/charts/agent"
	e.srv.Cfg.Onboarding.HubURL = "http://hub-api.hub.svc:8080"
	var s hubapi.InstallSnippet
	e.getJSON("/v1/admin/clusters/c1/install?format=values", tokAdmin, &s)
	if s.Format != "values" || !strings.Contains(s.Content, "clusterName: c1") || !strings.Contains(s.ArgoCDIgnoreDifferences, "resource-optimizer") {
		t.Errorf("snippet %+v", s)
	}
	e.problem(e.do(http.MethodGet, "/v1/admin/clusters/c1/install?format=tf", tokAdmin, nil), 400, "invalid_format")
	expectCode(t, "ui", e.do(http.MethodGet, "/v1/admin/clusters/c1/install", tokUI, nil), 403)
	// a bound cluster gets the health watch matchers
	if err := e.srv.Onb.Bind.PG.PutBinding(e.ctx, "c1", hubapi.MetricsBinding{DatasourceID: "local", ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod"}, hubapi.Actor{Type: "system", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	e.getJSON("/v1/admin/clusters/c1/install?format=helm", tokAdmin, &s)
	if !strings.Contains(s.Content, "health.clusterLabel=k8s_cluster") || !strings.Contains(s.Content, "health.clusterLabelValue=prod") {
		t.Errorf("helm snippet %s", s.Content)
	}
}

// TestPricingTokenScope: a pricing token (D-026) reaches only the two internal endpoints.
func TestPricingTokenScope(t *testing.T) {
	e := newEnv(t)
	e.initOnboarding()
	const tokPricing = "test-pricing"
	if err := e.st.EnsureToken(e.ctx, tokPricing, hubapi.RolePricing, "", "pricing"); err != nil {
		t.Fatal(err)
	}
	var bl hubapi.MetricsBindingList
	e.getJSON("/v1/internal/metrics-bindings", tokPricing, &bl)
	if len(bl.Items) != 2 {
		t.Errorf("bindings %+v", bl)
	}
	fp := &fakeProm{values: map[string]float64{"up": 1}}
	ps := fp.server(t)
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE datasources SET url = $1 WHERE id = 'local'`, ps.URL); err != nil {
		t.Fatal(err)
	}
	e.srv.Cfg.PrometheusURL = ps.URL
	expectCode(t, "proxy", e.do(http.MethodGet, "/v1/internal/datasources/local/api/v1/query?query=up", tokPricing, nil), 200)
	expectCode(t, "proxy range", e.do(http.MethodPost, "/v1/internal/datasources/local/api/v1/query_range", tokPricing,
		"query=up&start=1&end=2&step=1", "Content-Type", "application/x-www-form-urlencoded"), 200)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/clusters"}, {http.MethodGet, "/v1/workloads"}, {http.MethodGet, "/v1/summary"},
		{http.MethodGet, "/v1/audit"}, {http.MethodGet, "/v1/admin/datasources"}, {http.MethodGet, "/v1/clusters/c1/recommendations"},
		{http.MethodGet, "/v1/clusters/c1/config"}, {http.MethodGet, "/v1/clusters/c1/readiness"}, {http.MethodGet, "/v1/admin/tokens"},
		{http.MethodPost, "/v1/admin/recompute"}, {http.MethodGet, "/v1/me"},
	} {
		if r := e.do(rt.method, rt.path, tokPricing, nil); r.code != http.StatusForbidden && r.code != http.StatusUnauthorized {
			t.Errorf("%s %s with a pricing token: %d", rt.method, rt.path, r.code)
		}
	}
	// admins can create pricing tokens (without a cluster)
	r := e.do(http.MethodPost, "/v1/admin/tokens", tokAdmin, hubapi.TokenCreate{Role: hubapi.RolePricing, Reason: "pricing service"})
	expectCode(t, "create pricing token", r, 201)
	r = e.do(http.MethodPost, "/v1/admin/tokens", tokAdmin, hubapi.TokenCreate{Role: hubapi.RolePricing, Cluster: "c1", Reason: "x"})
	expectCode(t, "pricing token with cluster", r, 400)
}
