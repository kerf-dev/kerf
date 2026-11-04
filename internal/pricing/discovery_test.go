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
	if len(queries) != 2 {
		t.Fatalf("%d queries", len(queries))
	}
	for _, q := range queries {
		if !strings.Contains(q, `cluster=~"ro-spoke-.*|synth-.*"`) || !strings.Contains(q, "[21600s]))") {
			t.Errorf("query %q lacks the cluster selector or lookback", q)
		}
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
