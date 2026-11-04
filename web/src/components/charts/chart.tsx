import { useEffect, useRef } from 'react'
import { cn } from '@/lib/utils'
import { echarts, type EChartsOption } from './echarts'

/** Minimal React wrapper around ECharts with resize handling. */
export function Chart({
  option,
  className,
  ariaLabel,
  onClick,
}: {
  option: EChartsOption
  className?: string
  ariaLabel: string
  onClick?: (params: { seriesName?: string; name?: string; dataIndex?: number; data?: unknown }) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const inst = useRef<echarts.ECharts | null>(null)
  const clickRef = useRef(onClick)
  useEffect(() => {
    clickRef.current = onClick
  })

  useEffect(() => {
    const el = ref.current!
    const chart = echarts.init(el, undefined, { renderer: 'canvas' })
    inst.current = chart
    chart.on('click', (p) => clickRef.current?.(p as never))
    const ro = new ResizeObserver(() => chart.resize())
    ro.observe(el)
    return () => {
      ro.disconnect()
      chart.dispose()
      inst.current = null
    }
  }, [])

  useEffect(() => {
    inst.current?.setOption(option, { notMerge: true, lazyUpdate: true })
  }, [option])

  return (
    <div
      ref={ref}
      role="img"
      aria-label={ariaLabel}
      data-testid="chart"
      className={cn('h-64 w-full', className)}
    />
  )
}
