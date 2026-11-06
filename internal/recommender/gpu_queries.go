package recommender

import (
	"fmt"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
)

// GPU series. The NVIDIA DCGM exporter reports one series per GPU, labelled with the pod that holds
// it when its Kubernetes mapping is on. kube-state-metrics reports GPU requests as the resource
// `nvidia_com_gpu`. See docs/gpu.md.
const (
	// GPUResource is the `resource` label value of kube_pod_container_resource_requests for
	// nvidia.com/gpu.
	GPUResource = "nvidia_com_gpu"

	DCGMUtilization = "DCGM_FI_DEV_GPU_UTIL"
	DCGMFramebufUse = "DCGM_FI_DEV_FB_USED"
	DCGMFramebufFre = "DCGM_FI_DEV_FB_FREE"

	// GPUPriceMetric is the per-GPU price the pricing service exports.
	GPUPriceMetric = "kerf_gpu_hourly_cost_usd"
)

// DCGMSeries lists the DCGM series the GPU view needs.
var DCGMSeries = []string{DCGMUtilization, DCGMFramebufUse, DCGMFramebufFre}

// dcgm selects a DCGM series of the cluster and maps it to the pod that holds the GPU. A scrape
// without honor_labels renames the exporter's `pod`, `namespace` and `container` labels to
// `exported_*` (the target's own labels win); both variants end up as `pod` and `namespace`.
func (q Queries) dcgm(metric string, wrap func(string) string) string {
	if wrap == nil {
		wrap = func(s string) string { return s }
	}
	raw := q.rel(wrap(metric + q.sel()))
	return fmt.Sprintf(`label_replace(label_replace(%s, "pod", "$1", "exported_pod", "(.+)"), "namespace", "$1", "exported_namespace", "(.+)")`, raw)
}

// GPUPods is every pod that requests GPUs now, with the node it runs on and its GPU count: one series
// per pod with labels cluster, namespace, pod, node, workload_kind, workload. Instant query.
func (q Queries) GPUPods() string {
	perPod := fmt.Sprintf("sum by (cluster, namespace, pod, node) (%s)",
		q.rel("kube_pod_container_resource_requests"+q.sel(fmt.Sprintf("resource=%q", GPUResource))))
	return fmt.Sprintf("max by (cluster, namespace, pod, node, workload_kind, workload) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		perPod, q.PodWorkload(nil, nil))
}

// gpuByWorkload aggregates a per-pod expression (labels cluster, namespace, pod) to the workload.
func (q Queries) gpuByWorkload(agg, perPod string, extraBy string) string {
	by := "cluster, namespace, workload_kind, workload"
	if extraBy != "" {
		by += ", " + extraBy
	}
	return fmt.Sprintf("%s by (%s) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		agg, by, perPod, q.PodWorkload(nil, nil))
}

// GPUUtilization is the GPU utilisation in percent per workload and step: the mean over each pod's
// GPUs, then over the workload's pods.
func (q Queries) GPUUtilization() string {
	return q.gpuByWorkload("avg", fmt.Sprintf("avg by (cluster, namespace, pod) (%s)", q.dcgm(DCGMUtilization, nil)), "")
}

// GPUMemoryUsed is the framebuffer used in MiB per workload and step: the busiest GPU of each pod,
// then the busiest pod.
func (q Queries) GPUMemoryUsed() string {
	return q.gpuByWorkload("max", fmt.Sprintf("max by (cluster, namespace, pod) (%s)", q.dcgm(DCGMFramebufUse, nil)), "")
}

// GPUMemoryTotal is the framebuffer size of one GPU in MiB (used plus free) per workload and GPU
// model, over the lookback. Instant query.
func (q Queries) GPUMemoryTotal(lookback time.Duration) string {
	w := lastOver(lookback)
	total := fmt.Sprintf("%s + %s", q.dcgm(DCGMFramebufUse, w), q.dcgm(DCGMFramebufFre, w))
	perPod := fmt.Sprintf("max by (cluster, namespace, pod, modelName) (%s)", total)
	return q.gpuByWorkload("max", perPod, "modelName")
}

// GPUPrice returns the hourly price of one GPU per node (label node). The pricing service exports it
// to the hub's own metrics store, so this query ignores the binding and runs against local.
func (q Queries) GPUPrice(lookback time.Duration) string {
	return fmt.Sprintf("max by (cluster, node) (%s)", lastOver(lookback)(GPUPriceMetric+datasource.DefaultMatcher(q.Cluster).Sel()))
}
