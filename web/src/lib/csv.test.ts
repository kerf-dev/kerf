import { describe, expect, it } from 'vitest'
import type { GPUUsage, WorkloadRow } from '@/api/types'
import { workloadsToCsv } from './csv'

const gpu = (patch: Partial<GPUUsage> = {}): GPUUsage => ({
  status: 'under-used',
  gpusPerPod: 1,
  gpus: 2,
  pods: 2,
  samples: 100,
  utilizationP95: 27.1,
  priced: true,
  monthlyCostUSD: 1500,
  perPodMonthlyCostUSD: 750,
  unusedMonthlyCostUSD: 1200,
  suggestions: [],
  ...patch,
})

const row = (g?: GPUUsage): WorkloadRow => ({
  id: 'c/ns/Deployment/w',
  cluster: 'c',
  namespace: 'ns',
  kind: 'Deployment',
  name: 'w',
  modes: { cpu: 'audit', memory: 'audit' },
  phase: 'Pending',
  ...(g ? { gpu: g } : {}),
})

function parse(csv: string) {
  const [head, ...lines] = csv.trim().split('\n')
  const keys = head!.split(',')
  return lines.map((l) => Object.fromEntries(l.split(',').map((v, i) => [keys[i], v])))
}

describe('workloadsToCsv GPU columns', () => {
  it('exports status, GPU count, p95 utilisation and costs', () => {
    const [r] = parse(workloadsToCsv([row(gpu())]))
    expect(r).toMatchObject({
      gpu_status: 'under-used',
      gpus: '2',
      gpu_utilization_p95_percent: '27.1',
      gpu_monthly_cost_usd: '1500',
      gpu_unused_monthly_cost_usd: '1200',
    })
  })
  it('leaves the costs empty when no GPU price is known', () => {
    const [r] = parse(
      workloadsToCsv([
        row(
          gpu({
            status: 'no-data',
            priced: false,
            utilizationP95: undefined,
            monthlyCostUSD: 0,
            unusedMonthlyCostUSD: 0,
          }),
        ),
      ]),
    )
    expect(r).toMatchObject({
      gpu_status: 'no-data',
      gpu_utilization_p95_percent: '',
      gpu_monthly_cost_usd: '',
      gpu_unused_monthly_cost_usd: '',
    })
  })
  it('leaves the GPU columns empty for workloads without GPUs', () => {
    const [r] = parse(workloadsToCsv([row()]))
    expect(r).toMatchObject({ gpu_status: '', gpus: '', gpu_monthly_cost_usd: '' })
  })
})
