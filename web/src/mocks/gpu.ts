/**
 * GPU data of the mock fleet: a handful of named workloads with every GPU status. The numbers follow
 * the thresholds of the contract (idle: p95 utilisation < 5 %, under-used: < 40 % or memory < 30 %).
 * Prices are per GPU node and month (730 h) of the instance types noted next to each model.
 */
import type { GPUUsage } from '@/api/types'

const GiB = 1024 ** 3
const WINDOW = '336h0m0s'
const round2 = (n: number) => Math.round(n * 100) / 100

interface Fixture {
  /** `cluster/namespace/Kind/name` */
  id: string
  model: string
  gpusPerPod: number
  pods: number
  memTotalGiB: number
  /** Monthly price of one GPU; undefined: no price known. */
  gpuPrice?: number
  util?: { p95: number; avg: number }
  memP95GiB?: number
  status: GPUUsage['status']
  suggestions: (g: { monthly: number; model: string }) => GPUUsage['suggestions']
}

const FIXTURES: Fixture[] = [
  {
    // ok: a model server that keeps its GPUs busy
    id: 'prod-eu-west-1/risk/Deployment/risk-model-server',
    model: 'NVIDIA A10G', // g5.xlarge
    gpusPerPod: 1,
    pods: 3,
    memTotalGiB: 24,
    gpuPrice: 734.38,
    util: { p95: 82.4, avg: 61.3 },
    memP95GiB: 17.6,
    status: 'ok',
    suggestions: () => [],
  },
  {
    // under-used by memory: plenty of utilisation, a fraction of the framebuffer
    id: 'prod-eu-west-1/kyc/Deployment/document-ocr',
    model: 'NVIDIA A10G',
    gpusPerPod: 1,
    pods: 2,
    memTotalGiB: 24,
    gpuPrice: 734.38,
    util: { p95: 54.2, avg: 37.9 },
    memP95GiB: 4.2,
    status: 'under-used',
    suggestions: () => [
      {
        kind: 'smaller-gpu',
        targetModel: 'NVIDIA T4',
        message:
          'The busiest GPU of a pod uses 4.2 GiB of its 24 GiB at the 95th percentile. A 16 GiB GPU fits and costs less.',
        monthlySavingsUSD: 700.3,
      },
    ],
  },
  {
    // idle: nothing runs on the GPUs
    id: 'prod-eu-west-1/fraud-detection/Deployment/device-fingerprint',
    model: 'NVIDIA A100-SXM4-40GB', // p4d.24xlarge / 8
    gpusPerPod: 1,
    pods: 2,
    memTotalGiB: 40,
    gpuPrice: 2992.2,
    util: { p95: 1.8, avg: 0.6 },
    memP95GiB: 1.1,
    status: 'idle',
    suggestions: ({ monthly }) => [
      {
        kind: 'scale-to-zero',
        message:
          'The GPUs were idle (p95 utilisation 1.8 %) for the whole window. Scale the workload to zero when it is not needed.',
        monthlySavingsUSD: monthly,
      },
      {
        kind: 'time-slicing',
        message: 'If it must stay up, share one GPU between the pods with time-slicing.',
      },
    ],
  },
  {
    // under-used by utilisation
    id: 'prod-us-east-1/kyc/Deployment/liveness-check',
    model: 'NVIDIA L4', // g6.xlarge
    gpusPerPod: 1,
    pods: 4,
    memTotalGiB: 24,
    gpuPrice: 587.4,
    util: { p95: 27.1, avg: 14.2 },
    memP95GiB: 15.2,
    status: 'under-used',
    suggestions: () => [
      {
        kind: 'time-slicing',
        message: 'Four pods each hold a GPU at 27 % utilisation (p95). Time-slicing lets them share GPUs.',
      },
      {
        kind: 'mig',
        message: 'On MIG-capable GPUs, partition one GPU per pod instead of dedicating whole GPUs.',
      },
    ],
  },
  {
    // no data: DCGM series are missing and no node price is known
    id: 'prod-us-east-1/risk/Deployment/risk-model-server-replay',
    model: '',
    gpusPerPod: 1,
    pods: 2,
    memTotalGiB: 0,
    status: 'no-data',
    suggestions: () => [],
  },
]

function build(f: Fixture): GPUUsage {
  const gpus = f.gpusPerPod * f.pods
  const priced = f.gpuPrice != null
  const monthly = priced ? round2(f.gpuPrice! * gpus) : 0
  const noData = f.status === 'no-data'
  return {
    status: f.status,
    gpusPerPod: f.gpusPerPod,
    gpus,
    pods: f.pods,
    ...(f.model ? { model: f.model } : {}),
    window: WINDOW,
    samples: noData ? 0 : 40_320,
    ...(f.util ? { utilizationP95: f.util.p95, utilizationAvg: f.util.avg } : {}),
    ...(f.memP95GiB != null
      ? {
          memoryUsedP95Bytes: Math.round(f.memP95GiB * GiB),
          memoryTotalBytes: f.memTotalGiB * GiB,
          memoryUtilizationP95: round2((f.memP95GiB / f.memTotalGiB) * 100),
        }
      : {}),
    priced,
    monthlyCostUSD: monthly,
    perPodMonthlyCostUSD: priced ? round2(monthly / f.pods) : 0,
    unusedMonthlyCostUSD: priced ? round2(monthly * (1 - (f.util?.avg ?? 0) / 100)) : 0,
    suggestions: f.suggestions({ monthly, model: f.model }),
  }
}

/** GPU usage by workload id. */
export const GPU_BY_WORKLOAD: ReadonlyMap<string, GPUUsage> = new Map(FIXTURES.map((f) => [f.id, build(f)]))
