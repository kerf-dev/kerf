import { useId } from 'react'
import type { DatasourceMetricStatus } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { formatNumber } from '@/lib/format'

const STATUS: Record<
  DatasourceMetricStatus['status'],
  { label: string; variant: 'success' | 'destructive' | 'muted' }
> = {
  found: { label: 'found', variant: 'success' },
  missing: { label: 'missing', variant: 'destructive' },
  notAvailable: { label: 'not available', variant: 'muted' },
}

/**
 * How a datasource that maps its own metrics (Dynatrace) answers each input Kerf needs: where it is
 * read from and whether series exist. The summary line names what is missing and what the
 * datasource cannot provide.
 */
export function MetricMapping({ metrics }: { metrics: DatasourceMetricStatus[] }) {
  const id = useId()
  const missing = metrics.filter((m) => m.status === 'missing').map((m) => m.sourceMetric ?? m.input)
  const unavailable = metrics.filter((m) => m.status === 'notAvailable').map((m) => m.input)
  return (
    <section aria-labelledby={id} className="flex flex-col gap-2" data-testid="metric-mapping">
      <h4 id={id} className="text-[13px] font-semibold">
        Metric mapping
      </h4>
      <div className="flex flex-col gap-0.5 text-[13px]">
        {missing.length ? (
          <p data-testid="metric-mapping-missing">
            <span className="font-medium text-destructive">Missing:</span>{' '}
            <span className="font-mono text-xs">{missing.join(', ')}</span>
          </p>
        ) : (
          <p className="text-muted-foreground">Every metric Kerf needs from this datasource has series.</p>
        )}
        {unavailable.length ? (
          <p className="text-muted-foreground" data-testid="metric-mapping-unavailable">
            <span className="font-medium">Not available in this datasource:</span>{' '}
            <span className="font-mono text-xs">{unavailable.join(', ')}</span>
          </p>
        ) : null}
      </div>
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-left text-[13px]">
          <thead className="bg-muted/40 text-xs text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-1.5 font-medium">
                Kerf input
              </th>
              <th scope="col" className="px-3 py-1.5 font-medium">
                Dynatrace metric
              </th>
              <th scope="col" className="px-3 py-1.5 font-medium">
                Used for
              </th>
              <th scope="col" className="px-3 py-1.5 font-medium">
                Status
              </th>
              <th scope="col" className="px-3 py-1.5 text-right font-medium">
                Series
              </th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {metrics.map((m) => (
              <tr key={m.input} data-status={m.status}>
                <th scope="row" className="px-3 py-1.5 align-top font-mono text-xs font-normal break-all">
                  {m.input}
                </th>
                <td className="px-3 py-1.5 align-top font-mono text-xs break-all">{m.sourceMetric ?? '—'}</td>
                <td className="px-3 py-1.5 align-top">
                  {m.usedFor ?? '—'}
                  {m.note ? <div className="text-xs text-muted-foreground">{m.note}</div> : null}
                </td>
                <td className="px-3 py-1.5 align-top">
                  <Badge variant={STATUS[m.status].variant}>{STATUS[m.status].label}</Badge>
                </td>
                <td className="tabular px-3 py-1.5 text-right align-top">
                  {m.series != null ? formatNumber(m.series) : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}
