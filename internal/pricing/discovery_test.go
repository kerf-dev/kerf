package pricing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeVM answers the two discovery queries with canned vectors.
func fakeVM(t *testing.T, labels, alloc string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("query")
		var result string
		switch {
		case strings.Contains(q, "kube_node_labels"):
			result = labels
		case strings.Contains(q, "kube_node_status_allocatable"):
			result = alloc
		default:
			t.Errorf("unexpected query %q", q)
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, result)
	}))
}

func labelRow(cluster, node, it, ct, region, zone string) string {
	return fmt.Sprintf(`{"metric":{"cluster":%q,"node":%q,"label_node_kubernetes_io_instance_type":%q,`+
		`"label_karpenter_sh_capacity_type":%q,"label_topology_kubernetes_io_region":%q,"label_topology_kubernetes_io_zone":%q},"value":[1,"1"]}`,
		cluster, node, it, ct, region, zone)
}

func allocRow(cluster, node, resource, v string) string {
	return fmt.Sprintf(`{"metric":{"cluster":%q,"node":%q,"resource":%q},"value":[1,%q]}`, cluster, node, resource, v)
}

func TestDiscoveryNodes(t *testing.T) {
	labels := strings.Join([]string{
		labelRow("ro-spoke-2", "n2", "c6a.2xlarge", "spot", "eu-west-1", "eu-west-1b"),
		labelRow("ro-spoke-1", "n1", "m6i.xlarge", "on-demand", "eu-west-1", "eu-west-1a"),
		labelRow("synth-a", "s1", "m6i.xlarge", "", "", "eu-west-1c"), // region from zone, default capacity
		labelRow("synth-b", "s2", "c6a.2xlarge", "reserved", "", ""),  // default region, unknown capacity
		`{"metric":{"cluster":"eks","node":"e1","label_node_kubernetes_io_instance_type":"m5.large",` +
			`"label_eks_amazonaws_com_capacity_type":"SPOT"},"value":[1,"1"]}`, // EKS label
	}, ",")
	alloc := strings.Join([]string{
		allocRow("ro-spoke-1", "n1", "cpu", "3.92"),
		allocRow("ro-spoke-1", "n1", "memory", "16106127360"), // 15 GiB
		allocRow("ro-spoke-2", "n2", "cpu", "8"),
		allocRow("ro-spoke-2", "n2", "memory", "32212254720"),
		allocRow("ro-spoke-9", "gone", "cpu", "8"), // no labels: ignored
	}, ",")
	vm := fakeVM(t, labels, alloc)
	defer vm.Close()

	d := &Discovery{VMURL: vm.URL + "/", DefaultRegion: "eu-west-1"}
	got, err := d.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{Cluster: "eks", Name: "e1", InstanceType: "m5.large", CapacityType: Spot, Region: "eu-west-1"},
		{Cluster: "ro-spoke-1", Name: "n1", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", Zone: "eu-west-1a", AllocatableCores: 3.92, AllocatableGiB: 15},
		{Cluster: "ro-spoke-2", Name: "n2", InstanceType: "c6a.2xlarge", CapacityType: Spot, Region: "eu-west-1", Zone: "eu-west-1b", AllocatableCores: 8, AllocatableGiB: 30},
		{Cluster: "synth-a", Name: "s1", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", Zone: "eu-west-1c"},
		{Cluster: "synth-b", Name: "s2", InstanceType: "c6a.2xlarge", Region: "eu-west-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestDiscoveryClusterRegex(t *testing.T) {
	var queries []string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer vm.Close()
	d := &Discovery{VMURL: vm.URL, ClusterRegex: "ro-spoke-.*|synth-.*", Lookback: 6 * time.Hour}
	if _, err := d.Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 3 {
		t.Fatalf("%d queries", len(queries))
	}
	for i, q := range queries {
		window := "[21600s]))"
		if i == 1 { // the node labels of the last few minutes
			window = "[300s]))"
		}
		if !strings.Contains(q, `cluster=~"ro-spoke-.*|synth-.*"`) || !strings.Contains(q, window) {
			t.Errorf("query %q lacks the cluster selector or window %s", q, window)
		}
	}
}

// TestDiscoveryCurrentNodeLabels: after a kube-state-metrics allowlist change a node has two
// kube_node_labels series in the lookback; the current one (seen in the last minutes) decides.
func TestDiscoveryCurrentNodeLabels(t *testing.T) {
	eks := func(node, ct string) string {
		return fmt.Sprintf(`{"metric":{"cluster":"lab","node":%q,"label_node_kubernetes_io_instance_type":"m8g.large",`+
			`"label_topology_kubernetes_io_zone":"eu-west-1a","label_eks_amazonaws_com_capacity_type":%q},"value":[1,"1"]}`, node, ct)
	}
	tests := []struct {
		name           string
		lookback       []string
		recent         []string
		want           map[string]CapacityType
		wantRecentCall bool
	}{
		{
			name:     "old series without capacity type, current with it",
			lookback: []string{eks("mng-1", ""), eks("mng-1", "ON_DEMAND"), eks("mng-2", "ON_DEMAND"), eks("mng-2", "")},
			recent:   []string{eks("mng-1", "ON_DEMAND"), eks("mng-2", "ON_DEMAND")},
			want:     map[string]CapacityType{"mng-1": OnDemand, "mng-2": OnDemand},
		},
		{
			name:     "current series lost the label: it decides (default capacity type)",
			lookback: []string{eks("n", "SPOT"), eks("n", "")},
			recent:   []string{eks("n", "")},
			want:     map[string]CapacityType{"n": OnDemand},
		},
		{
			name:     "a node gone from the recent window keeps its most complete series",
			lookback: []string{eks("gone", ""), eks("gone", "SPOT")},
			want:     map[string]CapacityType{"gone": Spot},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query().Get("query")
				var rows []string
				switch {
				case strings.Contains(q, "kube_node_labels") && strings.Contains(q, "[300s]"):
					rows = tt.recent
				case strings.Contains(q, "kube_node_labels"):
					rows = tt.lookback
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(rows, ","))
			}))
			defer vm.Close()
			got, err := (&Discovery{VMURL: vm.URL, DefaultRegion: "eu-west-1"}).Nodes(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v", got)
			}
			for _, n := range got {
				if want, ok := tt.want[n.Name]; !ok || n.CapacityType != want {
					t.Errorf("%s: capacity type %q, want %q", n.Name, n.CapacityType, want)
				}
			}
		})
	}
}

func TestDiscoveryErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		errText string
	}{
		{"http error", http.StatusBadRequest, `{"status":"error","error":"bad query"}`, "HTTP 400"},
		{"query error", http.StatusOK, `{"status":"error","error":"bad query"}`, "bad query"},
		{"not json", http.StatusOK, `<html>`, "decode"},
		{"matrix", http.StatusOK, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, "want vector"},
		{"bad value", http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"x"]}]}}`, "bad sample"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer vm.Close()
			_, err := (&Discovery{VMURL: vm.URL}).Nodes(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.errText) {
				t.Fatalf("err = %v, want containing %q", err, tt.errText)
			}
		})
	}
	if _, err := (&Discovery{VMURL: "http://127.0.0.1:1"}).Nodes(context.Background()); err == nil {
		t.Fatal("want connection error")
	}
	if got := truncate(strings.Repeat("x", 10), 4); got != "xxxx..." {
		t.Fatalf("truncate = %q", got)
	}
}
