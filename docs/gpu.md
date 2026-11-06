# GPU visibility

Kerf shows which GPU workloads leave their GPUs idle or under-used, what those GPUs cost, and which direction to
look in. It is visibility only: Kerf never changes a GPU request, a MIG configuration or a node pool.

A GPU is a whole device (`nvidia.com/gpu: 1`), so the CPU and memory method does not apply. The useful answer is "this
GPU is mostly idle", or "this workload would fit a smaller GPU or a MIG slice".

NVIDIA GPUs only. AMD and Intel GPUs are not read.

## What you see

- **Workload page, GPU panel:** status, GPUs per pod and in total, GPU model, utilisation (95th percentile and average),
  memory used against the GPU's memory, the monthly cost of the workload's GPUs and of one pod, the part of it that
  went unused, and the suggestions.
- **Opportunities:** a **GPU** filter (any GPU workload, idle, under-used, ok, no data) and a GPU column.
- **API:** `GET /v1/workloads?gpu=idle&gpu=under-used` and the `gpu` field of a workload row and of
  `recommendation` (schema `GPUUsage` in [api/openapi/hub.yaml](../api/openapi/hub.yaml)). `GET /v1/workloads/facets`
  counts the workloads per GPU status.
- **Readiness:** the check `metrics.dcgm` of the cluster says which DCGM series are missing.

## Prerequisites

1. **The NVIDIA DCGM exporter** on the GPU nodes (part of the
   [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/index.html), or the
   `dcgm-exporter` Helm chart), scraped into the datasource the cluster is bound to. It must:
   - export `DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_FB_USED` and `DCGM_FI_DEV_FB_FREE` (they are in the default counters
     file);
   - map GPUs to pods (Kubernetes pod mapping, `DCGM_EXPORTER_KUBERNETES=true`; the GPU Operator turns it on), so each
     series carries `namespace` and `pod`.

   A scrape without `honorLabels` renames the exporter's `pod` and `namespace` to `exported_pod` and
   `exported_namespace`. Kerf handles both spellings.
2. **kube-state-metrics** with the usual series, and `kube_pod_container_resource_requests` (it reports GPU requests as
   `resource="nvidia_com_gpu"`) and `kube_node_status_allocatable`.
3. **GPU prices:** the pricing service prices GPU nodes from the AWS price list, which includes the GPU count of every
   instance type, or from the static price file (`gpus:` in `instanceTypes`, see `dev/prices.yaml`). The nodes need the
   labels described in the [install guide](install/eks.md) (instance type, capacity type, region, zone). Without a GPU
   price the cost shows as unknown (`priced: false`); the utilisation and the status still work.

## Method

Per workload, over the recommender window (the longest profile window in use on the cluster, 7 days with the default
profile):

1. **Which workloads:** those with pods that request `nvidia.com/gpu` now. The GPUs of a pod are the sum over its
   containers.
2. **Utilisation:** the mean of `DCGM_FI_DEV_GPU_UTIL` over the GPUs of each pod, then over the pods of the workload, at
   every step. Kerf reports the 95th percentile and the average of that series.
3. **Memory:** the busiest GPU of each pod, then the busiest pod: `DCGM_FI_DEV_FB_USED` at every step, 95th percentile.
   The GPU's memory is `DCGM_FI_DEV_FB_USED + DCGM_FI_DEV_FB_FREE`.
4. **Status:**

   | Status | When |
   |---|---|
   | `idle` | utilisation p95 < 5 % |
   | `under-used` | utilisation p95 < 40 %, or memory p95 < 30 % of the GPU's memory |
   | `ok` | neither |
   | `no-data` | the workload requests GPUs, but DCGM has no utilisation series for its pods |

5. **Cost:** the price of the GPU node times the workload's share of the node's GPUs. The pricing service exports
   `kerf_gpu_hourly_cost_usd` (the node's hourly price divided by the GPU count of the instance type) and `kerf_node_gpus`
   for nodes with GPUs. The workload's cost is the sum over its pods of the GPUs they request times the price of the
   node they run on, times 730 hours. The hub's pricing discount is applied. The cost of the GPUs held now is shown, not
   the cost over the window. *Unused cost* is that cost times the share of GPU time not used (100 % minus the average
   utilisation).

   The whole node price is attributed to its GPUs, as the GPU is what the node is bought for; CPU and memory savings
   keep their own rates. The GPU cost is not part of a workload's CPU and memory savings.
6. **Suggestions** (nothing is applied):

   | Kind | Shown for | Means |
   |---|---|---|
   | `scale-to-zero` | idle | Scale the workload down while it has nothing to do (a schedule, or event-driven scaling). Shows the monthly cost as the savings. |
   | `time-slicing` | under-used by utilisation | Share one GPU between several pods with NVIDIA time-slicing. |
   | `mig` | under-used by utilisation, on a GPU that supports Multi-Instance GPU (A100, A30, H100, H200, B200) | Give the workload a MIG slice instead of a whole GPU. |
   | `smaller-gpu` | under-used | The 95th percentile of the memory used, with 25 % headroom, fits a smaller GPU type (T4, L4, A10G, A100 40GB, L40S, A100 80GB, H100). Check that its compute is enough. |

## Limits

- A time-sliced or MIG-partitioned GPU that pods request as `nvidia.com/gpu` replicas is priced as whole GPUs: the cost
  of such a workload is overstated. MIG resources (`nvidia.com/mig-*`) are not read.
- A workload whose GPUs came and went inside the window is judged on the steps with data.
- Spot and on-demand nodes use their own prices, like the CPU and memory rates.
- The thresholds are fixed in this version.

## Troubleshooting

`GET /v1/clusters/{cluster}/readiness` (or the cluster page) shows `metrics.dcgm`:

| Message | What to do |
|---|---|
| no GPU nodes or pods that request GPUs | Nothing: the cluster has no GPUs. |
| these NVIDIA DCGM exporter series are missing: … | Install or scrape the DCGM exporter and keep the three counters in its counters file. `details.missing` lists them. |
| DCGM series are present but none carries the namespace and pod … | Turn on the exporter's Kubernetes pod mapping; check that the scrape does not drop the pod labels. |

A workload shows `no-data` when it requests GPUs but its pods have no DCGM series: check the same things, and that the
exporter runs on the node the pod runs on.
