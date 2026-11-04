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
    'recommendation_id',
    'generated_at',
  ]
  const rows: unknown[][] = []
  for (const w of items) {
    const base = [w.cluster, w.namespace, w.kind, w.name]
    const tail = [
      w.confidence,
      (w.riskFlags ?? []).join(' '),
      undefined,
      w.monthlySavingsUSD?.total,
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
