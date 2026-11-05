package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/tools/synth/synth"
)

// verify checks with PromQL that a backfilled scenario is in VictoriaMetrics with the expected
// shape. It never relies on the convenience workload label: workloads are matched by the KSM owner
// chain or by pod name prefix.
func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	var c common
	c.register(fs, "tools/synth/scenarios/fleet-default.yaml")
	vmURL := fs.String("vm-url", "http://localhost:18428", "VictoriaMetrics base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := c.generator()
	if err != nil {
		return err
	}
	q := &prom{base: strings.TrimRight(*vmURL, "/"), client: &http.Client{Timeout: 2 * time.Minute}}
	// The data ends at the backfill time, not now: evaluate at the newest sample.
	end, err := q.scalar(`max(tlast_over_time(kube_node_labels{cluster=~"synth-.*"}[35d]))`, time.Now())
	if err != nil {
		return fmt.Errorf("looking for synthetic data: %w", err)
	}
	if math.IsNaN(end) {
		return errors.New("no synthetic data found in VictoriaMetrics")
	}
	at := time.Unix(int64(end), 0)
	days := g.Scenario.Days
	fmt.Printf("evaluating at %s (newest synthetic sample)\n", at.UTC().Format(time.RFC3339))
	failed := 0
	check := func(name string, ok bool, detail string) {
		status := "PASS"
		if !ok {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s  %-70s %s\n", status, name, detail)
	}
	val := func(expr string) float64 {
		v, err := q.scalar(expr, at)
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", expr, err)
			return math.NaN()
		}
		return v
	}
	for _, cm := range g.Clusters {
		cl := cm.Spec.Name
		kinds := map[string]int{}
		for _, w := range cm.Workloads {
			kinds[w.Kind]++
		}
		fmt.Printf("\n== %s\n", cl)
		n := val(fmt.Sprintf(`count(kube_node_labels{cluster=%q})`, cl))
		check("nodes (kube_node_labels)", n == float64(len(cm.Nodes)), fmt.Sprintf("%v, want %d", n, len(cm.Nodes)))
		n = val(fmt.Sprintf(`count(count by (label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type) (kube_node_labels{cluster=%q, label_topology_kubernetes_io_region="eu-west-1"}))`, cl))
		check("instance type/capacity type combinations", n == float64(len(cm.Spec.NodePools)), fmt.Sprintf("%v", n))
		n = val(fmt.Sprintf(`count(kube_node_status_allocatable{cluster=%q, resource="cpu", unit="core"})`, cl))
		check("kube_node_status_allocatable cpu per node", n == float64(len(cm.Nodes)), fmt.Sprintf("%v", n))
		n = val(fmt.Sprintf(`count(kube_deployment_spec_replicas{cluster=%q})`, cl))
		check("Deployments (kube_deployment_spec_replicas)", n == float64(kinds[synth.KindDeployment]), fmt.Sprintf("%v, want %d", n, kinds[synth.KindDeployment]))
		if k := kinds[synth.KindStatefulSet]; k > 0 {
			n = val(fmt.Sprintf(`count(kube_statefulset_replicas{cluster=%q})`, cl))
			check("StatefulSets", n == float64(k), fmt.Sprintf("%v, want %d", n, k))
		}
		if k := kinds[synth.KindDaemonSet]; k > 0 {
			n = val(fmt.Sprintf(`count(kube_daemonset_status_desired_number_scheduled{cluster=%q})`, cl))
			check("DaemonSets", n == float64(k), fmt.Sprintf("%v, want %d", n, k))
		}
		if k := kinds[synth.KindCronJob]; k > 0 {
			n = val(fmt.Sprintf(`count(count by (namespace, owner_name) (last_over_time(kube_job_owner{cluster=%q}[%dd])))`, cl, int(days)))
			check("CronJobs with runs (kube_job_owner)", n == float64(k), fmt.Sprintf("%v, want %d", n, k))
		}
		// Owner chain: running Deployment pods resolve to a Deployment.
		n = val(fmt.Sprintf(`count(count by (namespace, workload) (`+deployJoin+`))`, cl, cl))
		want := 0
		for _, w := range cm.Workloads {
			if w.Kind == synth.KindDeployment {
				want++
			}
		}
		check("Deployments reachable via kube_pod_owner→kube_replicaset_owner", n == float64(want), fmt.Sprintf("%v, want %d", n, want))
		n = val(fmt.Sprintf(`count(max by (namespace, pod) (container_memory_working_set_bytes{cluster=%q, container!=""}) unless on (namespace, pod) kube_pod_owner{cluster=%q})`, cl, cl))
		check("every pod with cAdvisor data has kube_pod_owner", math.IsNaN(n), fmt.Sprintf("orphans: %v", nanZero(n)))
		n = val(fmt.Sprintf(`count(container_cpu_usage_seconds_total{cluster=%q, container!=""} unless on (namespace, pod, container) kube_pod_container_resource_requests{cluster=%q, resource="cpu"})`, cl, cl))
		check("every container has CPU requests (kube_pod_container_resource_requests)", math.IsNaN(n), fmt.Sprintf("missing: %v", nanZero(n)))
		cov := val(fmt.Sprintf(`min(count_over_time(kube_node_labels{cluster=%q}[%dd])) / %f`, cl, int(math.Ceil(days)), days*24*60/g.Timeline.Step.Minutes()))
		check(fmt.Sprintf("history covers %.0f days", days), cov > 0.99, fmt.Sprintf("coverage %.3f", cov))
		if !g.Scenario.WorkloadLabel {
			n = val(fmt.Sprintf(`count(container_cpu_usage_seconds_total{cluster=%q, workload!=""})`, cl))
			check("no workload label on cAdvisor series", math.IsNaN(n), fmt.Sprintf("%v", nanZero(n)))
		}
		if *g.Scenario.GroundTruth {
			n = val(fmt.Sprintf(`count(synth_workload_info{cluster=%q})`, cl))
			check("synth_workload_info per workload", n == float64(len(cm.Workloads)), fmt.Sprintf("%v", n))
		}
		for _, w := range cm.Spec.Workloads {
			verifyWorkload(cl, &w, days, val, check)
		}
	}
	fmt.Printf("\n%d checks failed\n", failed)
	if failed > 0 {
		return fmt.Errorf("%d checks failed", failed)
	}
	return nil
}

const deployJoin = `max by (namespace, pod, workload) (
  label_replace(kube_pod_owner{cluster=%q, owner_kind="ReplicaSet"}, "replicaset", "$1", "owner_name", "(.*)")
  * on (namespace, replicaset) group_left (workload)
  label_replace(kube_replicaset_owner{cluster=%q, owner_kind="Deployment"}, "workload", "$1", "owner_name", "(.*)"))`

func nanZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

// verifyWorkload checks an explicit workload's expectation with PromQL over its pods.
func verifyWorkload(cl string, w *synth.WorkloadSpec, days float64, val func(string) float64, check func(string, bool, string)) {
	if w.Expect == nil || len(w.Containers) == 0 {
		return
	}
	c := w.Containers[0]
	sel := fmt.Sprintf(`cluster=%q, namespace=%q, pod=~%q, container=%q`, cl, w.Namespace, w.Name+"-.*", c.Name)
	win := fmt.Sprintf("%dd", int(days))
	name := func(s string) string { return fmt.Sprintf("%s/%s: %s", w.Namespace, w.Name, s) }
	p95 := val(fmt.Sprintf(`max(quantile_over_time(0.95, max(rate(container_cpu_usage_seconds_total{%s}[5m]))[%s:5m]))`, sel, win))
	req := float64(c.Requests.CPU)
	switch w.Expect.CPU {
	case synth.ExpectDecrease:
		check(name("CPU p95 well below request"), p95 < 0.7*req, fmt.Sprintf("p95 %.3f, request %.3f", p95, req))
	case synth.ExpectKeep:
		check(name("CPU p95 near request"), p95 > 0.5*req && p95 < 1.1*req, fmt.Sprintf("p95 %.3f, request %.3f", p95, req))
	case synth.ExpectIncrease:
		thr := val(fmt.Sprintf(`max(quantile_over_time(0.95, (max(rate(container_cpu_cfs_throttled_periods_total{%s}[5m])) / max(rate(container_cpu_cfs_periods_total{%s}[5m])))[%s:5m]))`, sel, sel, win))
		check(name("CFS throttling ratio p95 > 0.25"), thr > 0.25, fmt.Sprintf("%.2f", thr))
	}
	for _, f := range w.Expect.RiskFlags {
		switch f {
		case "oom-killed":
			n := val(fmt.Sprintf(`count(last_over_time(kube_pod_container_status_last_terminated_reason{%s, reason="OOMKilled"}[%s]))`, sel, win))
			check(name("OOMKilled reasons present"), n > 0, fmt.Sprintf("%v pods", nanZero(n)))
		case "restarting":
			n := val(fmt.Sprintf(`sum(max_over_time(kube_pod_container_status_restarts_total{%s}[%s]))`, sel, win))
			check(name("container restarts"), n > 0, fmt.Sprintf("%v", nanZero(n)))
		case "hpa-coupled":
			n := val(fmt.Sprintf(`kube_horizontalpodautoscaler_spec_target_metric{cluster=%q, namespace=%q, horizontalpodautoscaler=%q, metric_target_type="utilization"}`, cl, w.Namespace, w.Name))
			rmax := val(fmt.Sprintf(`max_over_time(kube_horizontalpodautoscaler_status_current_replicas{cluster=%q, namespace=%q, horizontalpodautoscaler=%q}[%s])`, cl, w.Namespace, w.Name, win))
			check(name("HPA target metric, replicas scale"), !math.IsNaN(n) && rmax > float64(w.HPA.Min), fmt.Sprintf("target %v%%, max replicas %v", n, rmax))
		case "short-lived":
			n := val(fmt.Sprintf(`count(count by (pod) (last_over_time(kube_pod_owner{cluster=%q, namespace=%q, pod=~%q, owner_kind="Job"}[%s])))`, cl, w.Namespace, w.Name+"-.*", win))
			check(name("one pod per Job run"), n > days, fmt.Sprintf("%v pods", nanZero(n)))
		case "sidecar":
			n := val(fmt.Sprintf(`count(kube_pod_container_info{cluster=%q, namespace=%q, pod=~%q, container!=%q})`, cl, w.Namespace, w.Name+"-.*", c.Name))
			check(name("sidecar container present"), n > 0, fmt.Sprintf("%v", nanZero(n)))
		case "low-confidence":
			age := val(fmt.Sprintf(`time() - min(min_over_time(timestamp(kube_pod_owner{cluster=%q, namespace=%q, pod=~%q})[%s:10m]))`, cl, w.Namespace, w.Name+"-.*", win))
			check(name("first sample recent"), age < 4*86400, fmt.Sprintf("%.1fh", age/3600))
		}
	}
}

type prom struct {
	base   string
	client *http.Client
}

func (p *prom) post(form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.base+"/api/v1/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return p.client.Do(req)
}

// label returns label `name` of the single series an instant query returns.
func (p *prom) label(expr, name string, at time.Time) (string, error) {
	form := url.Values{"query": {expr}, "time": {strconv.FormatInt(at.Unix(), 10)}}
	resp, err := p.post(form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if len(r.Data.Result) != 1 {
		return "", fmt.Errorf("%s: %d series, want 1", expr, len(r.Data.Result))
	}
	return r.Data.Result[0].Metric[name], nil
}

// scalar evaluates an instant query and returns the single value (NaN for an empty result).
func (p *prom) scalar(expr string, at time.Time) (float64, error) {
	form := url.Values{"query": {expr}, "time": {strconv.FormatInt(at.Unix(), 10)}}
	resp, err := p.post(form)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, fmt.Errorf("%s: %s", resp.Status, b)
	}
	if r.Status != "success" {
		return 0, fmt.Errorf("%s", r.Error)
	}
	var vec []struct {
		Value [2]any `json:"value"`
	}
	if r.Data.ResultType == "scalar" {
		var v [2]any
		if err := json.Unmarshal(r.Data.Result, &v); err != nil {
			return 0, err
		}
		vec = append(vec, struct {
			Value [2]any `json:"value"`
		}{v})
	} else if err := json.Unmarshal(r.Data.Result, &vec); err != nil {
		return 0, err
	}
	if len(vec) == 0 {
		return math.NaN(), nil
	}
	if len(vec) > 1 {
		return 0, fmt.Errorf("query returned %d series, want 1", len(vec))
	}
	s, _ := vec[0].Value[1].(string)
	return strconv.ParseFloat(s, 64)
}
