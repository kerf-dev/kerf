import type { WorkloadRow } from '@/api/types'

function cell(v: unknown): string {
  if (v == null) return ''
  const s = String(v)
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function toCsv(headers: string[], rows: unknown[][]): string {
  return [headers, ...rows].map((r) => r.map(cell).join(',')).join('\n') + '\n'
}

/** One row per container so requests/limits stay comparable in a spreadsheet. */
export function workloadsToCsv(items: WorkloadRow[]): string {
  const headers = [
    'cluster',
    'namespace',
    'kind',
    'workload',
    'container',
    'cpu_mode',
    'memory_mode',
    'phase',
    'cpu_request_current',
    'cpu_request_recommended',
    'cpu_limit_current',
    'cpu_limit_recommended',
    'memory_request_current',
    'memory_request_recommended',
    'memory_limit_current',
    'memory_limit_recommended',
    'confidence',
    'risk_flags',
    'container_savings_usd_month',
    'workload_savings_usd_month',
    'gpu_status',
    'gpus',
    'gpu_utilization_p95_percent',
    'gpu_monthly_cost_usd',
    'gpu_unused_monthly_cost_usd',
    'recommendation_id',
    'generated_at',
  ]
  const rows: unknown[][] = []
  for (const w of items) {
    const base = [w.cluster, w.namespace, w.kind, w.name]
    // GPU data belongs to the workload, so it repeats on every container row like the workload savings.
    // Costs stay empty when no GPU price is known (0 would read as free).
    const gpu = [
      w.gpu?.status,
      w.gpu?.gpus,
      w.gpu?.utilizationP95,
      w.gpu?.priced ? w.gpu.monthlyCostUSD : undefined,
      w.gpu?.priced ? w.gpu.unusedMonthlyCostUSD : undefined,
    ]
    const tail = [
      w.confidence,
      (w.riskFlags ?? []).join(' '),
      undefined,
      w.monthlySavingsUSD?.total,
      ...gpu,
      w.recommendationID,
      w.generatedAt,
    ]
    if (!w.containers?.length) {
      rows.push([...base, '', w.modes.cpu, w.modes.memory, w.phase, '', '', '', '', '', '', '', '', ...tail])
      continue
    }
    for (const c of w.containers) {
      rows.push([
        ...base,
        c.name,
        w.modes.cpu,
        w.modes.memory,
        w.phase,
        c.current.requests?.cpu,
        c.recommended.requests?.cpu,
        c.current.limits?.cpu,
        c.recommended.limits?.cpu,
        c.current.requests?.memory,
        c.recommended.requests?.memory,
        c.current.limits?.memory,
        c.recommended.limits?.memory,
        w.confidence,
        (c.riskFlags ?? []).join(' '),
        c.monthlySavingsUSD?.total,
        w.monthlySavingsUSD?.total,
        ...gpu,
        w.recommendationID,
        w.generatedAt,
      ])
    }
  }
  return toCsv(headers, rows)
}

export function download(filename: string, content: string, type = 'text/csv;charset=utf-8') {
  const url = URL.createObjectURL(new Blob([content], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
