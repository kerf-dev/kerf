package readiness

import (
	"fmt"
	"strconv"
	"strings"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// dcgm checks the NVIDIA DCGM exporter series behind the GPU view. A cluster without GPU nodes or
// GPU requests passes: there is nothing to measure.
func (r *run) dcgm() {
	// GPUs of the cluster: nodes that advertise nvidia.com/gpu and pods that request it.
	nodes, _, err := r.value(r.c.Q, fmt.Sprintf("count(count by (node) (last_over_time(kube_node_status_allocatable%s[1h]) > 0))", r.sel(fmt.Sprintf("resource=%q", recommender.GPUResource))))
	if err != nil {
		r.queryFailed(rd.CheckDCGM, err)
		return
	}
	reqSel := r.sel(fmt.Sprintf("resource=%q", recommender.GPUResource))
	pods, _, err := r.value(r.c.Q, fmt.Sprintf("count(max by (namespace, pod) (last_over_time(kube_pod_container_resource_requests%s[1h]) > 0))", reqSel))
	if err != nil {
		r.queryFailed(rd.CheckDCGM, err)
		return
	}
	d := map[string]string{"gpuNodes": strconv.Itoa(int(nodes)), "gpuPods": strconv.Itoa(int(pods))}
	if nodes == 0 && pods == 0 {
		r.add(rd.CheckDCGM, rd.StatusPass, "no GPU nodes or pods that request GPUs: nothing to measure", "", "", d)
		return
	}
	var missing []string
	for _, m := range recommender.DCGMSeries {
		n, err := r.count(m)
		if err != nil {
			r.queryFailed(rd.CheckDCGM, err)
			return
		}
		if n == 0 {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		d["missing"] = strings.Join(missing, ",")
		r.add(rd.CheckDCGM, rd.StatusWarn,
			fmt.Sprintf("GPUs found (%d nodes, %d pods) but these NVIDIA DCGM exporter series are missing: %s", int(nodes), int(pods), strings.Join(missing, ", ")),
			r.dcgmRemediation(), hubapi.RemediationText, d)
		return
	}
	d["series"] = strings.Join(recommender.DCGMSeries, ",")
	if pods == 0 {
		r.add(rd.CheckDCGM, rd.StatusPass, "DCGM series present; no pod requests GPUs right now", "", "", d)
		return
	}
	// The series must carry the labels of the pods that hold the GPUs (Kubernetes pod mapping of the
	// exporter); the exporter's own pod labels do not count.
	util := fmt.Sprintf(`label_replace(label_replace(last_over_time(%s%s[1h]), "pod", "$1", "exported_pod", "(.+)"), "namespace", "$1", "exported_namespace", "(.+)")`, recommender.DCGMUtilization, r.sel())
	covered, _, err := r.value(r.c.Q, fmt.Sprintf("count(max by (namespace, pod) (%s) and on (namespace, pod) max by (namespace, pod) (last_over_time(kube_pod_container_resource_requests%s[1h]) > 0))", util, reqSel))
	if err != nil {
		r.queryFailed(rd.CheckDCGM, err)
		return
	}
	d["coveredPods"] = strconv.Itoa(int(covered))
	if covered == 0 {
		r.add(rd.CheckDCGM, rd.StatusWarn,
			fmt.Sprintf("DCGM series are present but none carries the namespace and pod of the %d pods that request GPUs: the exporter's Kubernetes pod mapping is off or its labels are renamed", int(pods)),
			r.dcgmRemediation(), hubapi.RemediationText, d)
		return
	}
	r.add(rd.CheckDCGM, rd.StatusPass, fmt.Sprintf("DCGM series for %d of %d pods that request GPUs", int(covered), int(pods)), "", "", d)
}

func (r *run) dcgmRemediation() string {
	s := "Run the NVIDIA DCGM exporter on the GPU nodes (it is part of the NVIDIA GPU Operator; or the dcgm-exporter Helm chart) with Kubernetes pod mapping on (DCGM_EXPORTER_KUBERNETES=true), " +
		"scrape it into the cluster's datasource and keep DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_FB_USED and DCGM_FI_DEV_FB_FREE in its counters file. " +
		"If the scrape renames the exporter's pod labels to exported_pod and exported_namespace, that is fine."
	if b := strings.TrimRight(r.c.DocsBase, "/"); b != "" {
		s += " See " + b + "/gpu."
	}
	return s
}
