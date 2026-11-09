package hub

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace"
	dtt "github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace/dynatracetest"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/readiness"
)

// TestDynatraceDatasourceAPI: a dynatrace datasource with its token in a mounted Secret is
// created, tested (the metric mapping), bound to a cluster, checked with Dynatrace
// remediations, and read by pricing through the node inventory instead of the query proxy.
func TestDynatraceDatasourceAPI(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dynatrace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dynatrace", "token"), []byte(dtt.Token), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.Cfg.Onboarding.DatasourceSecretsDir = dir
	e.initOnboarding()
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)
	fake := dtt.New(t, dtt.Healthy(t, e.now, "MilliCores")...)

	in := hubapi.DatasourceInput{Name: "dynatrace-prod", Type: hubapi.DatasourceDynatrace, URL: fake.URL, Reason: "Dynatrace metrics",
		Auth: &hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "dynatrace"}}
	r := e.sdo(admin, http.MethodPost, "/v1/admin/datasources", in)
	expectCode(t, "create", r, 201)
	if strings.Contains(string(r.body), dtt.Token) {
		t.Fatal("response contains the token")
	}
	var d hubapi.Datasource
	mustJSON(t, r.body, &d)
	if d.Type != hubapi.DatasourceDynatrace || d.Auth.TokenSecret != "dynatrace" || d.Auth.SecretConfigured {
		t.Errorf("created %+v", d)
	}
	bad := in
	bad.Name, bad.Auth = "dynatrace-bad", &hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "missing"}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/datasources", bad), 400, "invalid_datasource")

	// Test without a binding: the mapping over the whole environment, and its cluster names
	r = e.sdo(admin, http.MethodPost, "/v1/admin/datasources/"+d.ID+"/test", nil)
	expectCode(t, "test", r, 200)
	var tr hubapi.DatasourceTestResult
	mustJSON(t, r.body, &tr)
	if !tr.OK || len(tr.Metrics) != len(dynatrace.Inputs) || strings.Join(tr.LabelValues, ",") != "prod-eu1,staging-eu1" {
		t.Errorf("test %+v", tr)
	}
	// with a binding: every metrics check, the mapping and what it found
	r = e.sdo(admin, http.MethodPost, "/v1/admin/datasources/"+d.ID+"/test", hubapi.DatasourceTestRequest{Binding: &hubapi.MetricsBinding{ClusterLabel: "cluster", ClusterLabelValue: "prod-eu1"}})
	expectCode(t, "test binding", r, 200)
	tr = hubapi.DatasourceTestResult{}
	mustJSON(t, r.body, &tr)
	if !tr.OK || len(tr.Checks) != len(readiness.MetricsCheckIDs()) || len(tr.Metrics) != len(dynatrace.Inputs) || tr.Found == nil || tr.Found.Nodes != 3 {
		t.Errorf("test binding %+v", tr)
	}

	// bind c2; extra matchers are refused for a dynatrace datasource
	var ca hubapi.ClusterAdmin
	mustJSON(t, e.sdo(admin, http.MethodGet, "/v1/admin/clusters/c2", nil).body, &ca)
	e.problem(e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c2", hubapi.ClusterUpdate{Reason: "bind", Metrics: &hubapi.MetricsBinding{DatasourceID: d.ID, ClusterLabel: "cluster", ClusterLabelValue: "prod-eu1", ExtraMatchers: []string{`env="prod"`}}},
		"If-Match", fmt.Sprintf(`"%d"`, ca.Version)), 400, "invalid_metrics")
	r = e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c2", hubapi.ClusterUpdate{Reason: "bind", Metrics: &hubapi.MetricsBinding{DatasourceID: d.ID, ClusterLabel: "cluster", ClusterLabelValue: "prod-eu1"}},
		"If-Match", fmt.Sprintf(`"%d"`, ca.Version))
	expectCode(t, "bind", r, 200)

	// the hub checks of c2 run against Dynatrace with Dynatrace remediations
	if err := e.srv.runHubChecks(e.ctx, "c2"); err != nil {
		t.Fatal(err)
	}
	var cr hubapi.ClusterReadiness
	e.getJSON("/v1/clusters/c2/readiness", tokUI, &cr)
	found := map[string]hubapi.ReadinessCheck{}
	for _, c := range cr.Checks {
		found[c.ID] = c
	}
	if found[rd.CheckCAdvisor].Status != "pass" || found[rd.CheckHPASeries].Status != "unknown" || !strings.Contains(found[rd.CheckHPASeries].Remediation, "Dynatrace") {
		t.Errorf("readiness: cadvisor %+v hpa %+v", found[rd.CheckCAdvisor], found[rd.CheckHPASeries])
	}

	// pricing: the binding says dynatrace, the node inventory has the nodes, the proxy refuses PromQL
	var bl hubapi.MetricsBindingList
	e.getJSON("/v1/internal/metrics-bindings", tokAdmin, &bl)
	if len(bl.Items) != 2 || bl.Items[1].DatasourceType != hubapi.DatasourceDynatrace {
		t.Errorf("bindings %+v", bl)
	}
	var inv hubapi.NodeInventory
	e.getJSON("/v1/internal/clusters/c2/nodes", tokAdmin, &inv)
	if len(inv.Items) != 3 || inv.Items[0].CapacityType != "spot" || inv.Items[1].CapacityType != "ON_DEMAND" {
		t.Errorf("node inventory %+v", inv)
	}
	e.problem(e.do(http.MethodGet, "/v1/internal/clusters/c1/nodes", tokAdmin, nil), 409, hubapi.CodeNotSupported)
	r = e.do(http.MethodGet, "/v1/internal/datasources/"+d.ID+"/api/v1/query?query=up", tokAdmin, nil)
	if r.code != 400 || !strings.Contains(string(r.body), `"status":"error"`) {
		t.Errorf("proxy for dynatrace: %d %s", r.code, r.body)
	}

	// the recommender's binder hands out the Dynatrace source
	q, m, err := e.srv.metricsFor(e.ctx, "c2")
	if _, ok := q.(*dynatrace.Source); !ok || err != nil || m == nil || m.Value != "prod-eu1" {
		t.Errorf("metricsFor c2: %T %v %+v", q, err, m)
	}
}
