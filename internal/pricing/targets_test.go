package pricing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// recordingVM answers discovery queries and records them; when a query relabels (label_replace to
// cluster=<name>), rows carry that name, as the real PromQL would.
type recordingVM struct {
	mu      sync.Mutex
	queries []string
	auth    []string
	labels  func(q string) []string // rows
	alloc   func(q string) []string
}

func (v *recordingVM) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		v.mu.Lock()
		v.queries = append(v.queries, q)
		v.auth = append(v.auth, r.Header.Get("Authorization"))
		v.mu.Unlock()
		var rows []string
		if strings.Contains(q, "kube_node_labels") {
			rows = v.labels(q)
		} else {
			rows = v.alloc(q)
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(rows, ","))
	}))
}

type staticTargets []Target

func (s staticTargets) Targets(context.Context) ([]Target, error) { return s, nil }

func TestDiscoveryDefaultQueriesUnchanged(t *testing.T) {
	vm := &recordingVM{labels: func(string) []string { return nil }, alloc: func(string) []string { return nil }}
	srv := vm.server(t)
	defer srv.Close()
	if _, err := (&Discovery{VMURL: srv.URL}).Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`max by (cluster, node, label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, label_eks_amazonaws_com_capacity_type, label_topology_kubernetes_io_region, label_topology_kubernetes_io_zone) (last_over_time(kube_node_labels{cluster!=""}[600s]))`,
		`max by (cluster, node, resource) (last_over_time(kube_node_status_allocatable{cluster!="", resource=~"cpu|memory"}[600s]))`,
	}
	if strings.Join(vm.queries, "\n") != strings.Join(want, "\n") {
		t.Errorf("queries changed:\n%s", strings.Join(vm.queries, "\n"))
	}
}

func TestDiscoveryTargets(t *testing.T) {
	// local VM: ro-spoke-1 and a stale ro-spoke-2 entry (label value happens to equal the name)
	local := &recordingVM{
		labels: func(string) []string {
			return []string{labelRow("ro-spoke-1", "n1", "m6i.xlarge", "on-demand", "eu-west-1", "eu-west-1a"),
				labelRow("ro-spoke-2", "old", "m5.large", "on-demand", "eu-west-1", "")}
		},
		alloc: func(string) []string { return []string{allocRow("ro-spoke-1", "n1", "cpu", "4")} },
	}
	// AMP-like datasource of ro-spoke-2 under k8s_cluster="prod-eu1"
	remote := &recordingVM{
		labels: func(q string) []string {
			if !strings.Contains(q, `label_replace(`) {
				return nil
			}
			return []string{labelRow("ro-spoke-2", "p1", "c6a.2xlarge", "spot", "eu-west-1", "eu-west-1b")}
		},
		alloc: func(string) []string { return []string{allocRow("ro-spoke-2", "p1", "memory", "17179869184")} },
	}
	ls, rs := local.server(t), remote.server(t)
	defer ls.Close()
	defer rs.Close()
	m, _ := datasource.NewMatcher("ro-spoke-2", &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu1", ExtraMatchers: []string{`env="prod"`}})
	broken, _ := datasource.NewMatcher("ro-spoke-3", nil)
	d := &Discovery{VMURL: ls.URL, Targets: staticTargets{
		{Cluster: "ro-spoke-2", BaseURL: rs.URL, HTTP: http.DefaultClient, Matcher: m},
		{Cluster: "ro-spoke-3", BaseURL: "http://127.0.0.1:1", HTTP: http.DefaultClient, Matcher: broken},
	}}
	nodes, err := d.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, fmt.Sprintf("%s/%s/%s/%.0f", n.Cluster, n.Name, n.InstanceType, n.AllocatableGiB))
	}
	if strings.Join(got, " ") != "ro-spoke-1/n1/m6i.xlarge/0 ro-spoke-2/p1/c6a.2xlarge/16" {
		t.Errorf("nodes %v", got)
	}
	for _, q := range remote.queries {
		if !strings.Contains(q, `k8s_cluster="prod-eu1", env="prod"`) || !strings.Contains(q, `"cluster", "ro-spoke-2", "", ""`) {
			t.Errorf("remote query without binding/relabel: %s", q)
		}
	}

	// cluster regex filters bound clusters by registered name
	d.ClusterRegex = "ro-spoke-1"
	remote.queries = nil
	if _, err := d.Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(remote.queries) != 0 {
		t.Errorf("filtered target queried: %v", remote.queries)
	}
	d.ClusterRegex = "("
	if _, err := d.Nodes(context.Background()); err == nil {
		t.Error("bad regex accepted")
	}
}

type errTargets struct{}

func (errTargets) Targets(context.Context) ([]Target, error) { return nil, errors.New("db down") }

func TestDiscoveryTargetsError(t *testing.T) {
	vm := &recordingVM{labels: func(string) []string { return nil }, alloc: func(string) []string { return nil }}
	srv := vm.server(t)
	defer srv.Close()
	if _, err := (&Discovery{VMURL: srv.URL, Targets: errTargets{}}).Nodes(context.Background()); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("err = %v", err)
	}
}

// memBindings and memDS are small in-memory stand-ins for the hub tables.
type memBindings map[string]hubapi.MetricsBinding

func (m memBindings) Bindings(context.Context) (map[string]hubapi.MetricsBinding, error) { return m, nil }

type memDS struct{ recs map[string]datasource.Record }

func (m *memDS) ListDatasources(context.Context) ([]datasource.Record, error) { return nil, nil }
func (m *memDS) GetDatasource(_ context.Context, id string) (datasource.Record, error) {
	r, ok := m.recs[id]
	if !ok {
		return r, datasource.ErrNotFound
	}
	return r, nil
}
func (m *memDS) InsertDatasource(_ context.Context, r datasource.Record) error {
	m.recs[r.ID] = r
	return nil
}
func (m *memDS) UpdateDatasource(context.Context, datasource.Record) error { return nil }
func (m *memDS) DeleteDatasource(context.Context, string, int, hubapi.Actor) error {
	return nil
}
func (m *memDS) UsedBy(context.Context) (map[string][]string, error) { return nil, nil }
func (m *memDS) UpsertLocal(context.Context, string) error            { return nil }

func TestRegistryTargets(t *testing.T) {
	remote := &recordingVM{
		labels: func(string) []string {
			return []string{labelRow("ro-spoke-2", "p1", "c6a.2xlarge", "spot", "eu-west-1", "eu-west-1b")}
		},
		alloc: func(string) []string { return nil },
	}
	rs := remote.server(t)
	defer rs.Close()
	key := make([]byte, datasource.KeySize)
	_, _ = rand.Read(key)
	c, _ := datasource.NewCipher(key)
	reg := &datasource.Registry{Repo: &memDS{recs: map[string]datasource.Record{}}, Cipher: c}
	ds, err := reg.Create(context.Background(), hubapi.DatasourceInput{Name: "mimir", Type: "mimir", URL: rs.URL,
		Auth: &hubapi.DatasourceAuthInput{Type: "bearer", Token: "tok"}}, hubapi.Actor{Type: "user", Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	rt := &RegistryTargets{Registry: reg, Bindings: memBindings{"ro-spoke-2": {DatasourceID: ds.ID, ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu1"}}}
	targets, err := rt.Targets(context.Background())
	if err != nil || len(targets) != 1 || targets[0].Cluster != "ro-spoke-2" || targets[0].BaseURL != rs.URL {
		t.Fatalf("targets %v %+v", err, targets)
	}
	nodes, err := (&Discovery{VMURL: rs.URL, ClusterRegex: "ro-spoke-2", Targets: rt}).Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Cluster != "ro-spoke-2" {
		t.Fatalf("nodes %v %+v", err, nodes)
	}
	var authed bool
	for _, a := range remote.auth {
		authed = authed || a == "Bearer tok"
	}
	if !authed {
		t.Errorf("datasource auth not used: %v", remote.auth)
	}

	bad := &RegistryTargets{Registry: reg, Bindings: memBindings{"x": {DatasourceID: "missing", ClusterLabel: "cluster"}}}
	if _, err := bad.Targets(context.Background()); err == nil {
		t.Error("missing datasource accepted")
	}
}
