// Package readiness runs the hub-side cluster readiness checks (metrics checks against the
// cluster's datasource), merges them with the agent's self-checks, computes the readiness state
// (api/v1alpha1/readiness.ComputeState), stores results and history, and exports
// kerf_cluster_readiness. See docs/contracts/readiness.md.
package readiness

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Thresholds of the metrics checks.
const (
	MaxStaleness      = 5 * time.Minute
	MaxScrapeInterval = 60 * time.Second
	MinOwnerJoin      = 0.95
	// DefaultHistoryWindow is the profile window when none is given (production profile).
	DefaultHistoryWindow = 7 * 24 * time.Hour
)

// NodeLabelsAllowlist is the kube-state-metrics setting the node-label check asks for.
const NodeLabelsAllowlist = "nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,eks.amazonaws.com/capacityType,topology.kubernetes.io/zone,topology.kubernetes.io/region]"

// MetricsChecker runs the hub's metrics checks for one cluster binding.
type MetricsChecker struct {
	// Q queries the cluster's datasource.
	Q recommender.Querier
	// API probes the datasource and lists label values; nil skips the probe (reachability is
	// then derived from the first query).
	API *datasource.API
	// M selects the cluster's series.
	M datasource.Matcher
	// Jobs are the binding's scrape job names, used in remediation texts.
	Jobs *hubapi.MetricsJobNames
	// Local queries the hub's own store for pricing series; nil = pricing.coverage unknown.
	Local recommender.Querier
	// HistoryWindow is the longest profile window used on the cluster; 0 = DefaultHistoryWindow.
	HistoryWindow time.Duration
	// DocsBase is the hub docs base URL (docsURL = DocsBase/readiness#<id>).
	DocsBase string
	Now      func() time.Time
}

// Report is the result of a run.
type Report struct {
	Checks      []hubapi.ReadinessCheck
	Found       hubapi.DatasourceTestFound
	LabelValues []string
	Latency     time.Duration
	// Reachable is false when the datasource could not be queried at all.
	Reachable bool
}

// OK reports whether every blocking check passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Severity == string(rd.SeverityBlocking) && c.Status != string(rd.StatusPass) {
			return false
		}
	}
	return true
}

type run struct {
	c   *MetricsChecker
	ctx context.Context
	now time.Time
	out *Report
}

// Run executes every metrics check (all catalog checks of source hub that read the datasource).
func (c *MetricsChecker) Run(ctx context.Context) Report {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	rep := &Report{}
	r := &run{c: c, ctx: ctx, now: now, out: rep}
	if !r.reachable() {
		for _, id := range metricsCheckIDs[1:] {
			r.add(id, rd.StatusUnknown, "not checked: the datasource is not reachable", "", "", nil)
		}
		return *rep
	}
	rep.Reachable = true
	labelOK := r.clusterLabel()
	if !labelOK {
		for _, id := range metricsCheckIDs[2:] {
			r.add(id, rd.StatusUnknown, "not checked: no series match the cluster's binding", "", "", nil)
		}
		return *rep
	}
	r.cadvisor()
	r.ksmOwners()
	r.ksmResources()
	r.freshness()
	r.ownerJoin()
	r.scrapeInterval()
	r.history()
	r.throttling()
	r.restartsOOM()
	r.hpa()
	r.nodeLabels()
	r.nodeAllocatable()
	r.dcgm()
	r.pricingCoverage()
	return *rep
}

// metricsCheckIDs are the checks Run produces, in order (reachable and cluster-label first).
var metricsCheckIDs = []string{
	rd.CheckDatasourceReachable, rd.CheckClusterLabel, rd.CheckCAdvisor, rd.CheckKSMOwners, rd.CheckKSMResources,
	rd.CheckFreshness, rd.CheckOwnerJoin, rd.CheckScrapeInterval, rd.CheckHistory, rd.CheckThrottling,
	rd.CheckRestartsOOM, rd.CheckHPASeries, rd.CheckNodeLabels, rd.CheckNodeAllocatable, rd.CheckDCGM, rd.CheckPricingCoverage,
}

// MetricsCheckIDs lists the checks a MetricsChecker run produces.
func MetricsCheckIDs() []string { return append([]string(nil), metricsCheckIDs...) }

func (r *run) add(id string, st rd.Status, msg, remediation, remType string, details map[string]string) {
	r.out.Checks = append(r.out.Checks, NewCheck(id, st, msg, remediation, remType, r.c.DocsBase, r.now, details))
}

// NewCheck builds a result with the catalog's category, severity, source and gates.
func NewCheck(id string, st rd.Status, msg, remediation, remType, docsBase string, at time.Time, details map[string]string) hubapi.ReadinessCheck {
	cat, _ := rd.Lookup(id)
	c := hubapi.ReadinessCheck{
		ID: id, Category: string(cat.Category), Severity: string(cat.Severity), Source: string(cat.Source), Gates: string(cat.Gates),
		Status: string(st), Message: msg, CheckedAt: at.UTC(), Details: details,
	}
	if st != rd.StatusPass {
		c.Remediation, c.RemediationType = remediation, remType
	}
	if docsBase != "" {
		c.DocsURL = strings.TrimRight(docsBase, "/") + "/readiness#" + id
	}
	return c
}

// sel is the cluster's selector with extra matchers.
func (r *run) sel(extra ...string) string { return r.c.M.Sel(extra...) }

// value runs an instant query and returns the sum of its samples; ok is false for an empty result.
func (r *run) value(q recommender.Querier, query string) (float64, bool, error) {
	res, err := q.Instant(r.ctx, query, r.now)
	if err != nil || len(res) == 0 {
		return 0, false, err
	}
	var v float64
	for _, s := range res {
		v += s.Sample.V
	}
	return v, true, nil
}

// count returns the number of series of metric (with extra matchers) seen in the last hour.
func (r *run) count(metric string, extra ...string) (int, error) {
	v, _, err := r.value(r.c.Q, fmt.Sprintf("count(last_over_time(%s%s[1h]))", metric, r.sel(extra...)))
	return int(v), err
}

func (r *run) queryFailed(id string, err error) {
	r.add(id, rd.StatusUnknown, "query failed: "+err.Error(), "", "", nil)
}

func (r *run) jobs(ksm bool) string {
	var names []string
	if j := r.c.Jobs; j != nil {
		names = j.CAdvisor
		if ksm {
			names = j.KubeStateMetrics
		}
	}
	if len(names) == 0 {
		if ksm {
			return "kube-state-metrics"
		}
		return "kubelet/cadvisor"
	}
	return strings.Join(names, ", ")
}

func (r *run) reachable() bool {
	if r.c.API == nil {
		start := time.Now()
		_, _, err := r.value(r.c.Q, "vector(1)")
		r.out.Latency = time.Since(start)
		if err != nil {
			r.add(rd.CheckDatasourceReachable, rd.StatusFail, err.Error(), "Check the datasource URL, credentials and network access from the hub.", hubapi.RemediationText, map[string]string{"error": err.Error()})
			return false
		}
		r.add(rd.CheckDatasourceReachable, rd.StatusPass, "query API answered", "", "", nil)
		return true
	}
	p := r.c.API.Probe(r.ctx)
	r.out.Latency = p.Latency
	d := map[string]string{"latencyMs": strconv.FormatInt(p.Latency.Milliseconds(), 10)}
	if p.HTTPStatus != 0 {
		d["httpStatus"] = strconv.Itoa(p.HTTPStatus)
	}
	if !p.OK {
		d["error"] = p.Error
		r.add(rd.CheckDatasourceReachable, rd.StatusFail, p.Error, p.Hint, hubapi.RemediationText, d)
		return false
	}
	r.add(rd.CheckDatasourceReachable, rd.StatusPass, fmt.Sprintf("query API answered in %d ms", p.Latency.Milliseconds()), "", "", d)
	return true
}

// clusterLabel checks that the binding matches series and lists existing label values.
func (r *run) clusterLabel() bool {
	label := r.c.M.Label
	if label == "" {
		label = datasource.ClusterLabel
	}
	if r.c.API != nil {
		vals, err := r.c.API.LabelValues(r.ctx, label, []string{"kube_pod_owner", "container_memory_working_set_bytes"}, r.now.Add(-time.Hour), 50)
		if err == nil {
			r.out.LabelValues = vals
		}
	}
	v, _, err := r.value(r.c.Q, fmt.Sprintf("count(last_over_time(%s[1h]))",
		r.sel(`__name__=~"container_memory_working_set_bytes|container_cpu_usage_seconds_total|kube_pod_owner|kube_node_labels"`)))
	n := int(v)
	if err != nil {
		r.queryFailed(rd.CheckClusterLabel, err)
		return false
	}
	matchers := strings.Join(r.c.M.Selectors(), ", ")
	if matchers == "" {
		matchers = "(none: the whole datasource)"
	}
	d := map[string]string{"matchers": matchers, "series": strconv.Itoa(n)}
	if n > 0 {
		r.add(rd.CheckClusterLabel, rd.StatusPass, fmt.Sprintf("%d cAdvisor and kube-state-metrics series match %s", n, matchers), "", "", d)
		return true
	}
	msg := fmt.Sprintf("no cAdvisor or kube-state-metrics series match %s in the last hour", matchers)
	rem := "Set the cluster's metrics binding (clusterLabel, clusterLabelValue, extraMatchers) to the labels your metrics carry."
	if len(r.out.LabelValues) > 0 {
		d["suggestions"] = strings.Join(r.out.LabelValues, ",")
		msg += fmt.Sprintf("; existing values of %s: %s", label, strings.Join(r.out.LabelValues, ", "))
	} else if r.c.M.Label != "" {
		rem += fmt.Sprintf(" No series carry the label %s: check the label key (k8s_cluster, cluster_name, eks_cluster…) or add it as an external label of the scraper.", label)
	}
	r.add(rd.CheckClusterLabel, rd.StatusFail, msg, rem, hubapi.RemediationText, d)
	return false
}

func (r *run) cadvisor() {
	cpu, err := r.count("container_cpu_usage_seconds_total", `container!=""`, `container!="POD"`)
	if err != nil {
		r.queryFailed(rd.CheckCAdvisor, err)
		return
	}
	mem, err := r.count("container_memory_working_set_bytes", `container!=""`, `container!="POD"`)
	if err != nil {
		r.queryFailed(rd.CheckCAdvisor, err)
		return
	}
	r.out.Found.ContainerSeries = mem
	d := map[string]string{"cpuSeries": strconv.Itoa(cpu), "memorySeries": strconv.Itoa(mem)}
	if cpu > 0 && mem > 0 {
		r.add(rd.CheckCAdvisor, rd.StatusPass, fmt.Sprintf("%d container CPU and %d memory working set series", cpu, mem), "", "", d)
		return
	}
	// series without the container label: cAdvisor scraped without per-container labels
	anyMem, _ := r.count("container_memory_working_set_bytes")
	if anyMem > 0 {
		r.add(rd.CheckCAdvisor, rd.StatusFail, fmt.Sprintf("%d memory working set series but none with a container label", anyMem),
			"Keep the container, pod and namespace labels of the kubelet's /metrics/cadvisor endpoint (no labeldrop/relabelling of them in the "+r.jobs(false)+" scrape job).", hubapi.RemediationText, d)
		return
	}
	missing := []string{}
	if cpu == 0 {
		missing = append(missing, "container_cpu_usage_seconds_total")
	}
	if mem == 0 {
		missing = append(missing, "container_memory_working_set_bytes")
	}
	r.add(rd.CheckCAdvisor, rd.StatusFail, "missing "+strings.Join(missing, ", "),
		`Scrape the kubelet's cAdvisor endpoint (job `+r.jobs(false)+`), e.g. for Prometheus:
- job_name: kubelet-cadvisor
  scheme: https
  metrics_path: /metrics/cadvisor
  kubernetes_sd_configs: [{role: node}]
  bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
  tls_config: {insecure_skip_verify: true}`, hubapi.RemediationYAML, d)
}

func (r *run) ksmOwners() {
	pods, _, err := r.value(r.c.Q, fmt.Sprintf("count(count by (namespace, pod) (last_over_time(kube_pod_owner%s[1h])))", r.sel()))
	if err != nil {
		r.queryFailed(rd.CheckKSMOwners, err)
		return
	}
	r.out.Found.Pods = int(pods)
	rem := "Install kube-state-metrics (job " + r.jobs(true) + ") and scrape it, e.g. `helm install kube-state-metrics prometheus-community/kube-state-metrics -n kube-system`."
	if pods == 0 {
		r.add(rd.CheckKSMOwners, rd.StatusFail, "kube_pod_owner missing: workloads cannot be resolved", rem, hubapi.RemediationText, nil)
		return
	}
	// owner kinds referenced by pods must have their owner series
	var missing []string
	d := map[string]string{"pods": strconv.Itoa(int(pods))}
	for _, o := range []struct{ kind, metric string }{{"ReplicaSet", "kube_replicaset_owner"}, {"Job", "kube_job_owner"}} {
		owned, err := r.count("kube_pod_owner", fmt.Sprintf("owner_kind=%q", o.kind))
		if err != nil {
			r.queryFailed(rd.CheckKSMOwners, err)
			return
		}
		if owned == 0 {
			continue
		}
		n, err := r.count(o.metric)
		if err != nil {
			r.queryFailed(rd.CheckKSMOwners, err)
			return
		}
		d[o.metric] = strconv.Itoa(n)
		if n == 0 {
			missing = append(missing, o.metric)
		}
	}
	if len(missing) > 0 {
		r.add(rd.CheckKSMOwners, rd.StatusFail, "missing "+strings.Join(missing, ", ")+": pods of Deployments/CronJobs cannot be resolved to their workload",
			"Enable the replicasets and jobs collectors of kube-state-metrics (--resources) and do not drop these series.", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckKSMOwners, rd.StatusPass, fmt.Sprintf("owner series for %d pods", int(pods)), "", "", d)
}

func (r *run) ksmResources() {
	req, err := r.count("kube_pod_container_resource_requests", `resource=~"cpu|memory"`)
	if err != nil {
		r.queryFailed(rd.CheckKSMResources, err)
		return
	}
	lim, err := r.count("kube_pod_container_resource_limits", `resource=~"cpu|memory"`)
	if err != nil {
		r.queryFailed(rd.CheckKSMResources, err)
		return
	}
	d := map[string]string{"requestSeries": strconv.Itoa(req), "limitSeries": strconv.Itoa(lim)}
	if req == 0 {
		r.add(rd.CheckKSMResources, rd.StatusFail, "kube_pod_container_resource_requests missing: no current values, no savings",
			"Scrape kube-state-metrics (job "+r.jobs(true)+") with the pods collector enabled and keep kube_pod_container_resource_requests/limits.", hubapi.RemediationText, d)
		return
	}
	msg := fmt.Sprintf("%d request and %d limit series", req, lim)
	if lim == 0 {
		msg += " (no container sets a limit, or kube_pod_container_resource_limits is dropped)"
	}
	r.add(rd.CheckKSMResources, rd.StatusPass, msg, "", "", d)
}

func (r *run) freshness() {
	worst := time.Duration(0)
	d := map[string]string{}
	for _, m := range []string{"container_memory_working_set_bytes", "kube_pod_owner"} {
		ts, ok, err := r.value(r.c.Q, fmt.Sprintf("max(max_over_time(timestamp(%s%s)[1h:1m]))", m, r.sel()))
		if err != nil {
			r.queryFailed(rd.CheckFreshness, err)
			return
		}
		age := time.Hour
		if ok {
			age = r.now.Sub(time.Unix(0, int64(ts*1e9)))
			if age < 0 {
				age = 0
			}
		}
		d[m] = age.Truncate(time.Second).String()
		worst = max(worst, age)
	}
	d["lastSampleAge"] = worst.Truncate(time.Second).String()
	if worst > MaxStaleness {
		r.add(rd.CheckFreshness, rd.StatusFail, fmt.Sprintf("last sample is %s old (limit %s): scraping or remote-write stopped", humanAge(worst), MaxStaleness),
			"Check the scraper (vmagent/Prometheus) of the cluster: its targets page, remote-write errors and the kube-state-metrics pod.", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckFreshness, rd.StatusPass, fmt.Sprintf("last sample %s ago", humanAge(worst)), "", "", d)
}

func (r *run) ownerJoin() {
	cad := fmt.Sprintf("count by (namespace, pod) (last_over_time(container_memory_working_set_bytes%s[1h]))", r.sel(`container!=""`, `container!="POD"`))
	own := fmt.Sprintf("count by (namespace, pod) (last_over_time(kube_pod_owner%s[1h]))", r.sel())
	total, _, err := r.value(r.c.Q, "count("+cad+")")
	if err != nil {
		r.queryFailed(rd.CheckOwnerJoin, err)
		return
	}
	joined, _, err := r.value(r.c.Q, fmt.Sprintf("count(%s and on (namespace, pod) %s)", cad, own))
	if err != nil {
		r.queryFailed(rd.CheckOwnerJoin, err)
		return
	}
	if total == 0 {
		r.add(rd.CheckOwnerJoin, rd.StatusUnknown, "no cAdvisor pods to join", "", "", nil)
		return
	}
	cov := joined / total
	d := map[string]string{"coverage": strconv.FormatFloat(math.Floor(cov*1000)/10, 'f', 1, 64), "pods": strconv.Itoa(int(total)), "joined": strconv.Itoa(int(joined))}
	if cov < MinOwnerJoin {
		r.add(rd.CheckOwnerJoin, rd.StatusWarn, fmt.Sprintf("only %s %% of cAdvisor pods match a kube_pod_owner series (want ≥ 95 %%)", d["coverage"]),
			"cAdvisor and kube-state-metrics series must carry the same namespace and pod labels: remove relabelling that renames them (e.g. exported_namespace/exported_pod: set honorLabels on the kube-state-metrics scrape).", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckOwnerJoin, rd.StatusPass, fmt.Sprintf("%s %% of cAdvisor pods resolve to an owner", d["coverage"]), "", "", d)
}

func (r *run) scrapeInterval() {
	n, ok, err := r.value(r.c.Q, fmt.Sprintf("quantile(0.5, count_over_time(container_memory_working_set_bytes%s[30m]))", r.sel(`container!=""`, `container!="POD"`)))
	if err != nil {
		r.queryFailed(rd.CheckScrapeInterval, err)
		return
	}
	if !ok || n < 1 {
		r.add(rd.CheckScrapeInterval, rd.StatusUnknown, "no samples in the last 30 minutes", "", "", nil)
		return
	}
	interval := time.Duration(float64(30*time.Minute) / n).Round(time.Second)
	d := map[string]string{"interval": interval.String()}
	if interval > MaxScrapeInterval+5*time.Second {
		r.add(rd.CheckScrapeInterval, rd.StatusWarn, fmt.Sprintf("cAdvisor is scraped about every %s: P95/P99 are unreliable above 60 s", interval),
			"Set scrape_interval: 30s (or at most 60s) on the "+r.jobs(false)+" job.", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckScrapeInterval, rd.StatusPass, fmt.Sprintf("cAdvisor scraped about every %s", interval), "", "", d)
}

// historyProbes are the ages the history check looks for data at.
var historyProbes = []time.Duration{24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour, 14 * 24 * time.Hour, 28 * 24 * time.Hour}

func (r *run) history() {
	window := r.c.HistoryWindow
	if window <= 0 {
		window = DefaultHistoryWindow
	}
	probes := append([]time.Duration{}, historyProbes...)
	probes = append(probes, window)
	sort.Slice(probes, func(i, j int) bool { return probes[i] < probes[j] })
	have := time.Duration(0)
	for _, age := range probes {
		if age > window {
			break
		}
		// data within the hour before `age` ago
		v, _, err := r.value(r.c.Q, fmt.Sprintf("count(last_over_time(container_memory_working_set_bytes%s[1h] offset %s))", r.sel(`container!=""`), promDuration(age-time.Hour)))
		if err != nil {
			r.queryFailed(rd.CheckHistory, err)
			return
		}
		if v == 0 {
			break
		}
		have = age
	}
	d := map[string]string{"window": promDuration(window), "history": "<" + promDuration(historyProbes[0])}
	if have > 0 {
		d["history"] = "≥" + promDuration(have)
	}
	if have >= window {
		r.add(rd.CheckHistory, rd.StatusPass, "history covers the "+promDuration(window)+" profile window", "", "", d)
		return
	}
	enough := r.now.Add(window - have)
	d["enoughDataAt"] = enough.UTC().Format(time.RFC3339)
	r.add(rd.CheckHistory, rd.StatusWarn, fmt.Sprintf("history is %s, shorter than the %s profile window: recommendations stay low-confidence until about %s",
		d["history"], promDuration(window), enough.UTC().Format("2006-01-02")),
		"Wait for more data, or raise the retention of the datasource to at least the profile window.", hubapi.RemediationText, d)
}

func (r *run) throttling() {
	periods, err := r.count("container_cpu_cfs_periods_total", `container!=""`)
	if err != nil {
		r.queryFailed(rd.CheckThrottling, err)
		return
	}
	thr, err := r.count("container_cpu_cfs_throttled_periods_total", `container!=""`)
	if err != nil {
		r.queryFailed(rd.CheckThrottling, err)
		return
	}
	d := map[string]string{"periodsSeries": strconv.Itoa(periods), "throttledSeries": strconv.Itoa(thr)}
	if periods == 0 || thr == 0 {
		r.add(rd.CheckThrottling, rd.StatusWarn, "container_cpu_cfs_periods_total / container_cpu_cfs_throttled_periods_total missing: the throttled guardrail is blind (they exist only for containers with a CPU limit)",
			"Keep container_cpu_cfs_* in the "+r.jobs(false)+" scrape job (no metric_relabel_configs drop).", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckThrottling, rd.StatusPass, fmt.Sprintf("%d throttling series", thr), "", "", d)
}

func (r *run) restartsOOM() {
	restarts, err := r.count("kube_pod_container_status_restarts_total")
	if err != nil {
		r.queryFailed(rd.CheckRestartsOOM, err)
		return
	}
	reason, err := r.count("kube_pod_container_status_last_terminated_reason")
	if err != nil {
		r.queryFailed(rd.CheckRestartsOOM, err)
		return
	}
	oom, err := r.count("container_oom_events_total")
	if err != nil {
		r.queryFailed(rd.CheckRestartsOOM, err)
		return
	}
	d := map[string]string{"restartSeries": strconv.Itoa(restarts), "lastTerminatedSeries": strconv.Itoa(reason), "oomEventSeries": strconv.Itoa(oom)}
	var missing []string
	if restarts == 0 {
		missing = append(missing, "kube_pod_container_status_restarts_total")
	}
	if reason == 0 && oom == 0 {
		missing = append(missing, "container_oom_events_total (and no kube_pod_container_status_last_terminated_reason)")
	}
	if len(missing) > 0 {
		r.add(rd.CheckRestartsOOM, rd.StatusWarn, "missing "+strings.Join(missing, ", ")+": OOM/restart guardrails and rollbacks are blind",
			"Keep these series in the kube-state-metrics and cAdvisor scrape jobs; container_oom_events_total needs kubelet ≥ 1.26.", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckRestartsOOM, rd.StatusPass, "restart and OOM series present", "", "", d)
}

func (r *run) hpa() {
	n, err := r.count("kube_horizontalpodautoscaler_info")
	if err != nil {
		r.queryFailed(rd.CheckHPASeries, err)
		return
	}
	if n == 0 {
		// kube-state-metrics exports nothing when the cluster has no HPA.
		r.add(rd.CheckHPASeries, rd.StatusUnknown, "no kube_horizontalpodautoscaler_* series: either the cluster has no HPA or the horizontalpodautoscalers collector of kube-state-metrics is off",
			"If the cluster has HPAs, enable the horizontalpodautoscalers collector of kube-state-metrics (--resources).", hubapi.RemediationText, nil)
		return
	}
	spec, err := r.count("kube_horizontalpodautoscaler_spec_target_metric")
	if err != nil {
		r.queryFailed(rd.CheckHPASeries, err)
		return
	}
	d := map[string]string{"hpas": strconv.Itoa(n), "targetMetricSeries": strconv.Itoa(spec)}
	if spec == 0 {
		r.add(rd.CheckHPASeries, rd.StatusWarn, "kube_horizontalpodautoscaler_spec_target_metric missing: HPA coupling cannot be detected",
			"Use kube-state-metrics ≥ 2.0 and keep kube_horizontalpodautoscaler_spec_target_metric.", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckHPASeries, rd.StatusPass, fmt.Sprintf("%d HPAs", n), "", "", d)
}

func (r *run) nodeLabels() {
	res, err := r.c.Q.Instant(r.ctx, fmt.Sprintf("max by (node, label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, label_eks_amazonaws_com_capacity_type, label_topology_kubernetes_io_zone, label_topology_kubernetes_io_region) (last_over_time(kube_node_labels%s[1h]))", r.sel()), r.now)
	if err != nil {
		r.queryFailed(rd.CheckNodeLabels, err)
		return
	}
	nodes := map[string]bool{}
	missing := map[string]int{}
	for _, s := range res {
		nodes[s.Labels["node"]] = true
		l := s.Labels
		if l["label_node_kubernetes_io_instance_type"] == "" {
			missing["instance type"]++
		}
		if l["label_karpenter_sh_capacity_type"] == "" && l["label_eks_amazonaws_com_capacity_type"] == "" {
			missing["capacity type"]++
		}
		if l["label_topology_kubernetes_io_zone"] == "" {
			missing["zone"]++
		}
		if l["label_topology_kubernetes_io_region"] == "" && l["label_topology_kubernetes_io_zone"] == "" {
			missing["region"]++
		}
	}
	r.out.Found.Nodes = len(nodes)
	rem := "kube-state-metrics:\n  metricLabelsAllowlist:\n    - " + NodeLabelsAllowlist
	if len(nodes) == 0 {
		r.add(rd.CheckNodeLabels, rd.StatusWarn, "kube_node_labels missing: pricing falls back to default rates", rem, hubapi.RemediationYAML, nil)
		return
	}
	if len(missing) == 0 {
		r.add(rd.CheckNodeLabels, rd.StatusPass, fmt.Sprintf("%d nodes with instance type, capacity type, zone and region", len(nodes)), "", "", map[string]string{"nodes": strconv.Itoa(len(nodes))})
		return
	}
	var parts []string
	d := map[string]string{"nodes": strconv.Itoa(len(nodes))}
	for _, k := range []string{"instance type", "capacity type", "zone", "region"} {
		if missing[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s on %d of %d nodes", k, missing[k], len(nodes)))
			d[strings.ReplaceAll(k, " ", "")+"Missing"] = strconv.Itoa(missing[k])
		}
	}
	r.add(rd.CheckNodeLabels, rd.StatusWarn, "kube_node_labels lacks "+strings.Join(parts, ", ")+": pricing falls back to default rates", rem, hubapi.RemediationYAML, d)
}

func (r *run) nodeAllocatable() {
	n, _, err := r.value(r.c.Q, fmt.Sprintf("count(count by (node) (last_over_time(kube_node_status_allocatable%s[1h])))", r.sel(`resource=~"cpu|memory"`)))
	if err != nil {
		r.queryFailed(rd.CheckNodeAllocatable, err)
		return
	}
	if n == 0 {
		r.add(rd.CheckNodeAllocatable, rd.StatusWarn, "kube_node_status_allocatable missing: the cost split per vCPU/GiB is impossible",
			"Enable the nodes collector of kube-state-metrics and keep kube_node_status_allocatable.", hubapi.RemediationText, nil)
		return
	}
	r.add(rd.CheckNodeAllocatable, rd.StatusPass, fmt.Sprintf("allocatable for %d nodes", int(n)), "", "", map[string]string{"nodes": strconv.Itoa(int(n))})
}

func (r *run) pricingCoverage() {
	total := r.out.Found.Nodes
	if r.c.Local == nil || total == 0 {
		r.add(rd.CheckPricingCoverage, rd.StatusUnknown, "not checked: no node labels or no pricing store", "", "", nil)
		return
	}
	priced, _, err := r.value(r.c.Local, fmt.Sprintf("count(max by (node) (last_over_time(kerf_node_hourly_cost_usd%s[1h])))", datasource.DefaultMatcher(r.c.M.Cluster).Sel()))
	if err != nil {
		r.queryFailed(rd.CheckPricingCoverage, err)
		return
	}
	d := map[string]string{"priced": strconv.Itoa(int(priced)), "nodes": strconv.Itoa(total)}
	// Rate sources (best effort): details source.cur, source.list, source.spot, source.static.
	if res, err := r.c.Local.Instant(r.ctx, fmt.Sprintf("count by (source) (max by (node, source) (last_over_time(kerf_node_hourly_cost_usd%s[10m])))", datasource.DefaultMatcher(r.c.M.Cluster).Sel()), r.now); err == nil {
		for _, s := range res {
			if src := s.Labels["source"]; src != "" {
				d["source."+src] = strconv.Itoa(int(s.Sample.V))
			}
		}
	}
	// The Cost and Usage Report reader exists when its series do; a failed last read is a warning.
	curFailed := false
	if ok, found, err := r.value(r.c.Local, "max(last_over_time(kerf_pricing_cur_last_read_ok[10m]))"); err == nil && found {
		d["curLastReadOK"] = strconv.FormatBool(ok == 1)
		curFailed = ok != 1
	}
	const curRemediation = "The pricing service cannot read the AWS Cost and Usage Report: see its log (kubectl logs deploy/pricing) for the error, usually IAM (Athena, Glue or S3 access) or a wrong table, bucket or prefix. It keeps the last effective rates for pricing.cur.maxAge, then uses list prices. docs/pricing.md."
	if int(priced) < total {
		msg := fmt.Sprintf("%d of %d nodes priced: the others use the default rates", int(priced), total)
		if curFailed {
			msg += "; the AWS Cost and Usage Report cannot be read"
		}
		r.add(rd.CheckPricingCoverage, rd.StatusWarn, msg,
			"See kerf_pricing_nodes{status=\"skipped\"} and the pricing service log for the skip reason (usually missing node labels, see metrics.node-labels).", hubapi.RemediationText, d)
		return
	}
	if curFailed {
		r.add(rd.CheckPricingCoverage, rd.StatusWarn, fmt.Sprintf("all %d nodes priced, but the AWS Cost and Usage Report cannot be read: effective rates are stale or replaced by list prices", total),
			curRemediation, hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckPricingCoverage, rd.StatusPass, fmt.Sprintf("all %d nodes priced", total), "", "", d)
}

func humanAge(d time.Duration) string {
	if d >= time.Hour {
		return "over 1h"
	}
	return d.Truncate(time.Second).String()
}

func promDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}
