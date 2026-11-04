package pricing

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func TestHubTargets(t *testing.T) {
	// the datasource behind the hub's proxy
	remote := &recordingVM{
		labels: func(q string) []string {
			return []string{labelRow("ro-spoke-2", "p1", "c6a.2xlarge", "spot", "eu-west-1", "eu-west-1b")}
		},
		alloc: func(string) []string { return nil },
	}
	rs := remote.server(t)
	defer rs.Close()
	var auth []string
	var paths []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		paths = append(paths, r.URL.Path)
		switch {
		case r.URL.Path == "/v1/internal/metrics-bindings":
			fmt.Fprint(w, `{"items":[{"cluster":"ro-spoke-1","binding":{"datasourceId":"local","clusterLabel":"cluster","clusterLabelValue":"ro-spoke-1"},"bound":false},`+
				`{"cluster":"ro-spoke-2","binding":{"datasourceId":"ds-1","clusterLabel":"k8s_cluster","clusterLabelValue":"prod-eu1"},"bound":true}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/internal/datasources/ds-1/api/v1/query"):
			res, err := http.Get(rs.URL + "/api/v1/query?" + r.URL.RawQuery)
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()
	ht := &HubTargets{HubURL: hub.URL + "/", Token: func() (string, error) { return "pricing-tok", nil }}
	targets, err := ht.Targets(context.Background())
	if err != nil || len(targets) != 1 || targets[0].Cluster != "ro-spoke-2" || targets[0].BaseURL != hub.URL+"/v1/internal/datasources/ds-1" {
		t.Fatalf("targets %v %+v", err, targets)
	}
	nodes, err := (&Discovery{VMURL: rs.URL, ClusterRegex: "ro-spoke-2", Targets: ht}).Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Cluster != "ro-spoke-2" {
		t.Fatalf("nodes %v %+v", err, nodes)
	}
	for i, a := range auth {
		if a != "Bearer pricing-tok" {
			t.Errorf("request %s without the pricing token: %q", paths[i], a)
		}
	}
	for _, q := range remote.queries {
		if strings.Contains(q, "label_replace") && !strings.Contains(q, `k8s_cluster="prod-eu1"`) {
			t.Errorf("proxied query without the binding: %s", q)
		}
	}

	bad := &HubTargets{HubURL: hub.URL + "/nope", Token: func() (string, error) { return "x", nil }}
	if _, err := bad.Targets(context.Background()); err == nil {
		t.Error("404 accepted")
	}
	noTok := &HubTargets{HubURL: hub.URL, Token: func() (string, error) { return "", errors.New("no file") }}
	if _, err := noTok.Targets(context.Background()); err == nil {
		t.Error("token error ignored")
	}
}
