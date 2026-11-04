import { ArrowRightIcon, Loader2Icon, TriangleAlertIcon } from 'lucide-react'
import { Link } from 'react-router'
import type { ImpactPreview as Preview, Mode } from '@/api/types'
import { ModeBadge } from '@/components/domain'
import { Callout } from '@/components/ui/form'
import { ErrorState } from '@/components/states'
import { formatNumber, formatUSD } from '@/lib/format'
import { workloadPath } from '@/lib/workload-id'
import { cn } from '@/lib/utils'

function ModeArrow({ from, to }: { from: Mode; to: Mode }) {
  if (from === to) return <ModeBadge mode={to} />
  return (
    <span className="inline-flex items-center gap-1">
      <ModeBadge mode={from} />
      <ArrowRightIcon className="size-3 text-muted-foreground" />
      <ModeBadge mode={to} />
    </span>
  )
}

/** The dry-run result of `POST /v1/admin/preview`: what a change would do before it is saved. */
export function ImpactPreview({
  preview,
  loading,
  error,
  compact,
}: {
  preview?: Preview
  loading?: boolean
  error?: unknown
  compact?: boolean
}) {
  if (error) return <ErrorState error={error} compact />
  if (loading || !preview)
    return (
      <div
        className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-[13px] text-muted-foreground"
        data-testid="impact-loading"
      >
        <Loader2Icon className="size-4 animate-spin" /> Calculating the impact…
      </div>
    )
  const into = preview.savingsIntoAutoPatchUSD.total
  const out = preview.savingsOutOfAutoPatchUSD.total
  return (
    <div className="flex flex-col gap-3" data-testid="impact-preview">
      <div className="grid grid-cols-3 gap-2">
        <Stat
          label="Workloads changing"
          value={formatNumber(preview.workloadsChanged)}
          sub={`of ${formatNumber(preview.workloadsEvaluated)} evaluated`}
          testId="impact-changed"
        />
        <Stat
          label="Savings into auto-patch"
          value={formatUSD(into)}
          sub="per month"
          tone={into > 0 ? 'success' : undefined}
          testId="impact-into"
        />
        <Stat
          label="Savings out of auto-patch"
          value={formatUSD(out)}
          sub="per month"
          tone={out > 0 ? 'warning' : undefined}
        />
      </div>
      {preview.warnings?.length ? (
        <Callout tone="warning" icon={TriangleAlertIcon}>
          <ul className="list-disc pl-4">
            {preview.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </Callout>
      ) : null}
      {preview.items.length ? (
        <div className={cn('overflow-auto rounded-lg border', compact ? 'max-h-56' : 'max-h-80')}>
          <table className="w-full text-[13px]">
            <thead className="sticky top-0 bg-muted/80 text-xs text-muted-foreground backdrop-blur">
              <tr>
                <th className="px-3 py-1.5 text-left font-medium">Workload</th>
                <th className="px-3 py-1.5 text-left font-medium">CPU</th>
                <th className="px-3 py-1.5 text-left font-medium">Memory</th>
                <th className="px-3 py-1.5 text-right font-medium">Savings/mo</th>
              </tr>
            </thead>
            <tbody>
              {preview.items.map((it) => (
                <tr key={it.workloadId} className="border-t">
                  <td className="max-w-[16rem] px-3 py-1.5">
                    <Link
                      to={workloadPath(it.workloadId)}
                      className="block truncate font-medium hover:underline"
                      target="_blank"
                    >
                      {it.name}
                    </Link>
                    <div className="truncate text-xs text-muted-foreground">
                      {it.cluster} · {it.namespace}
                      {it.policyAfter ? ` · ${it.policyAfter}` : ''}
                    </div>
                  </td>
                  <td className="px-3 py-1.5">
                    <ModeArrow from={it.before.cpu} to={it.after.cpu} />
                  </td>
                  <td className="px-3 py-1.5">
                    <ModeArrow from={it.before.memory} to={it.after.memory} />
                  </td>
                  <td className="px-3 py-1.5 text-right tabular">{formatUSD(it.monthlySavingsUSD?.total)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {preview.truncated ? (
            <div className="border-t px-3 py-1.5 text-xs text-muted-foreground">
              Showing the {preview.items.length} with the most savings; the totals cover every workload.
            </div>
          ) : null}
        </div>
      ) : (
        <p className="rounded-lg border border-dashed px-4 py-4 text-center text-[13px] text-muted-foreground">
          No workload changes its effective mode. Other settings (guardrails, windows) still apply on the next
          pull.
        </p>
      )}
    </div>
  )
}

function Stat({
  label,
  value,
  sub,
  tone,
  testId,
}: {
  label: string
  value: string
  sub: string
  tone?: 'success' | 'warning'
  testId?: string
}) {
  return (
    <div className="rounded-lg border bg-card px-3 py-2.5" data-testid={testId}>
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div
        className={cn(
          'mt-0.5 text-lg font-semibold tabular',
          tone === 'success' && 'text-success',
          tone === 'warning' && 'text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]',
        )}
      >
        {value}
      </div>
      <div className="text-[11px] text-muted-foreground">{sub}</div>
    </div>
  )
}
