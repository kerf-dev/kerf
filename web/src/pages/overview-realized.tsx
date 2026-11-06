/**
 * Overview: realized savings (what the applied changes actually saved) and the node cost trend.
 * Realized amounts are never added to the estimated ones, and never share their label.
 */
import { BadgeCheckIcon, ExternalLinkIcon, InfoIcon, ServerIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useClusterCapacity } from '@/api/admin'
import { useNodeCostTrend, useRealizedSavings, type TimeRange } from '@/api/queries'
import type { ClusterSummary, RealizedSavings } from '@/api/types'
import { Chart } from '@/components/charts/chart'
import { alpha, axisStyle, baseOption, useChartColors } from '@/components/charts/theme'
import { EmptyState, ErrorState } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { NativeSelect } from '@/components/ui/form'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatBytes, formatCores, formatDateTime, formatPercent, formatUSD } from '@/lib/format'
import { realizedLabel } from '@/lib/realized'

/** Hours per month of the estimate's monthly costs (as in the hub). */
const HOURS_PER_MONTH = 730

const monthOnly = new Intl.DateTimeFormat('en-GB', { month: 'short', year: '2-digit', timeZone: 'UTC' })
const dayMonth = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' })

/** Coverage caveats of a realized read, or undefined when every hour was measured. */
function coverageNote(r: RealizedSavings): string | undefined {
  const c = r.coverage
  const parts: string[] = []
  if (c.replicasFromRecommendations)
    parts.push(
      `${c.replicasFromRecommendations} workload-hours used the recommendation's replicas (no metrics)`,
    )
  if (c.withoutReplicas) parts.push(`${c.withoutReplicas} workload-hours had no replica data and count zero`)
  if (c.defaultPricing) parts.push(`${c.defaultPricing} workload-hours used the default rates`)
  return parts.length ? parts.join('; ') + '.' : undefined
}

export function RealizedCard({ className }: { className?: string }) {
  const [step, setStep] = useState<'month' | 'day'>('month')
  const q = useRealizedSavings(step)
  const c = useChartColors()
  const r = q.data
  const option = useMemo(() => {
    const base = baseOption(c)
    // By month, start at the first month with a saving (at least six bars), not at a year of zeros.
    let pts = r?.points ?? []
    if (step === 'month') {
      const first = pts.findIndex((p) => p.total !== 0)
      pts = pts.slice(Math.max(0, Math.min(first < 0 ? pts.length : first, pts.length - 6)))
    }
    const fmt = step === 'month' ? monthOnly : dayMonth
    return {
      ...base,
      tooltip: {
        ...base.tooltip,
        axisPointer: { type: 'shadow' as const, shadowStyle: { color: alpha(c['muted-foreground'], 0.08) } },
        valueFormatter: (v: number) => formatUSD(v, { cents: true }),
      },
      legend: { show: false },
      xAxis: {
        type: 'category',
        data: pts.map((p) => fmt.format(Date.parse(p.time))),
        ...axisStyle(c),
        splitLine: { show: false },
      },
      yAxis: {
        type: 'value',
        ...axisStyle(c),
        axisLabel: { ...axisStyle(c).axisLabel, formatter: (v: number) => formatUSD(v, { compact: true }) },
      },
      series: [
        { name: 'CPU', key: 'cpu' as const, color: c['chart-1'] },
        { name: 'Memory', key: 'memory' as const, color: c['chart-2'] },
      ].map((s) => ({
        name: s.name,
        type: 'bar',
        stack: 'realized',
        barMaxWidth: step === 'month' ? 28 : 12,
        itemStyle: { color: s.color, borderRadius: s.key === 'memory' ? [3, 3, 0, 0] : 0 },
        emphasis: { focus: 'series' },
        data: pts.map((p) => p[s.key]),
      })),
    }
  }, [r, c, step])
  const note = r ? coverageNote(r) : undefined
  return (
    <Card className={className} data-testid="realized-card">
      <CardHeader>
        <div>
          <CardTitle className="flex items-center gap-1.5">
            <BadgeCheckIcon className="size-4 text-success" />
            Realized savings
          </CardTitle>
          <CardDescription>
            Requests released by applied changes × replicas that ran × node rates of the time
          </CardDescription>
        </div>
        <Segmented
          label="Realized savings resolution"
          value={step}
          onChange={setStep}
          options={[
            { value: 'month', label: 'Monthly' },
            { value: 'day', label: 'Daily' },
          ]}
        />
      </CardHeader>
      <CardContent className="pt-3">
        {q.isError ? (
          <ErrorState compact error={q.error} onRetry={() => q.refetch()} />
        ) : !r ? (
          <div className="space-y-3">
            <Skeleton className="h-8 w-40" />
            <Skeleton className="h-52 w-full" />
          </div>
        ) : !r.since ? (
          <EmptyState
            icon={BadgeCheckIcon}
            title="Nothing realized yet"
            description="Realized savings start when Kerf applies a change (auto-patch or Approve once). Until then every amount on this page is an estimate."
            className="py-12"
          />
        ) : (
          <>
            <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
              <div>
                <div className="text-[13px] font-medium text-muted-foreground" data-testid="realized-label">
                  {realizedLabel(r.since)}
                </div>
                <div
                  className="mt-1 text-[28px] leading-none font-semibold tracking-tight text-success tabular"
                  data-testid="realized-total"
                >
                  {formatUSD(r.total.total, { cents: r.total.total < 100 })}
                </div>
                <div className="mt-1.5 flex gap-3 text-xs text-muted-foreground tabular">
                  <span className="flex items-center gap-1.5">
                    <span className="size-1.5 rounded-full bg-chart-1" />
                    CPU {formatUSD(r.total.cpu, { compact: true })}
                  </span>
                  <span className="flex items-center gap-1.5">
                    <span className="size-1.5 rounded-full bg-chart-2" />
                    Memory {formatUSD(r.total.memory, { compact: true })}
                  </span>
                </div>
              </div>
              <div className="text-right text-xs text-muted-foreground">
                <div>Released now</div>
                <div
                  className="mt-0.5 font-mono text-[13px] text-foreground tabular"
                  data-testid="released-now"
                >
                  {formatCores(r.releasedNow.cpuCores)} cores · {formatBytes(r.releasedNow.memoryBytes, 1)}
                </div>
                {r.computedThrough ? (
                  <div className="mt-0.5">up to {formatDateTime(r.computedThrough, true)} UTC</div>
                ) : null}
              </div>
            </div>
            <Chart
              option={option}
              className="mt-3 h-52"
              ariaLabel={step === 'month' ? 'Realized savings by month' : 'Realized savings by day'}
            />
            {note ? (
              <p className="mt-2 flex items-start gap-1.5 text-[11px] text-muted-foreground">
                <InfoIcon className="mt-px size-3 shrink-0" />
                {note}
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function NodeCostCard({ clusters, className }: { clusters?: ClusterSummary[]; className?: string }) {
  const names = useMemo(
    () => (clusters ?? []).filter((x) => x.workloads > 0).map((x) => x.cluster),
    [clusters],
  )
  const [picked, setPicked] = useState<string>()
  const cluster = picked && names.includes(picked) ? picked : names[0]
  const [range, setRange] = useState<TimeRange>('14d')
  const q = useNodeCostTrend(cluster, range)
  // The node estimate of the Capacity tab, drawn as a level on the node cost: what the nodes would
  // cost per hour after the open recommendations. Never mixed into the realized amounts.
  const capacity = useClusterCapacity(cluster)
  const estimate = capacity.data?.estimate
  const estimateHourly =
    estimate && estimate.priceSource !== 'none' && estimate.estimatedNodes > 0
      ? estimate.estimatedMonthlyUSD / HOURS_PER_MONTH
      : undefined
  const c = useChartColors()
  const pts = useMemo(() => q.data?.points ?? [], [q.data])
  const option = useMemo(() => {
    const base = baseOption(c)
    const time = (v: number | undefined | null) => (v == null ? null : v)
    return {
      ...base,
      tooltip: {
        ...base.tooltip,
        formatter: (ps: { seriesName: string; value: [string, number | null]; color: string }[]) => {
          const head = ps[0] ? formatDateTime(ps[0].value[0], true) : ''
          const rows = ps.map((p) => {
            const v = p.value[1]
            const txt =
              v == null
                ? '—'
                : p.seriesName === 'Node cost / hour'
                  ? formatUSD(v, { cents: true })
                  : p.seriesName === 'Released CPU requests'
                    ? `${formatCores(v)} cores`
                    : formatPercent(v)
            return `<div style="display:flex;gap:12px;justify-content:space-between"><span><span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:${p.color};margin-right:6px"></span>${p.seriesName}</span><b>${txt}</b></div>`
          })
          return `<div style="margin-bottom:4px">${head}</div>${rows.join('')}`
        },
      },
      axisPointer: { link: [{ xAxisIndex: 'all' }] },
      grid: [
        { left: 8, right: 12, top: 18, height: '38%', containLabel: true },
        { left: 8, right: 12, top: '58%', bottom: 8, containLabel: true },
      ],
      xAxis: [0, 1].map((i) => ({
        type: 'time',
        gridIndex: i,
        ...axisStyle(c),
        splitLine: { show: false },
        axisLabel: { ...axisStyle(c).axisLabel, show: i === 1, formatter: '{d} {MMM}' },
      })),
      yAxis: [
        {
          type: 'value',
          gridIndex: 0,
          scale: true,
          // Keep the estimate level in view.
          ...(estimateHourly != null
            ? {
                min: (v: { min: number }) => Math.min(v.min, estimateHourly),
                max: (v: { max: number }) => Math.max(v.max, estimateHourly),
              }
            : {}),
          name: 'USD / h',
          nameTextStyle: { color: c['muted-foreground'], fontSize: 10, align: 'left' },
          ...axisStyle(c),
          axisLabel: { ...axisStyle(c).axisLabel, formatter: (v: number) => formatUSD(v, { compact: true }) },
        },
        {
          type: 'value',
          gridIndex: 1,
          name: 'cores',
          nameTextStyle: { color: c['muted-foreground'], fontSize: 10, align: 'left' },
          ...axisStyle(c),
        },
        {
          type: 'value',
          gridIndex: 1,
          min: 0,
          max: 1,
          position: 'right',
          ...axisStyle(c),
          splitLine: { show: false },
          axisLabel: { ...axisStyle(c).axisLabel, formatter: (v: number) => formatPercent(v) },
        },
      ],
      series: [
        {
          name: 'Node cost / hour',
          type: 'line',
          xAxisIndex: 0,
          yAxisIndex: 0,
          showSymbol: false,
          smooth: 0.2,
          lineStyle: { width: 1.75, color: c['chart-4'] },
          itemStyle: { color: c['chart-4'] },
          data: pts.map((p) => [p.time, time(p.nodeCostUSDPerHour)]),
          ...(estimateHourly != null
            ? {
                markLine: {
                  symbol: 'none',
                  silent: true,
                  lineStyle: { type: 'dashed', width: 1.25, color: c['chart-4'] },
                  label: {
                    formatter: 'Estimate',
                    position: 'insideEndTop',
                    color: c['muted-foreground'],
                    fontSize: 10,
                  },
                  data: [{ yAxis: estimateHourly }],
                },
              }
            : {}),
        },
        {
          name: 'Released CPU requests',
          type: 'line',
          xAxisIndex: 1,
          yAxisIndex: 1,
          showSymbol: false,
          step: 'end',
          lineStyle: { width: 1.5, color: c.success },
          itemStyle: { color: c.success },
          areaStyle: { color: alpha(c.success, 0.16) },
          data: pts.map((p) => [p.time, p.released.cpuCores]),
        },
        {
          name: 'CPU requested / allocatable',
          type: 'line',
          xAxisIndex: 1,
          yAxisIndex: 2,
          showSymbol: false,
          smooth: 0.2,
          lineStyle: { width: 1.5, color: c.info, type: 'dashed' },
          itemStyle: { color: c.info },
          data: pts.map((p) => [p.time, time(p.cpuRequestedRatio)]),
        },
      ],
    }
  }, [pts, c, estimateHourly])
  const legend = [
    { label: 'Node cost / hour', color: 'bg-chart-4' },
    { label: 'Released CPU requests', color: 'bg-success' },
    { label: 'CPU requested / allocatable', color: 'bg-info' },
  ]
  return (
    <Card className={className} data-testid="node-cost-card">
      <CardHeader>
        <div>
          <CardTitle className="flex items-center gap-1.5">
            <ServerIcon className="size-4 text-muted-foreground" />
            Node cost trend
          </CardTitle>
          <CardDescription className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
            Hourly node cost next to the requests Kerf released
            <Tooltip content="Correlated, not caused: if the node autoscaler turned the released requests into fewer nodes, the lines show it. Kerf does not claim that one caused the other.">
              <span
                className="cursor-help rounded-full border px-1.5 py-px text-[10px] font-medium whitespace-nowrap text-muted-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40"
                data-testid="correlated-badge"
                tabIndex={0}
              >
                correlated, not caused
              </span>
            </Tooltip>
          </CardDescription>
        </div>
        <div className="flex items-center gap-2">
          {names.length > 1 ? (
            <NativeSelect
              aria-label="Cluster"
              value={cluster}
              onChange={(e) => setPicked(e.target.value)}
              className="w-36"
            >
              {names.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </NativeSelect>
          ) : null}
          <Segmented
            label="Node cost time range"
            value={range}
            onChange={setRange}
            options={[
              { value: '7d', label: '7d' },
              { value: '14d', label: '14d' },
              { value: '30d', label: '30d' },
            ]}
          />
        </div>
      </CardHeader>
      <CardContent className="pt-3">
        {!clusters ? (
          <Skeleton className="h-64 w-full" />
        ) : !cluster ? (
          <EmptyState
            icon={ServerIcon}
            title="No clusters with workloads"
            description="The node cost trend appears once a cluster reports workloads."
          />
        ) : q.isError ? (
          <ErrorState compact error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <Skeleton className="h-64 w-full" />
        ) : !pts.length ? (
          <EmptyState
            title="No node cost data yet"
            description="The hub fills the trend hourly from the pricing series and kube-state-metrics."
          />
        ) : (
          <>
            <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1">
              {legend.map((l) => (
                <span key={l.label} className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span className={`size-2 rounded-sm ${l.color}`} />
                  {l.label}
                </span>
              ))}
            </div>
            <Chart
              option={option}
              className="h-64"
              ariaLabel={`Node cost per hour and released CPU requests for ${cluster}`}
            />
            {estimate && estimateHourly != null ? (
              <p
                className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-muted-foreground"
                data-testid="node-estimate-note"
              >
                <Badge variant="warning">Estimate</Badge>
                Dashed line: {formatUSD(estimateHourly, { cents: true })}/h on {estimate.estimatedNodes} nodes
                (now {estimate.currentNodes}) after the open recommendations.
                <Link
                  to={`/clusters/${encodeURIComponent(cluster)}?tab=capacity`}
                  className="text-primary hover:underline"
                >
                  Capacity and blockers
                </Link>
                ·
                <a
                  href={estimate.methodURL}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-0.5 text-primary hover:underline"
                >
                  How this is estimated <ExternalLinkIcon className="size-3" />
                </a>
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  )
}
