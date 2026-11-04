package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Node is a node discovered from kube-state-metrics series in VictoriaMetrics.
type Node struct {
	Cluster      string
	Name         string
	InstanceType string
	CapacityType CapacityType
	Region       string
	Zone         string
	// Allocatable capacity; 0 when the series is missing.
	AllocatableCores float64
	AllocatableGiB   float64
}

// Discovery lists nodes of every spoke from the hub's VictoriaMetrics (kube_node_labels and
// kube_node_status_allocatable, see docs/contracts/metrics.md). It needs no kube credentials (D-004).
type Discovery struct {
	// VMURL is the Prometheus-compatible query base URL, e.g. http://victoria-metrics.monitoring.svc:8428.
	VMURL string
	// ClusterRegex limits the clusters priced (PromQL regex on the cluster label); empty = all.
	ClusterRegex string
	// DefaultRegion is used for nodes without a region or zone label.
	DefaultRegion string
	// Lookback is how long a node is still priced after its last sample (last_over_time window).
	// 0 = DefaultNodeLookback. Larger values keep backfilled (synthetic) nodes priced.
	Lookback time.Duration
	HTTP     *http.Client
}

const gib = 1 << 30

// DefaultNodeLookback covers a few missed scrapes without pricing long-gone nodes.
const DefaultNodeLookback = 10 * time.Minute

// Nodes returns the nodes currently reported, sorted by cluster and name. Nodes with an unknown
// capacity type are returned with an empty CapacityType so the caller can count them.
func (d *Discovery) Nodes(ctx context.Context) ([]Node, error) {
	sel := `cluster!=""`
	if d.ClusterRegex != "" {
		sel = `cluster=~` + strconv.Quote(d.ClusterRegex)
	}
	lookback := d.Lookback
	if lookback <= 0 {
		lookback = DefaultNodeLookback
	}
	window := "[" + strconv.FormatInt(int64(lookback/time.Second), 10) + "s]"
	labelsQ := `max by (cluster, node, label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, ` +
		`label_eks_amazonaws_com_capacity_type, label_topology_kubernetes_io_region, label_topology_kubernetes_io_zone) ` +
		`(last_over_time(kube_node_labels{` + sel + `}` + window + `))`
	allocQ := `max by (cluster, node, resource) (last_over_time(kube_node_status_allocatable{` + sel + `, resource=~"cpu|memory"}` + window + `))`

	labelRows, err := d.query(ctx, labelsQ)
	if err != nil {
		return nil, err
	}
	allocRows, err := d.query(ctx, allocQ)
	if err != nil {
		return nil, err
	}

	type id struct{ cluster, node string }
	nodes := map[id]*Node{}
	for _, r := range labelRows {
		m := r.Metric
		n := &Node{
			Cluster:      m["cluster"],
			Name:         m["node"],
			InstanceType: m["label_node_kubernetes_io_instance_type"],
			Region:       m["label_topology_kubernetes_io_region"],
			Zone:         m["label_topology_kubernetes_io_zone"],
		}
		ct := m["label_karpenter_sh_capacity_type"]
		if ct == "" {
			ct = m["label_eks_amazonaws_com_capacity_type"]
		}
		if c, err := ParseCapacityType(ct); err == nil {
			n.CapacityType = c
		}
		if n.Region == "" && len(n.Zone) > 1 {
			n.Region = strings.TrimRight(n.Zone, "abcdefghijklmnopqrstuvwxyz") // eu-west-1a -> eu-west-1
		}
		if n.Region == "" {
			n.Region = d.DefaultRegion
		}
		nodes[id{n.Cluster, n.Name}] = n
	}
	for _, r := range allocRows {
		n, ok := nodes[id{r.Metric["cluster"], r.Metric["node"]}]
		if !ok {
			continue
		}
		switch r.Metric["resource"] {
		case "cpu":
			n.AllocatableCores = r.Value
		case "memory":
			n.AllocatableGiB = r.Value / gib
		}
	}

	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type sample struct {
	Metric map[string]string
	Value  float64
}

func (d *Discovery) query(ctx context.Context, q string) ([]sample, error) {
	u := strings.TrimRight(d.VMURL, "/") + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query victoria-metrics: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("query victoria-metrics: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query victoria-metrics: HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode victoria-metrics response: %w", err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("victoria-metrics query failed: %s", r.Error)
	}
	if r.Data.ResultType != "vector" {
		return nil, fmt.Errorf("victoria-metrics: want vector result, got %q", r.Data.ResultType)
	}
	out := make([]sample, 0, len(r.Data.Result))
	for _, res := range r.Data.Result {
		s, _ := res.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("victoria-metrics: bad sample value %v", res.Value[1])
		}
		out = append(out, sample{Metric: res.Metric, Value: v})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
