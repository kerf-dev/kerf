import { describe, expect, it } from 'vitest'
import type { CapacityHistory } from '@/api/types'
import { capacityHistoryFor } from '@/mocks/capacity-history'
import { trendOption } from './trend-option'

type Opt = {
  useUTC?: boolean
  grid: unknown[]
  xAxis: { type: string }[]
  dataZoom: { xAxisIndex: number[] }[]
  axisPointer: { link: unknown[] }
  series: { id: string; data: unknown[]; connectNulls?: boolean; lineStyle?: { type?: string } }[]
}
const build = (h: CapacityHistory) => trendOption(h) as unknown as Opt
const find = (o: Opt, id: string) => o.series.find((s) => s.id === id)!

describe('trendOption', () => {
  const h = capacityHistoryFor('30d', undefined)
  const o = build(h)

  it('has three grids with one time axis each, zoomed together', () => {
    expect(o.grid).toHaveLength(3)
    expect(o.xAxis.map((a) => a.type)).toEqual(['time', 'time', 'time'])
    expect(o.dataZoom[0]!.xAxisIndex).toEqual([0, 1, 2])
    expect(o.axisPointer.link).toEqual([{ xAxisIndex: 'all' }])
  })

  it('draws allocatable solid and requested dashed', () => {
    expect(find(o, 'cpuAllocatable').lineStyle?.type ?? 'solid').toBe('solid')
    expect(find(o, 'cpuRequested').lineStyle?.type).toBe('dashed')
    expect(find(o, 'memRequested').connectNulls).toBe(false)
  })

  it('turns a 6-hour gap into null points', () => {
    const data = find(o, 'nodes').data as [number, number | null][]
    expect(data.filter(([, v]) => v === null)).toHaveLength(6)
    expect(data).toHaveLength(h.points.length + 6)
    expect(data[0]![1]).not.toBeNull()
    expect(data.at(-1)![1]).not.toBeNull()
  })

  it('fills missing days for daily ranges', () => {
    const d = build(capacityHistoryFor('90d', undefined))
    const data = find(d, 'nodes').data as [number, number | null][]
    expect(data.filter(([, v]) => v === null)).toHaveLength(1)
  })

  it('does not fill before the first or after the last point', () => {
    const one: CapacityHistory = {
      ...h,
      points: [h.points[10]!, h.points[12]!],
      events: [],
    }
    const data = find(build(one), 'nodes').data as [number, number | null][]
    expect(data.map(([, v]) => v === null)).toEqual([false, true, false])
  })

  it('renders axes and tooltips in UTC', () => {
    expect(o.useUTC).toBe(true)
  })

  it('shows memory in GiB', () => {
    const data = find(o, 'memAllocatable').data as [number, number | null][]
    expect(data[0]![1]).toBe(h.points[0]!.memAllocatable / 2 ** 30)
  })

  it('has one event marker per event, at the top of the nodes grid at noon UTC', () => {
    const ev = find(o, 'events').data as { value: [number, number]; type: string }[]
    expect(ev).toHaveLength(h.events.length)
    const maxNodes = Math.max(...h.points.map((p) => p.nodes))
    for (const [i, e] of ev.entries()) {
      expect(e.value[1]).toBe(maxNodes)
      expect(e.value[0]).toBe(Date.parse(`${h.events[i]!.day}T12:00:00Z`))
    }
  })
})
