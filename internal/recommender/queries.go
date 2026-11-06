package recommender

import (
	"fmt"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
)

// Queries builds the canonical PromQL of the hub (docs/contracts/metrics.md, section 4). Every
// query is plain PromQL that runs on Prometheus and VictoriaMetrics (D-003).
type Queries struct {
	// Cluster restricts every selector to one cluster.
	Cluster string
	// RateWindow is the range of rate() and increase().
	RateWindow time.Duration
	// Binding selects the cluster's series in its datasource and relabels results to Cluster
	// (D-025). nil = label cluster = Cluster, the queries before D-025 (byte-identical).
	Binding *datasource.Matcher
}

// WorkloadLabels are the labels identifying a workload in query results.
const WorkloadLabels = "cluster, namespace, workload_kind, workload"

// Filter optionally narrows queries to one namespace (UI usage endpoint). Results are still
// grouped by workload; callers pick the workload they need.
type Filter struct {
	Namespace string
}

func (q Queries) matcher() datasource.Matcher {
	if q.Binding != nil {
		return *q.Binding
	}
	return datasource.DefaultMatcher(q.Cluster)
}

// sel is the selector of the cluster's series plus extra matchers.
func (q Queries) sel(extra ...string) string { return q.matcher().Sel(extra...) }

// rel relabels a raw instant vector to the registered cluster name (identity without a binding).
// Every raw vector goes through it before aggregation or joins.
func (q Queries) rel(expr string) string { return q.matcher().Relabel(expr) }

// containerSel filters real containers (no pod sandbox, no pod cgroup).
func (q Queries) containerSel(f *Filter) string {
	extra := []string{`container!=""`, `container!="POD"`}
	if f != nil && f.Namespace != "" {
		extra = append(extra, fmt.Sprintf("namespace=%q", f.Namespace))
	}
	return q.sel(extra...)
}

// PodWorkload maps pods to workloads: one series per pod, value 1, with labels
// cluster, namespace, pod, workload_kind, workload. wrap is applied to every raw selector
// (e.g. to add last_over_time for instant inventory queries); nil means no wrapping.
func (q Queries) PodWorkload(f *Filter, wrap func(string) string) string {
	if wrap == nil {
		wrap = func(s string) string { return s }
	}
	ns := ""
	if f != nil && f.Namespace != "" {
		ns = fmt.Sprintf("namespace=%q", f.Namespace)
	}
	s := func(extra ...string) string {
		if ns != "" {
			extra = append(extra, ns)
		}
		return q.sel(extra...)
	}
	dep := fmt.Sprintf(`label_replace(
  max by (cluster, namespace, pod, workload) (
    label_replace(%s, "replicaset", "$1", "owner_name", "(.*)")
    * on (cluster, namespace, replicaset) group_left (workload)
    max by (cluster, namespace, replicaset, workload) (
      label_replace(%s, "workload", "$1", "owner_name", "(.*)")
    )
  ),
  "workload_kind", "Deployment", "workload", ".*"
)`, q.rel(wrap("kube_pod_owner"+s(`owner_kind="ReplicaSet"`))), q.rel(wrap("kube_replicaset_owner"+s(`owner_kind="Deployment"`))))
	cron := fmt.Sprintf(`label_replace(
  max by (cluster, namespace, pod, workload) (
    label_replace(%s, "job_name", "$1", "owner_name", "(.*)")
    * on (cluster, namespace, job_name) group_left (workload)
    max by (cluster, namespace, job_name, workload) (
      label_replace(%s, "workload", "$1", "owner_name", "(.*)")
    )
  ),
  "workload_kind", "CronJob", "workload", ".*"
)`, q.rel(wrap("kube_pod_owner"+s(`owner_kind="Job"`))), q.rel(wrap("kube_job_owner"+s(`owner_kind="CronJob"`))))
	direct := fmt.Sprintf(`max by (cluster, namespace, pod, workload_kind, workload) (
  label_replace(
    label_replace(%s, "workload", "$1", "owner_name", "(.*)"),
    "workload_kind", "$1", "owner_kind", "(.*)"
  )
)`, q.rel(wrap("kube_pod_owner"+s(`owner_kind=~"StatefulSet|DaemonSet"`))))
	return "(\n" + dep + "\nor\n" + cron + "\nor\n" + direct + "\n)"
}

// join attaches workload labels to a per-pod expression and aggregates by workload and container.
func (q Queries) join(agg, perPod string, f *Filter) string {
	return fmt.Sprintf("%s by (%s, container) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		agg, WorkloadLabels, perPod, q.PodWorkload(f, nil))
}

// joinWith is join with a wrapped pod map (e.g. last_over_time for instant queries over a window).
func (q Queries) joinWith(agg, perPod string, wrap func(string) string) string {
	return fmt.Sprintf("%s by (%s, container) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		agg, WorkloadLabels, perPod, q.PodWorkload(nil, wrap))
}

// CPUPerPod is the CPU usage in cores per pod and container.
func (q Queries) CPUPerPod(f *Filter) string {
	return fmt.Sprintf("sum by (cluster, namespace, pod, container) (%s)",
		q.rel(fmt.Sprintf("rate(container_cpu_usage_seconds_total%s[%s])", q.containerSel(f), promDuration(q.RateWindow))))
}

// MemoryPerPod is the peak memory working set in bytes per pod and container within each step
// (max_over_time over the step catches peaks between steps and short-lived pods).
func (q Queries) MemoryPerPod(f *Filter, step time.Duration) string {
	return fmt.Sprintf("max by (cluster, namespace, pod, container) (%s)",
		q.rel(fmt.Sprintf("max_over_time(container_memory_working_set_bytes%s[%s])", q.containerSel(f), promDuration(step))))
}

// CPUUsage is the per-step CPU usage per workload and container; agg is max (what a per-pod
// request must cover), avg or sum.
func (q Queries) CPUUsage(agg string, f *Filter) string { return q.join(agg, q.CPUPerPod(f), f) }

// MemoryUsage is the per-step memory working set per workload and container.
func (q Queries) MemoryUsage(agg string, f *Filter, step time.Duration) string {
	return q.join(agg, q.MemoryPerPod(f, step), f)
}

// ThrottleRatio is the CFS throttling ratio per workload and container (max across pods).
func (q Queries) ThrottleRatio() string {
	r := promDuration(q.RateWindow)
	perPod := fmt.Sprintf("(\n  sum by (cluster, namespace, pod, container) (%s)\n  / clamp_min(sum by (cluster, namespace, pod, container) (%s), 1)\n)",
		q.rel(fmt.Sprintf("rate(container_cpu_cfs_throttled_periods_total%s[%s])", q.containerSel(nil), r)),
		q.rel(fmt.Sprintf("rate(container_cpu_cfs_periods_total%s[%s])", q.containerSel(nil), r)))
	return q.join("max", perPod, nil)
}

// OOMKills is > 0 for every container OOM-killed within window: the KSM last terminated reason
// was OOMKilled and the container restarted within the window, or the cAdvisor OOM counter grew.
// Pods are mapped with last_over_time(window), so pods deleted since are included. Instant query.
func (q Queries) OOMKills(window time.Duration) string {
	w := promDuration(window)
	perPod := fmt.Sprintf(`(
  (
    max by (cluster, namespace, pod, container) (%s)
    and on (cluster, namespace, pod, container)
    (max by (cluster, namespace, pod, container) (%s) > 0)
  )
  or
  (max by (cluster, namespace, pod, container) (%s) > 0)
)`, q.rel(fmt.Sprintf("max_over_time(kube_pod_container_status_last_terminated_reason%s[%s])", q.sel(`reason="OOMKilled"`), w)),
		q.rel(fmt.Sprintf("increase(kube_pod_container_status_restarts_total%s[%s])", q.sel(), w)),
		q.rel(fmt.Sprintf("increase(container_oom_events_total%s[%s])", q.containerSel(nil), w)))
	return q.joinWith("max", perPod, lastOver(window))
}

// Restarts is the number of container restarts within window, summed over pods. Instant query.
func (q Queries) Restarts(window time.Duration) string {
	perPod := fmt.Sprintf("sum by (cluster, namespace, pod, container) (%s)",
		q.rel(fmt.Sprintf("increase(kube_pod_container_status_restarts_total%s[%s])", q.sel(), promDuration(window))))
	return q.joinWith("sum", perPod, lastOver(window))
}

// RunningPods is the number of pods per workload that ran during each step.
func (q Queries) RunningPods(step time.Duration) string {
	return fmt.Sprintf("count by (%s) (\nmax by (cluster, namespace, pod) (%s)\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		WorkloadLabels, q.rel(fmt.Sprintf("max_over_time(container_memory_working_set_bytes%s[%s])", q.containerSel(nil), promDuration(step))), q.PodWorkload(nil, nil))
}

// Resources returns current requests (metric = "requests") or limits ("limits") per workload,
// container and resource; init selects the init-container series. lookback > 0 wraps every
// selector in last_over_time so workloads without a running pod (CronJobs, backfilled data)
// keep their values.
func (q Queries) Resources(metric string, init bool, lookback time.Duration, f *Filter) string {
	name := "kube_pod_container_resource_" + metric
	if init {
		name = "kube_pod_init_container_resource_" + metric
	}
	wrap := lastOver(lookback)
	extra := []string{`resource=~"cpu|memory"`}
	if f != nil && f.Namespace != "" {
		extra = append(extra, fmt.Sprintf("namespace=%q", f.Namespace))
	}
	perPod := fmt.Sprintf("max by (cluster, namespace, pod, container, resource) (%s)", q.rel(wrap(name+q.sel(extra...))))
	return fmt.Sprintf("max by (%s, container, resource) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		WorkloadLabels, perPod, q.PodWorkload(f, wrap))
}

// InitContainers lists init containers per workload.
func (q Queries) InitContainers(lookback time.Duration) string {
	wrap := lastOver(lookback)
	perPod := fmt.Sprintf("max by (cluster, namespace, pod, container) (%s)", q.rel(wrap("kube_pod_init_container_info"+q.sel())))
	return fmt.Sprintf("max by (%s, container) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		WorkloadLabels, perPod, q.PodWorkload(nil, wrap))
}

// HPAInfo returns HPAs with their scale target.
func (q Queries) HPAInfo(lookback time.Duration) string {
	return fmt.Sprintf("max by (cluster, namespace, horizontalpodautoscaler, scaletargetref_kind, scaletargetref_name) (%s)",
		q.rel(lastOver(lookback)("kube_horizontalpodautoscaler_info"+q.sel())))
}

// HPAMetrics returns the metrics each HPA scales on.
func (q Queries) HPAMetrics(lookback time.Duration) string {
	return fmt.Sprintf("max by (cluster, namespace, horizontalpodautoscaler, metric_name, metric_target_type) (%s)",
		q.rel(lastOver(lookback)("kube_horizontalpodautoscaler_spec_target_metric"+q.sel())))
}

// HPAReplicaBounds returns spec.minReplicas or spec.maxReplicas (bound "min" or "max") per HPA.
func (q Queries) HPAReplicaBounds(bound string, lookback time.Duration) string {
	return fmt.Sprintf("max by (cluster, namespace, horizontalpodautoscaler) (%s)",
		q.rel(lastOver(lookback)("kube_horizontalpodautoscaler_spec_"+bound+"_replicas"+q.sel())))
}

// HPAPeakReplicas returns the highest current replica count of each HPA over window (WO-11: an HPA
// at maxReplicas is not coordinated).
func (q Queries) HPAPeakReplicas(window time.Duration) string {
	return fmt.Sprintf("max by (cluster, namespace, horizontalpodautoscaler) (%s)",
		q.rel(fmt.Sprintf("max_over_time(%s[%s])", "kube_horizontalpodautoscaler_status_current_replicas"+q.sel(), promDuration(window))))
}

// Pricing returns the average hourly rate over the cluster's nodes and the node count. The pricing
// service exports its series to the hub's own metrics store (datasource local) with label cluster
// = the registered name, so these queries ignore the binding and must run against local.
func (q Queries) Pricing(metric string, lookback time.Duration) (avg, count string) {
	// max by node first: a pricing rollout briefly exposes two series per node (WO-03 hand-over).
	inner := fmt.Sprintf("max by (cluster, node) (%s)", lastOver(lookback)(metric+datasource.DefaultMatcher(q.Cluster).Sel()))
	return fmt.Sprintf("avg by (cluster) (%s)", inner), fmt.Sprintf("count by (cluster) (%s)", inner)
}

// PricingSourceLookback is the lookback of PricingSources: short, so a node whose rate source just
// changed is not counted under both sources for long.
const PricingSourceLookback = 10 * time.Minute

// PricingSources counts the cluster's priced nodes by rate source (label `source` of the pricing
// series: cur, list, spot or static).
func (q Queries) PricingSources() string {
	inner := fmt.Sprintf("max by (cluster, node, source) (%s)", lastOver(PricingSourceLookback)("kerf_cpu_core_hourly_cost_usd"+datasource.DefaultMatcher(q.Cluster).Sel()))
	return fmt.Sprintf("count by (source) (%s)", inner)
}

// PricingCURPeriod returns the start or end ("start"|"end") of the usage the pricing service's
// effective rates were computed from (the pricing service's own series; 0 when none are in use).
func PricingCURPeriod(bound string) string {
	return fmt.Sprintf("max(last_over_time(kerf_pricing_cur_period_%s_timestamp_seconds[%s]))", bound, promDuration(PricingSourceLookback))
}

func lastOver(d time.Duration) func(string) string {
	if d <= 0 {
		return func(s string) string { return s }
	}
	return func(s string) string { return fmt.Sprintf("last_over_time(%s[%s])", s, promDuration(d)) }
}

// promDuration renders a duration in PromQL syntax (e.g. 5m, 25h, 90s).
func promDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", max(1, d/time.Second))
	}
}
