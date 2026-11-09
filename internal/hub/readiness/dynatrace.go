package readiness

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Remediations of the Dynatrace metrics checks. They name Dynatrace steps only: a Dynatrace
// datasource has no scrape jobs or kube-state-metrics to fix.
const (
	DynatraceK8sMonitoring = "Enable Kubernetes monitoring for this cluster in Dynatrace (Dynatrace Operator with a DynaKube whose ActiveGate has the kubernetes-monitoring capability), then check in the Dynatrace Kubernetes app that its containers report CPU, memory, requests and limits."
	DynatraceHPAUnknown    = "Review the recommendations of HPA-scaled workloads before you automate them, or exclude those workloads with a policy. Kerf does not guess HPA coupling for a Dynatrace datasource."
	dynatraceStale         = "Dynatrace stopped receiving this cluster's Kubernetes metrics: check the Dynatrace Operator, ActiveGate and OneAgent pods in the cluster and the cluster's status in the Dynatrace Kubernetes app."
	dynatraceThrottling    = "The throttling ratio comes from OneAgent container monitoring: run OneAgent on the cluster's nodes (DynaKube cloudNativeFullStack or classicFullStack) so containers report dt.containers.cpu.throttling_ratio with their k8s.* dimensions."
	dynatraceNodeLabels    = "Dynatrace reads node labels from Kubernetes: set the missing labels on the nodes. EKS sets node.kubernetes.io/instance-type and the topology.kubernetes.io labels; Karpenter sets karpenter.sh/capacity-type and managed node groups eks.amazonaws.com/capacityType."
	dynatraceNoNodes       = "The token needs the storage:smartscape:read scope, and the cluster's Kubernetes monitoring must report its nodes (Dynatrace Kubernetes app → Nodes)."
	dynatraceRetention     = "Wait for more data, or keep Dynatrace metrics for at least the profile window (the retention of the Grail metrics bucket)."
)

// runDynatrace runs the metrics checks against a Dynatrace datasource: the same check ids,
// read from the dt.kubernetes.* metrics and Smartscape, with Dynatrace remediations.
func (r *run) runDynatrace(src *dynatrace.Source) {
	ctx := r.ctx
	cluster := r.c.M.Value
	if cluster == "" {
		cluster = r.c.M.Cluster
	}
	lat, err := src.Probe(ctx)
	r.out.Latency = lat
	d := map[string]string{"latencyMs": strconv.FormatInt(lat.Milliseconds(), 10)}
	if err != nil {
		d["error"] = err.Error()
		r.add(rd.CheckDatasourceReachable, rd.StatusFail, err.Error(), dynatrace.Hint(err), hubapi.RemediationText, d)
		for _, id := range metricsCheckIDs[1:] {
			r.add(id, rd.StatusUnknown, "not checked: the datasource is not reachable", "", "", nil)
		}
		return
	}
	r.out.Reachable = true
	r.add(rd.CheckDatasourceReachable, rd.StatusPass, fmt.Sprintf("Dynatrace query API answered in %d ms", lat.Milliseconds()), "", "", d)

	counts, err := src.Count(ctx, cluster, r.now)
	if err != nil {
		for _, id := range metricsCheckIDs[1:] {
			r.queryFailed(id, err)
		}
		return
	}
	r.out.Metrics = counts.Statuses()
	cpu, mem := counts.Series[dynatrace.KeyCPUUsage], counts.Series[dynatrace.KeyMemoryWorkingSet]
	r.out.Found.ContainerSeries = mem
	r.out.Found.Pods = counts.OwnedPods
	if !r.dtClusterLabel(src, cluster, cpu+mem, counts) {
		for _, id := range metricsCheckIDs[2:] {
			r.add(id, rd.StatusUnknown, "not checked: no Dynatrace series match the cluster's binding", "", "", nil)
		}
		return
	}
	r.dtContainers(cpu, mem)
	r.dtOwners(counts)
	r.dtResources(counts)
	samples := r.dtFreshness(src, cluster)
	r.dtOwnerJoin(counts)
	r.dtResolution(samples)
	r.dtHistory(src, cluster)
	r.dtThrottling(counts)
	r.dtRestartsOOM(counts)
	r.add(rd.CheckHPASeries, rd.StatusUnknown,
		"HPA coupling not available: Dynatrace records no HorizontalPodAutoscaler metrics, so Kerf cannot tell which workloads an HPA scales",
		DynatraceHPAUnknown, hubapi.RemediationText, nil)
	r.dtNodes(src, cluster)
	r.add(rd.CheckDCGM, rd.StatusUnknown, "GPU metrics are not read from a Dynatrace datasource", "", "", nil)
	r.pricingCoverage()
}

// failedQuery reports a mapping input that could not be checked; ok is false then.
func (r *run) failedQuery(id string, counts dynatrace.Counts, keys ...string) bool {
	for _, k := range keys {
		if e := counts.Errors[k]; e != "" {
			r.add(id, rd.StatusUnknown, "query failed: "+e, "", "", nil)
			return false
		}
	}
	return true
}

func (r *run) dtClusterLabel(src *dynatrace.Source, cluster string, n int, counts dynatrace.Counts) bool {
	if !r.failedQuery(rd.CheckClusterLabel, counts, dynatrace.KeyCPUUsage, dynatrace.KeyMemoryWorkingSet) {
		return false
	}
	matcher := dynatrace.DimCluster + "=" + strconv.Quote(cluster)
	d := map[string]string{"matchers": matcher, "series": strconv.Itoa(n)}
	if n > 0 {
		r.add(rd.CheckClusterLabel, rd.StatusPass, fmt.Sprintf("%d container series match %s", n, matcher), "", "", d)
		return true
	}
	msg := fmt.Sprintf("no Dynatrace container series match %s in the last hour", matcher)
	if names, err := src.ClusterNames(r.ctx, r.now); err == nil && len(names) > 0 {
		if len(names) > 50 {
			names = names[:50]
		}
		r.out.LabelValues = names
		d["suggestions"] = strings.Join(names, ",")
		msg += "; existing values of " + dynatrace.DimCluster + ": " + strings.Join(names, ", ")
	}
	r.add(rd.CheckClusterLabel, rd.StatusFail, msg,
		"Set the cluster's metrics binding to its Dynatrace cluster name (clusterLabelValue = the k8s.cluster.name shown in the Dynatrace Kubernetes app). If the cluster is not in the list, "+lowerFirst(DynatraceK8sMonitoring),
		hubapi.RemediationText, d)
	return false
}

func (r *run) dtContainers(cpu, mem int) {
	d := map[string]string{"cpuSeries": strconv.Itoa(cpu), "memorySeries": strconv.Itoa(mem)}
	if cpu > 0 && mem > 0 {
		r.add(rd.CheckCAdvisor, rd.StatusPass, fmt.Sprintf("%d %s and %d %s series", cpu, dynatrace.KeyCPUUsage, mem, dynatrace.KeyMemoryWorkingSet), "", "", d)
		return
	}
	var missing []string
	if cpu == 0 {
		missing = append(missing, dynatrace.KeyCPUUsage)
	}
	if mem == 0 {
		missing = append(missing, dynatrace.KeyMemoryWorkingSet)
	}
	r.add(rd.CheckCAdvisor, rd.StatusFail, "missing "+strings.Join(missing, ", "), DynatraceK8sMonitoring, hubapi.RemediationText, d)
}

func (r *run) dtOwners(counts dynatrace.Counts) {
	if !r.failedQuery(rd.CheckKSMOwners, counts, "owners") {
		return
	}
	d := map[string]string{"pods": strconv.Itoa(counts.Pods), "ownedPods": strconv.Itoa(counts.OwnedPods)}
	if counts.OwnedPods == 0 {
		r.add(rd.CheckKSMOwners, rd.StatusFail, "no container series carry the k8s.workload.kind and k8s.workload.name dimensions of a Deployment, StatefulSet, DaemonSet or CronJob: workloads cannot be resolved",
			DynatraceK8sMonitoring, hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckKSMOwners, rd.StatusPass, fmt.Sprintf("Dynatrace resolves the workload (k8s.workload.kind, k8s.workload.name) of %d pods", counts.OwnedPods), "", "", d)
}

// dtOwnerJoin replaces the cAdvisor-to-owner join of a Prometheus datasource: the share of pods
// whose workload Kerf rightsizes.
func (r *run) dtOwnerJoin(counts dynatrace.Counts) {
	if counts.Errors["owners"] != "" || counts.Pods == 0 {
		r.add(rd.CheckOwnerJoin, rd.StatusUnknown, "no pods to resolve", "", "", nil)
		return
	}
	cov := float64(counts.OwnedPods) / float64(counts.Pods)
	d := map[string]string{"coverage": strconv.FormatFloat(math.Floor(cov*1000)/10, 'f', 1, 64), "pods": strconv.Itoa(counts.Pods), "joined": strconv.Itoa(counts.OwnedPods)}
	if cov < MinOwnerJoin {
		r.add(rd.CheckOwnerJoin, rd.StatusWarn, fmt.Sprintf("only %s %% of pods belong to a Deployment, StatefulSet, DaemonSet or CronJob (want ≥ 95 %%)", d["coverage"]),
			"Kerf rightsizes only these workload kinds; bare pods and Jobs without a CronJob are skipped. Check the pods' k8s.workload.kind in the Dynatrace Kubernetes app; nothing to fix when they are expected.", hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckOwnerJoin, rd.StatusPass, fmt.Sprintf("%s %% of pods resolve to a workload", d["coverage"]), "", "", d)
}

func (r *run) dtResources(counts dynatrace.Counts) {
	if !r.failedQuery(rd.CheckKSMResources, counts, dynatrace.KeyRequestsCPU, dynatrace.KeyRequestsMemory, dynatrace.KeyLimitsCPU, dynatrace.KeyLimitsMemory) {
		return
	}
	req := counts.Series[dynatrace.KeyRequestsCPU] + counts.Series[dynatrace.KeyRequestsMemory]
	lim := counts.Series[dynatrace.KeyLimitsCPU] + counts.Series[dynatrace.KeyLimitsMemory]
	d := map[string]string{"requestSeries": strconv.Itoa(req), "limitSeries": strconv.Itoa(lim)}
	if counts.Series[dynatrace.KeyRequestsCPU] == 0 || counts.Series[dynatrace.KeyRequestsMemory] == 0 {
		r.add(rd.CheckKSMResources, rd.StatusFail, dynatrace.KeyRequestsCPU+" / "+dynatrace.KeyRequestsMemory+" missing: no current values, no savings",
			DynatraceK8sMonitoring, hubapi.RemediationText, d)
		return
	}
	msg := fmt.Sprintf("%d request and %d limit series", req, lim)
	if lim == 0 {
		msg += " (no container sets a limit)"
	}
	r.add(rd.CheckKSMResources, rd.StatusPass, msg, "", "", d)
}

// dtFreshness checks the age of the newest memory sample; it returns the cluster's 1-minute
// samples of the last hour (nil when the query failed).
func (r *run) dtFreshness(src *dynatrace.Source, cluster string) []recommender.Sample {
	samples, _, err := src.Aggregate(r.ctx, dynatrace.KeyMemoryWorkingSet, "max", cluster, r.now.Add(-time.Hour), r.now, time.Minute)
	if err != nil {
		r.queryFailed(rd.CheckFreshness, err)
		return nil
	}
	if len(samples) == 0 {
		d := map[string]string{dynatrace.KeyMemoryWorkingSet: Absent, "lastSampleAge": Absent}
		r.add(rd.CheckFreshness, rd.StatusFail, "no samples of "+dynatrace.KeyMemoryWorkingSet+" in the last 1h", dynatraceStale, hubapi.RemediationText, d)
		return []recommender.Sample{}
	}
	last := samples[len(samples)-1].T
	age := max(r.now.Sub(last), 0)
	d := map[string]string{dynatrace.KeyMemoryWorkingSet: age.Truncate(time.Second).String(), "lastSampleAge": age.Truncate(time.Second).String()}
	if age > MaxStaleness {
		r.add(rd.CheckFreshness, rd.StatusFail, fmt.Sprintf("last sample is %s old (limit %s)", humanAge(age), MaxStaleness), dynatraceStale, hubapi.RemediationText, d)
	} else {
		r.add(rd.CheckFreshness, rd.StatusPass, fmt.Sprintf("last sample %s ago", humanAge(age)), "", "", d)
	}
	return samples
}

// dtResolution estimates the time between points of the last 30 minutes: Dynatrace stores one
// point per minute; gaps make it coarser.
func (r *run) dtResolution(samples []recommender.Sample) {
	if samples == nil {
		r.add(rd.CheckScrapeInterval, rd.StatusUnknown, "not checked: the freshness query failed", "", "", nil)
		return
	}
	var recent []time.Time
	for _, s := range samples {
		if s.T.After(r.now.Add(-scrapeWindow)) {
			recent = append(recent, s.T)
		}
	}
	interval, used := estimateScrapeInterval([]seriesCoverage{{span: spanOf(recent), samples: len(recent)}})
	if used == 0 {
		r.add(rd.CheckScrapeInterval, rd.StatusUnknown, fmt.Sprintf("not enough data yet: fewer than %d points in the last 30 minutes", minScrapeSamples), "", "", nil)
		return
	}
	sd := map[string]string{"interval": interval.String(), "points": strconv.Itoa(len(recent))}
	secs := fmt.Sprintf("%.0f s", interval.Seconds())
	if interval > MaxScrapeInterval+5*time.Second {
		r.add(rd.CheckScrapeInterval, rd.StatusWarn, "Dynatrace has a point about every "+secs+": P95/P99 are unreliable above 60 s", dynatraceStale, hubapi.RemediationText, sd)
		return
	}
	r.add(rd.CheckScrapeInterval, rd.StatusPass, "one point about every "+secs+" (Dynatrace resolution)", "", "", sd)
}

func spanOf(t []time.Time) time.Duration {
	if len(t) < 2 {
		return 0
	}
	return t[len(t)-1].Sub(t[0])
}

func (r *run) dtHistory(src *dynatrace.Source, cluster string) {
	window := r.c.HistoryWindow
	if window <= 0 {
		window = DefaultHistoryWindow
	}
	day := 24 * time.Hour
	end := r.now.Truncate(day).Add(day)
	samples, _, err := src.Aggregate(r.ctx, dynatrace.KeyMemoryWorkingSet, "count", cluster, end.Add(-window-day), end, day)
	if err != nil {
		r.queryFailed(rd.CheckHistory, err)
		return
	}
	have := map[int64]bool{}
	for _, s := range samples {
		if s.V > 0 {
			have[s.T.Unix()] = true
		}
	}
	// days with data, counted back from today without a gap
	days := 0
	for t := end; have[t.Unix()]; t = t.Add(-day) {
		days++
	}
	covered := time.Duration(days) * day
	d := map[string]string{"window": promDuration(window), "history": "<" + promDuration(day)}
	if days > 0 {
		d["history"] = "≥" + promDuration(covered)
	}
	if covered >= window {
		r.add(rd.CheckHistory, rd.StatusPass, "history covers the "+promDuration(window)+" profile window", "", "", d)
		return
	}
	enough := r.now.Add(window - covered)
	d["enoughDataAt"] = enough.UTC().Format(time.RFC3339)
	r.add(rd.CheckHistory, rd.StatusWarn, fmt.Sprintf("history is %s, shorter than the %s profile window: recommendations stay low-confidence until about %s",
		d["history"], promDuration(window), enough.UTC().Format("2006-01-02")), dynatraceRetention, hubapi.RemediationText, d)
}

func (r *run) dtThrottling(counts dynatrace.Counts) {
	if !r.failedQuery(rd.CheckThrottling, counts, dynatrace.KeyThrottlingRatio) {
		return
	}
	n := counts.Series[dynatrace.KeyThrottlingRatio]
	d := map[string]string{"throttlingSeries": strconv.Itoa(n), "throttlingInput": "derived"}
	if n == 0 {
		r.add(rd.CheckThrottling, rd.StatusWarn, dynatrace.KeyThrottlingRatio+" has no series with Kubernetes dimensions for this cluster: the throttled guardrail is blind",
			dynatraceThrottling, hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckThrottling, rd.StatusPass, fmt.Sprintf("derived throttling ratio (%s) for %d containers: Dynatrace has no CFS period counters, so recommendations mark throttling as derived", dynatrace.KeyThrottlingRatio, n), "", "", d)
}

func (r *run) dtRestartsOOM(counts dynatrace.Counts) {
	if !r.failedQuery(rd.CheckRestartsOOM, counts, dynatrace.KeyRestarts, dynatrace.KeyOOMKills) {
		return
	}
	restarts, oom := counts.Series[dynatrace.KeyRestarts], counts.Series[dynatrace.KeyOOMKills]
	d := map[string]string{"restartSeries": strconv.Itoa(restarts), "oomKillSeries": strconv.Itoa(oom)}
	if restarts == 0 && oom == 0 {
		r.add(rd.CheckRestartsOOM, rd.StatusWarn, fmt.Sprintf("no %s or %s series in the last day: either no container restarted or Kubernetes monitoring does not record them; OOM/restart guardrails may be blind", dynatrace.KeyRestarts, dynatrace.KeyOOMKills),
			DynatraceK8sMonitoring, hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckRestartsOOM, rd.StatusPass, fmt.Sprintf("%d restart and %d OOM-kill series in the last day (series exist only while containers restart or are OOM-killed)", restarts, oom), "", "", d)
}

func (r *run) dtNodes(src *dynatrace.Source, cluster string) {
	nodes, err := src.Nodes(r.ctx, cluster, r.now)
	if err != nil {
		r.add(rd.CheckNodeLabels, rd.StatusUnknown, "query failed: "+err.Error(), dynatrace.Hint(err), hubapi.RemediationText, nil)
		r.add(rd.CheckNodeAllocatable, rd.StatusUnknown, "query failed: "+err.Error(), "", "", nil)
		return
	}
	missing := map[string]int{}
	labelled, allocatable := 0, 0
	for _, n := range nodes {
		if n.AllocatableCores > 0 && n.AllocatableMemoryBytes > 0 {
			allocatable++
		}
		if n.InstanceType == "" && n.CapacityType == "" && n.Zone == "" && n.Region == "" {
			if n.AllocatableCores > 0 {
				missing["instance type"]++
				missing["capacity type"]++
				missing["zone"]++
				missing["region"]++
			}
			continue
		}
		labelled++
		if n.InstanceType == "" {
			missing["instance type"]++
		}
		if n.CapacityType == "" {
			missing["capacity type"]++
		}
		if n.Zone == "" {
			missing["zone"]++
		}
		if n.Region == "" && n.Zone == "" {
			missing["region"]++
		}
	}
	r.out.Found.Nodes = len(nodes)
	nd := map[string]string{"nodes": strconv.Itoa(len(nodes))}
	switch {
	case len(nodes) == 0:
		r.add(rd.CheckNodeLabels, rd.StatusWarn, "no nodes of this cluster in Smartscape (K8S_NODE): pricing falls back to default rates", dynatraceNoNodes, hubapi.RemediationText, nil)
	case len(missing) == 0:
		r.add(rd.CheckNodeLabels, rd.StatusPass, fmt.Sprintf("%d nodes with instance type, capacity type, zone and region (Smartscape)", len(nodes)), "", "", nd)
	default:
		var parts []string
		for _, k := range []string{"instance type", "capacity type", "zone", "region"} {
			if missing[k] > 0 {
				parts = append(parts, fmt.Sprintf("%s on %d of %d nodes", k, missing[k], len(nodes)))
				nd[strings.ReplaceAll(k, " ", "")+"Missing"] = strconv.Itoa(missing[k])
			}
		}
		r.add(rd.CheckNodeLabels, rd.StatusWarn, "node labels lack "+strings.Join(parts, ", ")+": pricing falls back to default rates", dynatraceNodeLabels, hubapi.RemediationText, nd)
	}
	if allocatable == 0 {
		r.add(rd.CheckNodeAllocatable, rd.StatusWarn, dynatrace.KeyNodeCPUAlloc+" / "+dynatrace.KeyNodeMemoryAlloc+" missing: the cost split per vCPU/GiB is impossible",
			DynatraceK8sMonitoring, hubapi.RemediationText, nil)
		return
	}
	r.add(rd.CheckNodeAllocatable, rd.StatusPass, fmt.Sprintf("allocatable for %d nodes", allocatable), "", "", map[string]string{"nodes": strconv.Itoa(allocatable)})
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
