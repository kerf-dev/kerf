import {
  ArrowRightIcon,
  CpuIcon,
  MemoryStickIcon,
  PiggyBankIcon,
  ServerOffIcon,
  ShieldAlertIcon,
  TrendingDownIcon,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useSavingsTrend, useSummary, useWorkloads, type TimeRange } from '@/api/queries'
import type { RiskFlag, Summary, WorkloadRow } from '@/api/types'
import { Chart } from '@/components/charts/chart'
import { alpha, axisStyle, baseOption, useChartColors } from '@/components/charts/theme'
import { Change, Confidence, KindBadge, MODE_INFO, PHASE_INFO, RISK_INFO, Savings } from '@/components/domain'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import {
  formatBytes,
  formatCores,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatUSD,
  parseQuantity,
} from '@/lib/format'
import { cn } from '@/lib/utils'
import { workloadPath } from '@/lib/workload-id'

/** Stable color per cluster (alphabetical index), so a cluster has the same color in every chart. */
function clusterColor(c: ReturnType<typeof useChartColors>, name: string | undefined, all: string[]) {
  const i = Math.max(0, [...all].sort().indexOf(name ?? ''))
  return c.series[i % c.series.length]!
}

/**
 * Drops the leading stretch where every series is zero (before the first recommender run), keeping one
 * zero point, so a young installation does not show 29 empty days and a spike.
 */
function trimLeadingZeros<P extends { total: number }>(series: { points: P[] }[]): number {
  const n = Math.max(0, ...series.map((s) => s.points.length))
  for (let i = 0; i < n; i++) if (series.some((s) => (s.points[i]?.total ?? 0) > 0)) return Math.max(0, i - 1)
  return 0
}

export function OverviewPage() {
  const summary = useSummary()
  const s = summary.data

  if (summary.isError) {
    return (
      <div className="space-y-6">
        <PageHeader title="Overview" />
        <Card>
          <ErrorState error={summary.error} onRetry={() => summary.refetch()} />
        </Card>
      </div>
    )
  }
  if (s && s.fleet.workloads === 0) {
    return (
      <div className="space-y-6">
        <PageHeader title="Overview" description="Fleet-wide rightsizing opportunities." />
        <Card>
          <EmptyState
            icon={ServerOffIcon}
            title="No workloads reported yet"
            description="Once an agent connects and the recommender has run, savings opportunities appear here. Check the Clusters page for agent status."
            action={
              <Button asChild size="sm" variant="outline">
                <Link to="/clusters">View clusters</Link>
              </Button>
            }
            className="py-20"
          />
        </Card>
      </div>
    )
  }

  const activeClusters = s?.clusters.filter((c) => c.workloads > 0).length ?? 0
  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description={
          s ? (
            <>
              {formatNumber(s.fleet.workloads)} workloads across {activeClusters} cluster
              {activeClusters === 1 ? '' : 's'}
              {s.fleet.lastRecommenderRunAt ? (
                <> · recommendations from {formatDateTime(s.fleet.lastRecommenderRunAt, true)}</>
              ) : null}
            </>
          ) : (
            <Skeleton className="h-4 w-80" />
          )
        }
        actions={
          <Button asChild size="sm">
            <Link to="/opportunities">
              Explore opportunities <ArrowRightIcon />
            </Link>
          </Button>
        }
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <SavingsTile summary={s?.fleet} />
        <ResourceTile resource="cpu" summary={s?.fleet} />
        <ResourceTile resource="memory" summary={s?.fleet} />
        <RiskTile summary={s?.fleet} />
      </div>

      <div className="grid gap-4 xl:grid-cols-3">
        <SavingsTrendCard className="xl:col-span-2" />
        <ClusterSavingsCard summary={s} />
      </div>

      <div className="grid gap-4 xl:grid-cols-3">
        <TopOpportunitiesCard className="xl:col-span-2" />
        <AutomationCard summary={s?.fleet} />
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- KPI tiles

function Tile({
  icon: Icon,
  label,
  children,
  tone = 'primary',
  loading,
}: {
  icon: typeof CpuIcon
  label: string
  children: React.ReactNode
  tone?: 'primary' | 'info' | 'chart-2' | 'warning'
  loading?: boolean
}) {
  const toneCls = {
    primary: 'bg-primary/10 text-primary',
    info: 'bg-info/10 text-info',
    'chart-2': 'bg-chart-2/10 text-chart-2',
    warning: 'bg-warning/15 text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]',
  }[tone]
  return (
    <Card className="relative overflow-hidden">
      <CardContent className="flex h-full flex-col gap-3 py-4">
        <div className="flex items-center gap-2 text-[13px] font-medium text-muted-foreground">
          <span className={cn('flex size-6 items-center justify-center rounded-md', toneCls)}>
            <Icon className="size-3.5" />
          </span>
          {label}
        </div>
        {loading ? (
          <div className="space-y-2.5">
            <Skeleton className="h-8 w-32" />
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-2/3" />
          </div>
        ) : (
          children
        )}
      </CardContent>
    </Card>
  )
}

function SavingsTile({ summary }: { summary?: Summary }) {
  const trend = useSavingsTrend('30d', 'none')
  const c = useChartColors()
  const pts = useMemo(() => {
    const p = trend.data?.series[0]?.points ?? []
    return p.slice(trimLeadingZeros([{ points: p }]))
  }, [trend.data])
  const first = pts[0]?.total
  const option = useMemo(
    () => ({
      animation: false,
      grid: { left: 0, right: 0, top: 2, bottom: 0 },
      xAxis: { type: 'time', show: false },
      yAxis: { type: 'value', show: false, min: (v: { min: number }) => v.min * 0.92 },
      series: [
        {
          type: 'line',
          data: pts.map((p) => [p.time, p.total]),
          showSymbol: false,
          smooth: 0.3,
          lineStyle: { width: 1.5, color: c.success },
          areaStyle: {
            color: {
              type: 'linear',
              x: 0,
              y: 0,
              x2: 0,
              y2: 1,
              colorStops: [
                { offset: 0, color: alpha(c.success, 0.25) },
                { offset: 1, color: alpha(c.success, 0) },
              ],
            },
          },
        },
      ],
    }),
    [pts, c],
  )
  const total = summary?.monthlySavingsUSD.total ?? 0
  const cpuShare = total > 0 ? Math.max(0, summary!.monthlySavingsUSD.cpu) / total : 0
  return (
    <Tile icon={PiggyBankIcon} label="Estimated monthly savings" loading={!summary}>
      <div className="flex items-end justify-between gap-3">
        <div>
          <div
            className="text-[28px] leading-none font-semibold tracking-tight tabular"
            data-testid="kpi-savings"
          >
            {formatUSD(total)}
          </div>
          <div className="mt-1.5 text-xs text-muted-foreground">
            {formatUSD(total * 12, { compact: true })} / year
            {first ? (
              <Tooltip content="Change of the estimated savings over the last 30 days. Applied recommendations turn estimated savings into realized ones, so this can go down while costs fall.">
                <span className="ml-1.5 cursor-help whitespace-nowrap underline decoration-dotted underline-offset-2">
                  {total >= first ? '+' : '−'}
                  {formatPercent(Math.abs(total - first) / first)} 30d
                </span>
              </Tooltip>
            ) : null}
          </div>
        </div>
        <div className="h-10 w-24 shrink-0">
          {pts.length ? <Chart option={option} className="h-10" ariaLabel="Savings trend sparkline" /> : null}
        </div>
      </div>
      <div className="mt-auto space-y-1.5">
        <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
          <div className="bg-chart-1" style={{ width: `${cpuShare * 100}%` }} />
          <div className="bg-chart-2" style={{ width: `${(1 - cpuShare) * 100}%` }} />
        </div>
        <div className="flex justify-between text-[11px] text-muted-foreground">
          <span className="flex items-center gap-1.5">
            <span className="size-1.5 rounded-full bg-chart-1" />
            CPU {formatUSD(summary?.monthlySavingsUSD.cpu, { compact: true })}
          </span>
          <span className="flex items-center gap-1.5">
            <span className="size-1.5 rounded-full bg-chart-2" />
            Memory {formatUSD(summary?.monthlySavingsUSD.memory, { compact: true })}
          </span>
        </div>
      </div>
    </Tile>
  )
}

function ResourceTile({ resource, summary }: { resource: 'cpu' | 'memory'; summary?: Summary }) {
  const t = summary?.[resource]
  const fmt = (v?: number) =>
    resource === 'cpu' ? `${formatNumber(Math.round(v ?? 0))} cores` : formatBytes(v, 1)
  const max = t ? Math.max(t.requested, t.recommended, t.used) || 1 : 1
  const reduction = t && t.requested > 0 ? 1 - t.recommended / t.requested : 0
  const rows = t
    ? [
        { label: 'Requested', v: t.requested, cls: 'bg-muted-foreground/35' },
        { label: 'Recommended', v: t.recommended, cls: resource === 'cpu' ? 'bg-chart-1' : 'bg-chart-2' },
        { label: 'Used (24h avg)', v: t.used, cls: 'bg-foreground/70' },
      ]
    : []
  return (
    <Tile
      icon={resource === 'cpu' ? CpuIcon : MemoryStickIcon}
      label={resource === 'cpu' ? 'CPU' : 'Memory'}
      tone={resource === 'cpu' ? 'primary' : 'chart-2'}
      loading={!summary}
    >
      <div className="flex items-baseline gap-2">
        <div className="text-[28px] leading-none font-semibold tracking-tight tabular">
          {formatPercent(t && t.requested ? t.used / t.requested : undefined)}
        </div>
        <div className="text-xs text-muted-foreground">of requests used</div>
      </div>
      <div className="mt-auto space-y-1.5">
        {rows.map((r) => (
          <div key={r.label} className="grid grid-cols-[84px_1fr_auto] items-center gap-2 text-[11px]">
            <span className="text-muted-foreground">{r.label}</span>
            <div className="h-1.5 overflow-hidden rounded-full bg-muted">
              <div className={cn('h-full rounded-full', r.cls)} style={{ width: `${(r.v / max) * 100}%` }} />
            </div>
            <span className="text-right font-mono whitespace-nowrap tabular text-foreground/80">
              {fmt(r.v)}
            </span>
          </div>
        ))}
        <div className="pt-0.5 text-[11px] text-muted-foreground">
          <span className="font-medium text-success">{formatPercent(reduction)}</span> less requested if all
          recommendations are applied
        </div>
      </div>
    </Tile>
  )
}

function RiskTile({ summary }: { summary?: Summary }) {
  const flags = summary?.workloadsByRiskFlag ?? {}
  const entries = (Object.entries(flags) as [RiskFlag, number][])
    .filter(([f]) => f !== 'sidecar' && f !== 'short-lived')
    .sort((a, b) => b[1] - a[1])
  const pressure = (flags.throttled ?? 0) + (flags['oom-killed'] ?? 0)
  const navigate = useNavigate()
  return (
    <Tile icon={ShieldAlertIcon} label="Under pressure" tone="warning" loading={!summary}>
      <div className="flex items-baseline gap-2">
        <div className="text-[28px] leading-none font-semibold tracking-tight tabular">
          {formatNumber(pressure)}
        </div>
        <div className="text-xs text-muted-foreground">throttled or OOM-killed</div>
      </div>
      <div className="mt-auto flex flex-wrap gap-1.5">
        {entries.slice(0, 5).map(([f, n]) => {
          const Icon = RISK_INFO[f].icon
          return (
            <button
              key={f}
              onClick={() => navigate(`/opportunities?riskFlag=${f}`)}
              className="inline-flex items-center gap-1.5 rounded-md border bg-card px-2 py-1 text-[11px] transition-colors outline-none hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/40"
            >
              <Icon
                className={cn(
                  'size-3',
                  RISK_INFO[f].severity === 'high' ? 'text-destructive' : 'text-warning',
                )}
              />
              <span className="text-muted-foreground">{RISK_INFO[f].label}</span>
              <span className="font-semibold tabular">{n}</span>
            </button>
          )
        })}
        {!entries.length ? <span className="text-xs text-muted-foreground">No risk flags. 🎉</span> : null}
      </div>
    </Tile>
  )
}

// ---------------------------------------------------------------- trend

function SavingsTrendCard({ className }: { className?: string }) {
  const [range, setRange] = useState<TimeRange>('30d')
  const trend = useSavingsTrend(range, 'cluster')
  const c = useChartColors()
  const series = useMemo(() => {
    const all = (trend.data?.series ?? []).filter((s) => s.points.length)
    const start = trimLeadingZeros(all)
    return all.map((s) => ({ ...s, points: s.points.slice(start) }))
  }, [trend.data])
  // All clusters (also empty ones) so indices match the by-cluster card.
  const names = useMemo(() => (trend.data?.series ?? []).map((s) => s.cluster ?? ''), [trend.data])
  const trimmedFrom =
    trend.data && series[0]?.points[0] && series[0].points[0].time > trend.data.from
      ? series[0].points[0].time
      : undefined
  const option = useMemo(() => {
    const base = baseOption(c)
    return {
      ...base,
      color: series.map((x) => clusterColor(c, x.cluster, names)),
      legend: { show: false },
      tooltip: {
        ...base.tooltip,
        valueFormatter: (v: number) => formatUSD(v),
      },
      xAxis: {
        type: 'time',
        ...axisStyle(c),
        splitLine: { show: false },
        axisLabel: { ...axisStyle(c).axisLabel, formatter: '{d} {MMM}' },
      },
      yAxis: {
        type: 'value',
        ...axisStyle(c),
        axisLabel: { ...axisStyle(c).axisLabel, formatter: (v: number) => formatUSD(v, { compact: true }) },
      },
      series: series.map((s) => ({
        name: s.cluster ?? 'Fleet',
        type: 'line',
        stack: 'total',
        smooth: 0.25,
        showSymbol: false,
        lineStyle: { width: 1.75 },
        emphasis: { focus: 'series' },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              { offset: 0, color: alpha(clusterColor(c, s.cluster, names), 0.32) },
              { offset: 1, color: alpha(clusterColor(c, s.cluster, names), 0.04) },
            ],
          },
        },
        data: s.points.map((p) => [p.time, p.total]),
      })),
    }
  }, [series, c, names])
  return (
    <Card className={className}>
      <CardHeader>
        <div>
          <CardTitle>Savings trend</CardTitle>
          <CardDescription>
            Estimated monthly savings of the latest recommendations, by cluster
          </CardDescription>
        </div>
        <Segmented
          label="Time range"
          value={range}
          onChange={setRange}
          options={[
            { value: '7d', label: '7d' },
            { value: '14d', label: '14d' },
            { value: '30d', label: '30d' },
          ]}
        />
      </CardHeader>
      <CardContent className="pt-3">
        {trend.isError ? (
          <ErrorState compact error={trend.error} onRetry={() => trend.refetch()} />
        ) : !trend.data ? (
          <Skeleton className="h-64 w-full" />
        ) : !series.length ? (
          <EmptyState
            title="No trend data yet"
            description="The trend fills in after the first recommender runs."
          />
        ) : (
          <>
            <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1">
              {series.map((s, i) => (
                <span
                  key={s.cluster ?? i}
                  className="flex items-center gap-1.5 text-xs text-muted-foreground"
                >
                  <span
                    className="size-2 rounded-sm"
                    style={{ background: clusterColor(c, s.cluster, names) }}
                  />
                  {s.cluster}
                </span>
              ))}
              {trimmedFrom ? (
                <span className="ml-auto text-xs text-muted-foreground">
                  Data since {formatDateTime(trimmedFrom, true)}
                </span>
              ) : null}
            </div>
            <Chart
              option={option}
              className="h-64"
              ariaLabel="Estimated monthly savings over time by cluster"
            />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function ClusterSavingsCard({
  summary,
}: {
  summary?: { clusters: (Summary & { cluster: string })[]; fleet: Summary }
}) {
  const c = useChartColors()
  const navigate = useNavigate()
  const clusters = [...(summary?.clusters ?? [])].sort(
    (a, b) => b.monthlySavingsUSD.total - a.monthlySavingsUSD.total,
  )
  const max = Math.max(1, ...clusters.map((x) => x.monthlySavingsUSD.total))
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Savings by cluster</CardTitle>
          <CardDescription>Monthly estimate and requested-vs-recommended CPU</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-1 pt-3">
        {!summary
          ? Array.from({ length: 3 }, (_, i) => <Skeleton key={i} className="mb-3 h-14 w-full" />)
          : clusters.map((cl) => {
              const color = clusterColor(
                c,
                cl.cluster,
                clusters.map((x) => x.cluster),
              )
              const share = summary.fleet.monthlySavingsUSD.total
                ? cl.monthlySavingsUSD.total / summary.fleet.monthlySavingsUSD.total
                : 0
              return (
                <button
                  key={cl.cluster}
                  onClick={() => navigate(`/opportunities?cluster=${encodeURIComponent(cl.cluster)}`)}
                  className="group -mx-2 block w-[calc(100%+1rem)] rounded-lg px-2 py-2.5 text-left transition-colors outline-none hover:bg-accent/60 focus-visible:ring-[3px] focus-visible:ring-ring/40"
                >
                  <div className="flex items-baseline justify-between gap-3">
                    <span className="flex min-w-0 items-center gap-2 text-[13px] font-medium">
                      <span className="size-2 shrink-0 rounded-sm" style={{ background: color }} />
                      <span className="truncate">{cl.cluster}</span>
                    </span>
                    <span className="text-[13px] font-semibold tabular">
                      {cl.workloads ? (
                        formatUSD(cl.monthlySavingsUSD.total)
                      ) : (
                        <span className="font-normal text-muted-foreground">no data</span>
                      )}
                    </span>
                  </div>
                  <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full rounded-full transition-all group-hover:opacity-90"
                      style={{
                        width: `${(cl.monthlySavingsUSD.total / max) * 100}%`,
                        background: color,
                      }}
                    />
                  </div>
                  <div className="mt-1.5 flex justify-between text-[11px] text-muted-foreground">
                    <span>
                      {formatNumber(cl.workloads)} workloads · {formatPercent(share)} of total
                    </span>
                    {cl.workloads ? (
                      <span className="font-mono">
                        {formatNumber(Math.round(cl.cpu.requested))} →{' '}
                        {formatNumber(Math.round(cl.cpu.recommended))} cores
                      </span>
                    ) : null}
                  </div>
                </button>
              )
            })}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- top opportunities

function mainChange(w: WorkloadRow, resource: 'cpu' | 'memory') {
  let cur = 0
  let rec = 0
  for (const c of w.containers ?? []) {
    if (c.init) continue
    cur += parseQuantity(c.current.requests?.[resource]) ?? 0
    rec += parseQuantity(c.recommended.requests?.[resource]) ?? 0
  }
  return { cur, rec }
}

function TopOpportunitiesCard({ className }: { className?: string }) {
  const q = useWorkloads({ sort: 'savings', limit: 10 })
  const navigate = useNavigate()
  return (
    <Card className={className}>
      <CardHeader>
        <div>
          <CardTitle>Top 10 opportunities</CardTitle>
          <CardDescription>Largest estimated monthly savings per workload</CardDescription>
        </div>
        <Button asChild variant="ghost" size="sm" className="-mt-1 -mr-2 text-muted-foreground">
          <Link to="/opportunities">
            View all <ArrowRightIcon />
          </Link>
        </Button>
      </CardHeader>
      <CardContent className="px-0 pt-3 pb-2">
        {q.isError ? (
          <ErrorState compact error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <div className="space-y-2 px-5">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : !q.data.items.length ? (
          <EmptyState
            icon={TrendingDownIcon}
            title="No opportunities"
            description="Every workload is already right-sized."
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                  <th className="py-2 pr-3 pl-5 font-medium">Workload</th>
                  <th className="px-3 py-2 font-medium">CPU request</th>
                  <th className="hidden px-3 py-2 font-medium md:table-cell">Memory request</th>
                  <th className="hidden px-3 py-2 font-medium 2xl:table-cell">Confidence</th>
                  <th className="py-2 pr-5 pl-3 text-right font-medium">Savings / mo</th>
                </tr>
              </thead>
              <tbody>
                {q.data.items.map((w, i) => {
                  const cpu = mainChange(w, 'cpu')
                  const mem = mainChange(w, 'memory')
                  return (
                    <tr
                      key={w.id}
                      tabIndex={0}
                      onClick={() => navigate(workloadPath(w.id))}
                      onKeyDown={(e) => e.key === 'Enter' && navigate(workloadPath(w.id))}
                      className="cursor-pointer border-b border-border/60 transition-colors outline-none last:border-0 hover:bg-accent/50 focus-visible:bg-accent/60"
                    >
                      <td className="py-2.5 pr-3 pl-5">
                        <div className="flex items-center gap-2.5">
                          <span className="w-4 text-right text-[11px] text-muted-foreground tabular">
                            {i + 1}
                          </span>
                          <div className="min-w-0">
                            <div className="flex items-center gap-1.5">
                              <Link
                                to={workloadPath(w.id)}
                                onClick={(e) => e.stopPropagation()}
                                className="truncate font-medium hover:underline"
                                tabIndex={-1}
                              >
                                {w.name}
                              </Link>
                              <KindBadge kind={w.kind} />
                            </div>
                            <div className="truncate text-xs text-muted-foreground">
                              {w.namespace} · {w.cluster}
                            </div>
                          </div>
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <Change from={cpu.cur} to={cpu.rec} format={formatCores} />
                      </td>
                      <td className="hidden px-3 py-2.5 md:table-cell">
                        <Change from={mem.cur} to={mem.rec} format={(v) => formatBytes(v)} />
                      </td>
                      <td className="hidden px-3 py-2.5 2xl:table-cell">
                        <Confidence value={w.confidence} />
                      </td>
                      <td className="py-2.5 pr-5 pl-3 text-right">
                        <Savings value={w.monthlySavingsUSD?.total} />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- automation

function AutomationCard({ summary }: { summary?: Summary }) {
  const navigate = useNavigate()
  const phases = summary?.workloadsByPhase ?? {}
  const total = summary?.workloads ?? 0
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Automation</CardTitle>
          <CardDescription>Effective modes and what the agents are doing</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-5 pt-3">
        {!summary ? (
          <Skeleton className="h-60 w-full" />
        ) : (
          <>
            {(['cpu', 'memory'] as const).map((r) => {
              const m = summary.workloadsByMode[r]
              return (
                <div key={r}>
                  <div className="mb-1.5 flex items-center justify-between text-xs">
                    <span className="flex items-center gap-1.5 font-medium">
                      {r === 'cpu' ? (
                        <CpuIcon className="size-3.5 text-muted-foreground" />
                      ) : (
                        <MemoryStickIcon className="size-3.5 text-muted-foreground" />
                      )}
                      {r === 'cpu' ? 'CPU' : 'Memory'} mode
                    </span>
                    <span className="text-muted-foreground tabular">
                      {formatPercent(total ? m['auto-patch'] / total : 0)} automated
                    </span>
                  </div>
                  <div className="flex h-2.5 gap-0.5 overflow-hidden rounded-full">
                    {(['auto-patch', 'audit', 'off'] as const).map((mode) =>
                      m[mode] ? (
                        <Tooltip key={mode} content={`${m[mode]} workloads in ${mode}`}>
                          <button
                            aria-label={`${m[mode]} workloads with ${r} in ${mode}`}
                            onClick={() => navigate(`/opportunities?mode=${mode}`)}
                            className={cn(
                              'h-full transition-opacity outline-none hover:opacity-80 focus-visible:ring-2 focus-visible:ring-ring',
                              mode === 'auto-patch'
                                ? 'bg-primary'
                                : mode === 'audit'
                                  ? 'bg-info/60'
                                  : 'bg-muted-foreground/25',
                            )}
                            style={{ width: `${(m[mode] / total) * 100}%` }}
                          />
                        </Tooltip>
                      ) : null,
                    )}
                  </div>
                  <div className="mt-1.5 flex gap-3 text-[11px] text-muted-foreground">
                    {(['auto-patch', 'audit', 'off'] as const).map((mode) => (
                      <span key={mode} className="flex items-center gap-1">
                        <span
                          className={cn(
                            'size-1.5 rounded-full',
                            mode === 'auto-patch'
                              ? 'bg-primary'
                              : mode === 'audit'
                                ? 'bg-info/60'
                                : 'bg-muted-foreground/40',
                          )}
                        />
                        {MODE_INFO[mode].label}{' '}
                        <span className="font-medium text-foreground tabular">{m[mode]}</span>
                      </span>
                    ))}
                  </div>
                </div>
              )
            })}
            <div className="border-t pt-4">
              <div className="mb-2 text-xs font-medium">Phase</div>
              <div className="grid grid-cols-2 gap-1.5">
                {(['Applied', 'Applying', 'Blocked', 'RolledBack', 'Released', 'Pending'] as const).map(
                  (p) => {
                    const Icon = PHASE_INFO[p].icon
                    const v = PHASE_INFO[p].variant
                    return (
                      <Tooltip key={p} content={PHASE_INFO[p].desc}>
                        <div className="flex items-center justify-between rounded-md border px-2 py-1.5 text-xs">
                          <span className="flex items-center gap-1.5 text-muted-foreground">
                            <Icon
                              className={cn(
                                'size-3.5',
                                v === 'success'
                                  ? 'text-success'
                                  : v === 'warning'
                                    ? 'text-warning'
                                    : v === 'destructive'
                                      ? 'text-destructive'
                                      : v === 'info'
                                        ? 'text-info'
                                        : 'text-muted-foreground',
                              )}
                            />
                            {p === 'RolledBack' ? 'Rolled back' : p}
                          </span>
                          <span className="font-semibold tabular">{phases[p] ?? 0}</span>
                        </div>
                      </Tooltip>
                    )
                  },
                )}
              </div>
              <Link
                to="/activity"
                className="mt-3 inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
              >
                See activity log <ArrowRightIcon className="size-3" />
              </Link>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
