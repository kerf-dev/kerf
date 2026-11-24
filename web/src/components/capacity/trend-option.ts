import type { CapacityHistory, CapacityHistoryEvent } from '@/api/types'
import type { EChartsOption } from '@/components/charts/echarts'
import { alpha, axisStyle, baseOption, type ChartColors } from '@/components/charts/theme'
import { formatNumber } from '@/lib/format'

const HOUR = 3600_000
const DAY = 24 * HOUR
const GiB = 2 ** 30

type Point = [number, number | null]

/** Marker per event type: ▲ applied, ▼ rolled back, ● released, ◆ pool added or removed. */
const SYMBOL: Record<CapacityHistoryEvent['type'], { symbol: string; symbolRotate?: number }> = {
  applied: { symbol: 'triangle' },
  rolled_back: { symbol: 'triangle', symbolRotate: 180 },
  released: { symbol: 'circle' },
  pool_added: { symbol: 'diamond' },
  pool_removed: { symbol: 'diamond' },
}

export const EVENT_LABEL: Record<CapacityHistoryEvent['type'], string> = {
  applied: 'Applied',
  rolled_back: 'Rolled back',
  released: 'Released',
  pool_added: 'Pool added',
  pool_removed: 'Pool removed',
}

const FALLBACK = '#888888'

/**
 * One value per expected step from the first to the last point, `null` where a step is missing so
 * the line breaks there. Nothing is filled before the first or after the last point.
 */
function series(h: CapacityHistory, value: (p: CapacityHistory['points'][number]) => number): Point[] {
  if (!h.points.length) return []
  const step = h.range === '90d' || h.range === '1y' ? DAY : HOUR
  const byTime = new Map(h.points.map((p) => [Date.parse(p.hour), value(p)]))
  const times = [...byTime.keys()]
  const first = Math.min(...times)
  const last = Math.max(...times)
  const out: Point[] = []
  for (let t = first; t <= last; t += step) out.push([t, byTime.get(t) ?? null])
  return out
}

const tooltipTime = new Intl.DateTimeFormat('en-GB', {
  day: '2-digit',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
  timeZone: 'UTC',
})

export function trendOption(h: CapacityHistory, colors?: ChartColors): EChartsOption {
  const c: ChartColors =
    colors ??
    (new Proxy(
      { series: Array(6).fill(FALLBACK) },
      { get: (t, k) => (k in t ? t[k as 'series'] : FALLBACK) },
    ) as ChartColors)
  const base = baseOption(c)
  const axis = axisStyle(c)
  const daily = h.range === '90d' || h.range === '1y'

  const nodes = series(h, (p) => p.nodes)
  const maxNodes = h.points.reduce((m, p) => Math.max(m, p.nodes), 0)
  const [colNodes, colCPU, colMem] = [c['chart-1'], c['chart-2'], c['chart-3']]

  const heights = { top: [12, 178, 344], height: 130 }
  const grid = heights.top.map((top) => ({
    left: 8,
    right: 16,
    top,
    height: heights.height,
    containLabel: true,
  }))
  const xAxis = heights.top.map((_, i) => ({
    type: 'time' as const,
    gridIndex: i,
    ...axis,
    splitLine: { show: false },
    axisLabel: { ...axis.axisLabel, show: i === 2 },
    axisPointer: { show: true },
  }))
  const yAxis = [
    { name: 'Nodes', min: 0 },
    { name: 'CPU cores', min: 0 },
    { name: 'Memory GiB', min: 0 },
  ].map((a, i) => ({
    type: 'value' as const,
    gridIndex: i,
    ...a,
    nameTextStyle: { color: c['muted-foreground'], fontSize: 11, align: 'left' as const },
    ...axis,
  }))

  const line = (id: string, name: string, i: number, color: string, data: Point[], dashed = false) => ({
    id,
    name,
    type: 'line' as const,
    xAxisIndex: i,
    yAxisIndex: i,
    data,
    connectNulls: false,
    showSymbol: false,
    symbolSize: 6,
    lineStyle: { width: 1.75, color, ...(dashed ? { type: 'dashed' as const } : {}) },
    itemStyle: { color },
    emphasis: { focus: 'none' as const },
    ...(id === 'nodes' ? { areaStyle: { color: alpha(color, 0.08) } } : {}),
  })

  const events = h.events.map((e) => ({
    value: [Date.parse(`${e.day}T12:00:00Z`), maxNodes] as [number, number],
    type: e.type,
    day: e.day,
    count: e.count,
    nodePool: e.nodePool,
    ...SYMBOL[e.type],
    symbolSize: 10 + Math.min(10, Math.log2(e.count + 1) * 2),
  }))

  const fmtDay = (v: number) => (daily ? tooltipTime.format(v).split(',')[0] : tooltipTime.format(v))
  const val = (params: { seriesId?: string; value?: unknown }[], id: string) => {
    const p = params.find((x) => x.seriesId === id)
    const v = Array.isArray(p?.value) ? (p.value[1] as number | null) : null
    return v == null ? '—' : formatNumber(Math.round(v * 100) / 100)
  }

  return {
    ...base,
    grid,
    xAxis,
    yAxis,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      axisPointer: { ...base.tooltip.axisPointer, link: [{ xAxisIndex: 'all' }] },
      formatter: (raw: unknown) => {
        const params = (Array.isArray(raw) ? raw : [raw]) as {
          seriesId?: string
          value?: unknown
          axisValue?: number
        }[]
        const t = params.find((p) => p.axisValue != null)?.axisValue
        if (t == null) return ''
        return [
          `<div style="font-weight:600;margin-bottom:4px">${fmtDay(t)}${daily ? '' : ' UTC'}</div>`,
          `Nodes: ${val(params, 'nodes')}`,
          `CPU requested: ${val(params, 'cpuRequested')} of ${val(params, 'cpuAllocatable')} cores`,
          `Memory requested: ${val(params, 'memRequested')} of ${val(params, 'memAllocatable')} GiB`,
        ].join('<br/>')
      },
    },
    dataZoom: [
      { type: 'slider', xAxisIndex: [0, 1, 2], bottom: 8, height: 22, filterMode: 'none' },
      { type: 'inside', xAxisIndex: [0, 1, 2], filterMode: 'none' },
    ],
    series: [
      line('nodes', 'Nodes', 0, colNodes, nodes),
      line(
        'cpuAllocatable',
        'CPU allocatable',
        1,
        colCPU,
        series(h, (p) => p.cpuAllocatable),
      ),
      line(
        'cpuRequested',
        'CPU requested',
        1,
        colCPU,
        series(h, (p) => p.cpuRequested),
        true,
      ),
      line(
        'memAllocatable',
        'Memory allocatable',
        2,
        colMem,
        series(h, (p) => p.memAllocatable / GiB),
      ),
      line(
        'memRequested',
        'Memory requested',
        2,
        colMem,
        series(h, (p) => p.memRequested / GiB),
        true,
      ),
      {
        id: 'events',
        name: 'Events',
        type: 'scatter' as const,
        xAxisIndex: 0,
        yAxisIndex: 0,
        data: events,
        z: 5,
        itemStyle: { color: c.foreground },
        label: {
          show: true,
          position: 'top' as const,
          color: c['muted-foreground'],
          fontSize: 10,
          formatter: (p: { data?: { count?: number } }) =>
            (p.data?.count ?? 0) > 1 ? String(p.data?.count) : '',
        },
        tooltip: {
          trigger: 'item' as const,
          formatter: (p: {
            data?: { type: CapacityHistoryEvent['type']; day: string; count: number; nodePool?: string }
          }) => {
            const d = p.data
            if (!d) return ''
            return `${EVENT_LABEL[d.type]}: ${d.count}${d.nodePool ? ` (${d.nodePool})` : ''}<br/>${d.day}`
          },
        },
      },
    ],
  }
}
