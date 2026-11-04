import { CpuIcon, MemoryStickIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { TIME_RANGES, useUsage, type TimeRange } from '@/api/queries'
import type { ContainerRecommendation } from '@/api/types'
import { Chart } from '@/components/charts/chart'
import { alpha, axisStyle, baseOption, useChartColors } from '@/components/charts/theme'
import { EmptyState, ErrorState } from '@/components/states'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { formatBytes, formatCores, parseGoDuration, parseQuantity, type Resource } from '@/lib/format'
import { cn } from '@/lib/utils'

/** Breaks the line where samples are missing (e.g. between CronJob runs) instead of bridging gaps. */
type Pt = readonly number[]
function withGaps(points: readonly Pt[], stepSec: number): (Pt | [number, null])[] {
  const out: (Pt | [number, null])[] = []
  for (let i = 0; i < points.length; i++) {
    const p = points[i]!
    const prev = points[i - 1]
    if (prev && p[0]! - prev[0]! > stepSec * 1.5) out.push([prev[0]! + stepSec, null])
    out.push(p)
  }
  return out
}

/**
 * Step series (request, limit, recommendation) may be sparse: carry the last value to the end of the
 * usage data so the line spans the chart instead of stopping at its last change.
 */
function extendStep(points: readonly Pt[], endT: number | undefined): Pt[] {
  const out = [...points]
  const last = out[out.length - 1]
  if (last && endT != null && last[0]! < endT) out.push([endT, last[1]!])
  return out
}

const ms = (pts: readonly (Pt | readonly [number, null])[]) => pts.map((p) => [p[0]! * 1000, p[1]])

export function UsageChartCard({ id, containers }: { id: string; containers: ContainerRecommendation[] }) {
  const [resource, setResource] = useState<Resource>('cpu')
  const [range, setRange] = useState<TimeRange>('7d')
  const main = containers.find((c) => !c.init && !(c.riskFlags ?? []).includes('sidecar')) ?? containers[0]
  const [container, setContainer] = useState(main?.name ?? '')
  const usage = useUsage(id, resource, range)
  const c = useChartColors()

  const series = usage.data?.containers.find((x) => x.name === container) ?? usage.data?.containers[0]
  const explanation = containers.find((x) => x.name === (series?.name ?? container))?.explanation?.[resource]
  const fmt = useMemo(
    () => (resource === 'cpu' ? (v?: number) => formatCores(v) : (v?: number) => formatBytes(v)),
    [resource],
  )
  const step = parseGoDuration(usage.data?.step) ?? 300

  const option = useMemo(() => {
    if (!series) return {}
    const base = baseOption(c)
    const p95 = parseQuantity(explanation?.observed.p95)
    const p99 = parseQuantity(explanation?.observed.p99)
    const endT = series.usage[series.usage.length - 1]?.[0]
    const rec = series.recommendation
    const firstUsage = series.usage[0]?.[0]
    // Draw it as a line only if it spans a visible part of the window (≥ 10 %); a recommendation that
    // only exists for the last few minutes would otherwise be an invisible stub at the right edge.
    const span = endT != null && firstUsage != null ? endT - firstUsage : 0
    const recStart = rec[0] ? Math.max(rec[0][0]!, firstUsage ?? rec[0][0]!) : undefined
    const recLine = rec.length > 0 && span > 0 && recStart != null && (endT! - recStart) / span >= 0.1
    const recLevel = !recLine && rec.length ? rec[rec.length - 1]![1] : undefined
    const usageColor = resource === 'cpu' ? c['chart-5'] : c['chart-2']
    const mark = (
      v: number | undefined,
      label: string,
      color: string,
      position: 'insideStartTop' | 'insideEndTop',
    ) =>
      v == null
        ? []
        : [
            {
              yAxis: v,
              label: {
                formatter: `${label} ${fmt(v)}`,
                position,
                color,
                fontSize: 10,
                fontWeight: 500 as const,
              },
              lineStyle: { color, type: [2, 3] as number[], width: 1, opacity: 0.9 },
            },
          ]
    return {
      ...base,
      tooltip: {
        ...base.tooltip,
        valueFormatter: (v: number | null) => (v == null ? '—' : fmt(v)),
      },
      legend: { show: false },
      grid: { ...base.grid, top: 24, bottom: 36 },
      dataZoom: [
        { type: 'inside', throttle: 50 },
        {
          type: 'slider',
          height: 18,
          bottom: 4,
          borderColor: 'transparent',
          backgroundColor: alpha(c['muted-foreground'], 0.06),
          fillerColor: alpha(c.primary, 0.12),
          dataBackground: {
            lineStyle: { color: alpha(c['muted-foreground'], 0.4) },
            areaStyle: { color: alpha(c['muted-foreground'], 0.1) },
          },
          selectedDataBackground: {
            lineStyle: { color: c.primary },
            areaStyle: { color: alpha(c.primary, 0.15) },
          },
          handleStyle: { color: c.card, borderColor: c.border },
          moveHandleStyle: { color: alpha(c['muted-foreground'], 0.3) },
          textStyle: { color: c['muted-foreground'], fontSize: 10 },
          labelFormatter: '',
        },
      ],
      xAxis: { type: 'time', ...axisStyle(c), splitLine: { show: false } },
      yAxis: {
        type: 'value',
        ...axisStyle(c),
        axisLabel: { ...axisStyle(c).axisLabel, formatter: (v: number) => fmt(v) },
      },
      series: [
        {
          name: 'Usage (max across pods)',
          type: 'line',
          data: ms(withGaps(series.usage, step)),
          showSymbol: false,
          sampling: 'lttb',
          lineStyle: { width: 1.25, color: usageColor },
          itemStyle: { color: usageColor },
          areaStyle: {
            color: {
              type: 'linear',
              x: 0,
              y: 0,
              x2: 0,
              y2: 1,
              colorStops: [
                { offset: 0, color: alpha(usageColor, 0.28) },
                { offset: 1, color: alpha(usageColor, 0.02) },
              ],
            },
          },
          markLine: {
            symbol: 'none',
            silent: true,
            data: [
              ...mark(p95, 'P95', c.warning, 'insideStartTop'),
              ...mark(p99, 'P99', c['chart-4'], 'insideEndTop'),
            ],
          },
          z: 2,
        },
        {
          name: 'Usage (avg across pods)',
          type: 'line',
          data: ms(withGaps(series.usageAvg ?? [], step)),
          showSymbol: false,
          sampling: 'lttb',
          lineStyle: { width: 1, color: alpha(usageColor, 0.7), type: 'dotted' },
          itemStyle: { color: alpha(usageColor, 0.7) },
          z: 2,
        },
        {
          name: 'Request',
          type: 'line',
          step: 'end',
          data: ms(extendStep(series.request, endT)),
          showSymbol: false,
          lineStyle: { width: 1.75, color: c['muted-foreground'] },
          itemStyle: { color: c['muted-foreground'] },
          z: 3,
        },
        {
          name: 'Limit',
          type: 'line',
          step: 'end',
          data: ms(extendStep(series.limit, endT)),
          showSymbol: false,
          lineStyle: { width: 1.25, color: c.destructive, type: [6, 4] },
          itemStyle: { color: c.destructive },
          z: 3,
        },
        {
          name: 'Recommendation',
          type: 'line',
          step: 'end',
          data: recLine ? ms(extendStep(series.recommendation, endT)) : [],
          showSymbol: false,
          lineStyle: { width: 2.25, color: c.success, type: [8, 4] },
          itemStyle: { color: c.success },
          // A single recommendation point (only the latest run in range) has no line to draw:
          // show it as a labelled level across the chart instead.
          markLine: recLevel
            ? {
                symbol: 'none',
                silent: true,
                data: [
                  {
                    yAxis: recLevel,
                    label: {
                      formatter: `Recommended ${fmt(recLevel)}`,
                      position: 'insideMiddleTop',
                      color: c.success,
                      fontSize: 10,
                      fontWeight: 600,
                    },
                    lineStyle: { color: c.success, type: [8, 4], width: 2 },
                  },
                ],
              }
            : undefined,
          z: 4,
        },
      ],
    }
  }, [series, c, explanation, resource, step, fmt])

  const legend = [
    {
      label: 'Usage (max of pods)',
      swatch: (
        <span
          className={cn('h-2.5 w-3 rounded-sm', resource === 'cpu' ? 'bg-chart-5/50' : 'bg-chart-2/50')}
        />
      ),
    },
    {
      label: 'Avg of pods',
      swatch: (
        <span
          className={cn(
            'w-3 border-t-2 border-dotted',
            resource === 'cpu' ? 'border-chart-5' : 'border-chart-2',
          )}
        />
      ),
    },
    { label: 'Request', swatch: <span className="w-3 border-t-2 border-muted-foreground" /> },
    { label: 'Limit', swatch: <span className="w-3 border-t-2 border-dashed border-destructive" /> },
    { label: 'Recommendation', swatch: <span className="w-3 border-t-[3px] border-dashed border-success" /> },
    { label: 'P95 / P99', swatch: <span className="w-3 border-t border-dotted border-warning" /> },
  ]

  return (
    <Card>
      <CardHeader className="flex-wrap">
        <div>
          <CardTitle>Usage</CardTitle>
          <CardDescription>Per-pod usage against request, limit and the recommended request</CardDescription>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Segmented
            label="Resource"
            value={resource}
            onChange={setResource}
            options={[
              {
                value: 'cpu',
                label: (
                  <>
                    <CpuIcon /> CPU
                  </>
                ),
              },
              {
                value: 'memory',
                label: (
                  <>
                    <MemoryStickIcon /> Memory
                  </>
                ),
              },
            ]}
          />
          <Segmented
            label="Time range"
            value={range}
            onChange={setRange}
            options={TIME_RANGES.map((r) => ({ value: r.value, label: r.label }))}
          />
        </div>
      </CardHeader>
      <CardContent className="pt-3">
        {containers.length > 1 ? (
          <div className="mb-3 flex flex-wrap items-center gap-1.5" role="radiogroup" aria-label="Container">
            {containers.map((ct) => (
              <button
                key={ct.name}
                role="radio"
                aria-checked={container === ct.name}
                onClick={() => setContainer(ct.name)}
                className={cn(
                  'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 font-mono text-xs transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40',
                  container === ct.name
                    ? 'border-foreground/20 bg-foreground text-background'
                    : 'text-muted-foreground hover:bg-accent hover:text-foreground',
                )}
              >
                {ct.name}
                {ct.init ? <span className="text-[10px] opacity-70">init</span> : null}
              </button>
            ))}
          </div>
        ) : null}
        <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1">
          {legend.map((l) => (
            <span key={l.label} className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              {l.swatch}
              {l.label}
            </span>
          ))}
        </div>
        {usage.isError ? (
          <ErrorState compact error={usage.error} onRetry={() => usage.refetch()} />
        ) : !usage.data ? (
          <Skeleton className="h-80 w-full" />
        ) : !series || !series.usage.length ? (
          <EmptyState
            title="No usage samples in this range"
            description="Pick a longer range, or wait for the metrics pipeline to collect data."
            className="h-80"
          />
        ) : (
          <Chart
            option={option}
            className={cn('h-80 transition-opacity', usage.isPlaceholderData && 'opacity-50')}
            ariaLabel={`${resource} usage of ${series.name}`}
          />
        )}
      </CardContent>
    </Card>
  )
}
