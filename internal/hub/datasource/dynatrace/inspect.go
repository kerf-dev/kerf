package dynatrace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Probe runs a query without data access: it checks the URL, TLS, the token and the network.
func (s *Source) Probe(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	_, err := s.run(ctx, "data record(ok = 1)")
	return time.Since(start), err
}

// SeriesDims returns the dimension values of every series of key with samples in [from, to)
// (cluster "" = the whole environment).
func (s *Source) SeriesDims(ctx context.Context, key string, by []string, cluster string, from, to time.Time) ([]map[string]string, error) {
	res, err := s.run(ctx, fmt.Sprintf("timeseries n = count(%s, scalar: true), by: %s%s%s\n| fields %s",
		key, dims(by), filter(cluster), window(from, to), strings.Join(by, ", ")))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, len(res.Records))
	for _, rec := range res.Records {
		m := make(map[string]string, len(by))
		for _, d := range by {
			m[d] = str(rec, d)
		}
		out = append(out, m)
	}
	return out, nil
}

// ClusterNames returns the k8s.cluster.name values with container metrics in the last hour.
func (s *Source) ClusterNames(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.SeriesDims(ctx, KeyMemoryWorkingSet, []string{DimCluster}, "", now.Add(-time.Hour), now)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if c := r[DimCluster]; c != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Aggregate returns one series of key aggregated over the cluster (agg max, avg, count …) with
// the given bucket size, and the unit the API reports.
func (s *Source) Aggregate(ctx context.Context, key, agg, cluster string, from, to time.Time, step time.Duration) ([]recommender.Sample, string, error) {
	res, err := s.run(ctx, fmt.Sprintf("timeseries v = %s(%s)%s%s, interval: %s", agg, key, filter(cluster), window(from, to), dqlDuration(step)))
	if err != nil {
		return nil, "", err
	}
	var out []recommender.Sample
	for _, rec := range res.Records {
		sm, err := series(rec, "v", 1)
		if err != nil {
			return nil, "", err
		}
		out = append(out, sm...)
	}
	return mergeMax(out), res.Units[key], nil
}

// Node is a node of the cluster with the labels pricing reads and its allocatable capacity.
type Node = hubapi.NodeInventoryItem

// Node label keys pricing reads (both capacity-type labels).
const (
	LabelInstanceType      = "node.kubernetes.io/instance-type"
	LabelCapacityKarpenter = "karpenter.sh/capacity-type"
	LabelCapacityEKS       = "eks.amazonaws.com/capacityType"
	LabelZone              = "topology.kubernetes.io/zone"
	LabelRegion            = "topology.kubernetes.io/region"
)

// NodeLabelsQuery reads the labels of the cluster's nodes from Smartscape: the node's
// Kubernetes object, and the tags:k8s.labels field where the environment has it.
func NodeLabelsQuery(cluster string) string {
	q := "smartscapeNodes K8S_NODE"
	if cluster != "" {
		q += "\n| filter " + DimCluster + " == " + dqlString(cluster)
	}
	return q + "\n| parse k8s.object, \"JSON:config\"\n| fields " + DimNode + ", labels = config[metadata][labels], tagLabels = `tags:k8s.labels`"
}

// labelsOf reads a labels field: a JSON object, or an array of "key=value" / "key:value" strings.
func labelsOf(raw json.RawMessage, into map[string]string) {
	if len(raw) == 0 {
		return
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		for k, v := range obj {
			if s, ok := v.(string); ok && into[k] == "" {
				into[k] = s
			}
		}
		return
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && strings.HasPrefix(strings.TrimSpace(s), "{") {
		labelsOf(json.RawMessage(s), into)
		return
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, kv := range arr {
			i := strings.IndexAny(kv, "=:")
			// keys contain ':'? Kubernetes label keys never do; '=' wins when both are present.
			if j := strings.Index(kv, "="); j > 0 {
				i = j
			}
			if i > 0 && into[kv[:i]] == "" {
				into[kv[:i]] = kv[i+1:]
			}
		}
	}
}

// Nodes returns the cluster's nodes with labels (Smartscape) and allocatable CPU and memory
// (last value in the hour before now), sorted by name.
func (s *Source) Nodes(ctx context.Context, cluster string, now time.Time) ([]Node, error) {
	var labelRows []map[string]json.RawMessage
	alloc := map[string]map[string]float64{} // node -> key -> value
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		res, err := s.run(gctx, NodeLabelsQuery(cluster))
		if err != nil {
			return fmt.Errorf("node labels: %w", err)
		}
		labelRows = res.Records
		return nil
	})
	type allocResult struct {
		key  string
		rows map[string]float64
	}
	results := make([]allocResult, 2)
	for i, k := range []struct {
		key  string
		kind unitKind
	}{{KeyNodeCPUAlloc, unitCPU}, {KeyNodeMemoryAlloc, unitBytes}} {
		g.Go(func() error {
			res, err := s.run(gctx, fmt.Sprintf("timeseries v = max(%s), by: {%s}%s%s, interval: 10m", k.key, DimNode, filter(cluster), window(now.Add(-time.Hour), now)))
			if err != nil {
				return fmt.Errorf("%s: %w", k.key, err)
			}
			f := s.unitScale(gctx, res, k.key, k.kind)
			rows := map[string]float64{}
			for _, rec := range res.Records {
				sm, err := series(rec, "v", f)
				if err != nil {
					return err
				}
				if n := str(rec, DimNode); n != "" && len(sm) > 0 {
					rows[n] = sm[len(sm)-1].V
				}
			}
			results[i] = allocResult{k.key, rows}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for _, r := range results {
		for n, v := range r.rows {
			if alloc[n] == nil {
				alloc[n] = map[string]float64{}
			}
			alloc[n][r.key] = v
		}
	}
	nodes := map[string]*Node{}
	for _, rec := range labelRows {
		name := str(rec, DimNode)
		if name == "" {
			continue
		}
		l := map[string]string{}
		labelsOf(rec["labels"], l)
		labelsOf(rec["tagLabels"], l)
		n := &Node{Node: name, InstanceType: l[LabelInstanceType], CapacityType: l[LabelCapacityKarpenter], Zone: l[LabelZone], Region: l[LabelRegion]}
		if n.CapacityType == "" {
			n.CapacityType = l[LabelCapacityEKS]
		}
		nodes[name] = n
	}
	for name, a := range alloc {
		n := nodes[name]
		if n == nil {
			// allocatable without a Smartscape node: keep it, its labels are unknown
			n = &Node{Node: name}
			nodes[name] = n
		}
		n.AllocatableCores, n.AllocatableMemoryBytes = a[KeyNodeCPUAlloc], a[KeyNodeMemoryAlloc]
	}
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// Counts are what the mapping table found for a cluster (or the environment).
type Counts struct {
	// Series per metric key (container series, or node series for node keys).
	Series map[string]int
	// Pods are the pods with memory series; OwnedPods those whose workload Kerf rightsizes.
	Pods, OwnedPods int
	// Nodes are the Smartscape nodes; LabelledNodes those with an instance type.
	Nodes, LabelledNodes int
	// Errors per mapping input that could not be checked.
	Errors map[string]string
}

// eventKeys are counted over a day: they only have series while restarts or OOM kills happen.
var eventKeys = map[string]bool{KeyRestarts: true, KeyOOMKills: true}

// Count counts the series of every mapped key (the last hour; the last day for restarts and
// OOM kills), the pods with a resolved workload and the labelled nodes. A failing query is
// recorded in Errors; ctx errors are returned.
func (s *Source) Count(ctx context.Context, cluster string, now time.Time) (Counts, error) {
	c := Counts{Series: map[string]int{}, Errors: map[string]string{}}
	var keys []string
	seen := map[string]bool{}
	for _, in := range Inputs {
		for _, k := range in.Keys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	type out struct {
		key  string
		n    int
		err  error
		pods [2]int
	}
	results := make([]out, len(keys)+2)
	g, gctx := errgroup.WithContext(ctx)
	for i, k := range keys {
		by := containerDims
		if k == KeyNodeCPUAlloc || k == KeyNodeMemoryAlloc {
			by = []string{DimNode}
		}
		from := now.Add(-time.Hour)
		if eventKeys[k] {
			from = now.Add(-24 * time.Hour)
		}
		g.Go(func() error {
			rows, err := s.SeriesDims(gctx, k, by, cluster, from, now)
			results[i] = out{key: k, n: len(rows), err: err}
			return gctx.Err()
		})
	}
	g.Go(func() error {
		rows, err := s.SeriesDims(gctx, KeyMemoryWorkingSet, []string{DimNamespace, DimPod, DimKind, DimWorkload}, cluster, now.Add(-time.Hour), now)
		r := out{key: "owners", err: err}
		pods, owned := map[string]bool{}, map[string]bool{}
		for _, row := range rows {
			p := row[DimNamespace] + "/" + row[DimPod]
			pods[p] = true
			if kerfKind(row[DimKind]) != "" && row[DimWorkload] != "" {
				owned[p] = true
			}
		}
		r.pods = [2]int{len(pods), len(owned)}
		results[len(keys)] = r
		return gctx.Err()
	})
	g.Go(func() error {
		res, err := s.run(gctx, NodeLabelsQuery(cluster))
		r := out{key: "nodes", err: err}
		if err == nil {
			for _, rec := range res.Records {
				l := map[string]string{}
				labelsOf(rec["labels"], l)
				labelsOf(rec["tagLabels"], l)
				r.pods[0]++
				if l[LabelInstanceType] != "" {
					r.pods[1]++
				}
			}
		}
		results[len(keys)+1] = r
		return gctx.Err()
	})
	if err := g.Wait(); err != nil {
		return c, err
	}
	for _, r := range results {
		if r.err != nil {
			c.Errors[r.key] = r.err.Error()
			continue
		}
		switch r.key {
		case "owners":
			c.Pods, c.OwnedPods = r.pods[0], r.pods[1]
		case "nodes":
			c.Nodes, c.LabelledNodes = r.pods[0], r.pods[1]
		default:
			c.Series[r.key] = r.n
		}
	}
	return c, nil
}

// Statuses turns counts into the mapping table of the Test result.
func (c Counts) Statuses() []hubapi.DatasourceMetricStatus {
	out := make([]hubapi.DatasourceMetricStatus, 0, len(Inputs))
	for _, in := range Inputs {
		switch {
		case in.Source == "":
			out = append(out, metricStatus(in, 0, false))
		case in.Source == SmartscapeNodeLabels:
			st := metricStatus(in, c.Nodes, c.LabelledNodes > 0)
			if e := c.Errors["nodes"]; e != "" {
				st.Note = "not checked: " + e
			}
			out = append(out, st)
		case len(in.Keys) == 0:
			st := metricStatus(in, c.OwnedPods, c.OwnedPods > 0)
			if e := c.Errors["owners"]; e != "" {
				st.Note = "not checked: " + e
			}
			out = append(out, st)
		default:
			n, found := 0, true
			var errs []string
			for _, k := range in.Keys {
				if e := c.Errors[k]; e != "" {
					errs = append(errs, e)
				}
				n += c.Series[k]
				if c.Series[k] == 0 {
					found = false
				}
			}
			st := metricStatus(in, n, found)
			if len(errs) > 0 {
				st.Status, st.Note = hubapi.MetricMissing, "not checked: "+errs[0]
			}
			out = append(out, st)
		}
	}
	return out
}

// Missing lists the source metrics of the inputs with status missing.
func Missing(st []hubapi.DatasourceMetricStatus) []string {
	var out []string
	for _, s := range st {
		if s.Status == hubapi.MetricMissing {
			out = append(out, s.SourceMetric)
		}
	}
	return out
}
