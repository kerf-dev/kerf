package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
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
// kube_node_status_allocatable, see docs/contracts/metrics.md). It needs no kube credentials.
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
	// Targets lists clusters with a metrics binding; nil = only VMURL.
	Targets TargetLister
	Log     *slog.Logger
}

// Target is a cluster whose node metrics are read from its bound datasource.
type Target struct {
	// Cluster is the registered cluster name.
	Cluster string
	BaseURL string
	HTTP    *http.Client
	Matcher datasource.Matcher
}

// TargetLister lists the bound clusters.
type TargetLister interface {
	Targets(ctx context.Context) ([]Target, error)
}

const gib = 1 << 30

// DefaultNodeLookback covers a few missed scrapes without pricing long-gone nodes.
const DefaultNodeLookback = 10 * time.Minute

// Nodes returns the nodes currently reported, sorted by cluster and name. Nodes with an unknown
// capacity type are returned with an empty CapacityType so the caller can count them.
//
// Every cluster label value in VMURL is discovered as it was before metrics bindings. Clusters with a metrics
// binding (Targets) are then read from their own datasource with their own matchers and
// re-labelled to the registered cluster name; they replace whatever VMURL had for that name. A
// failing target is logged and skipped.
func (d *Discovery) Nodes(ctx context.Context) ([]Node, error) {
	sel := `cluster!=""`
	if d.ClusterRegex != "" {
		sel = `cluster=~` + strconv.Quote(d.ClusterRegex)
	}
	nodes := map[nodeID]*Node{}
	if err := d.discover(ctx, d.VMURL, d.HTTP, []string{sel}, func(s string) string { return s }, nodes); err != nil {
		return nil, err
	}
	if d.Targets != nil {
		targets, err := d.Targets.Targets(ctx)
		if err != nil {
			return nil, fmt.Errorf("list metrics bindings: %w", err)
		}
		var re *regexp.Regexp
		if d.ClusterRegex != "" {
			if re, err = regexp.Compile("^(?:" + d.ClusterRegex + ")$"); err != nil {
				return nil, fmt.Errorf("cluster regex: %w", err)
			}
		}
		for _, t := range targets {
			if re != nil && !re.MatchString(t.Cluster) {
				continue
			}
			for id := range nodes {
				if id.cluster == t.Cluster {
					delete(nodes, id)
				}
			}
			found := map[nodeID]*Node{}
			if err := d.discover(ctx, t.BaseURL, t.HTTP, t.Matcher.Selectors(), t.Matcher.Relabel, found); err != nil {
				d.log().WarnContext(ctx, "node discovery failed for a bound cluster", "cluster", t.Cluster, "err", err)
				continue
			}
			for id, n := range found {
				if id.cluster == t.Cluster {
					nodes[id] = n
				}
			}
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

type nodeID struct{ cluster, node string }

func (d *Discovery) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// discover reads nodes from one datasource. sel are the label matchers of the series; rel wraps
// each raw vector (relabelling to the registered cluster name).
func (d *Discovery) discover(ctx context.Context, base string, hc *http.Client, sel []string, rel func(string) string, nodes map[nodeID]*Node) error {
	lookback := d.Lookback
	if lookback <= 0 {
		lookback = DefaultNodeLookback
	}
	window := "[" + strconv.FormatInt(int64(lookback/time.Second), 10) + "s]"
	labelsQ := `max by (cluster, node, label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, ` +
		`label_eks_amazonaws_com_capacity_type, label_topology_kubernetes_io_region, label_topology_kubernetes_io_zone) ` +
		`(` + rel(`last_over_time(kube_node_labels{`+strings.Join(sel, ", ")+`}`+window+`)`) + `)`
	allocQ := `max by (cluster, node, resource) (` + rel(`last_over_time(kube_node_status_allocatable{`+strings.Join(append(append([]string{}, sel...), `resource=~"cpu|memory"`), ", ")+`}`+window+`)`) + `)`

	labelRows, err := d.query(ctx, base, hc, labelsQ)
	if err != nil {
		return err
	}
	allocRows, err := d.query(ctx, base, hc, allocQ)
	if err != nil {
		return err
	}
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
		nodes[nodeID{n.Cluster, n.Name}] = n
	}
	for _, r := range allocRows {
		n, ok := nodes[nodeID{r.Metric["cluster"], r.Metric["node"]}]
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
	return nil
}

type sample struct {
	Metric map[string]string
	Value  float64
}

func (d *Discovery) query(ctx context.Context, base string, hc *http.Client, q string) ([]sample, error) {
	u := strings.TrimRight(base, "/") + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
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
