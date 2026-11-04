import { useMemo } from 'react'
import { useTheme } from '@/components/theme-provider'

/**
 * ECharts cannot parse oklch(), so resolve the CSS tokens to rgb by painting one pixel per token.
 * Recomputed whenever the resolved theme changes.
 */
const TOKENS = [
  'foreground',
  'muted-foreground',
  'border',
  'card',
  'popover',
  'primary',
  'success',
  'warning',
  'destructive',
  'info',
  'chart-1',
  'chart-2',
  'chart-3',
  'chart-4',
  'chart-5',
  'chart-6',
] as const
export type ChartColors = Record<(typeof TOKENS)[number], string> & { series: string[] }

function resolveColors(): ChartColors {
  const style = getComputedStyle(document.documentElement)
  const canvas = document.createElement('canvas')
  canvas.width = canvas.height = 1
  const ctx = canvas.getContext('2d', { willReadFrequently: true })
  const out = {} as Record<string, string>
  for (const t of TOKENS) {
    const raw = style.getPropertyValue(`--${t}`).trim()
    if (!ctx || !raw) {
      out[t] = raw || '#888'
      continue
    }
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = '#000'
    ctx.fillStyle = raw
    ctx.fillRect(0, 0, 1, 1)
    const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data
    out[t] = `rgb(${r}, ${g}, ${b})`
  }
  const c = out as ChartColors
  c.series = [c['chart-1'], c['chart-2'], c['chart-3'], c['chart-4'], c['chart-5'], c['chart-6']]
  return c
}

export function useChartColors(): ChartColors {
  const { resolved } = useTheme()
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => resolveColors(), [resolved])
}

export function alpha(rgb: string, a: number): string {
  return rgb.replace('rgb(', 'rgba(').replace(')', `, ${a})`)
}

/** Shared axis/tooltip styling. */
export function baseOption(c: ChartColors) {
  return {
    animationDuration: 500,
    textStyle: {
      fontFamily: 'Geist Variable, ui-sans-serif, system-ui, sans-serif',
      color: c['muted-foreground'],
    },
    tooltip: {
      trigger: 'axis' as const,
      backgroundColor: c.popover,
      borderColor: c.border,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: { color: c.foreground, fontSize: 12 },
      extraCssText: 'box-shadow: 0 8px 24px -8px rgba(0,0,0,.25); border-radius: 8px;',
      axisPointer: {
        type: 'line' as const,
        lineStyle: { color: c['muted-foreground'], type: 'dashed' as const, opacity: 0.6 },
      },
    },
    grid: { left: 8, right: 12, top: 16, bottom: 8, containLabel: true },
  }
}

export function axisStyle(c: ChartColors) {
  return {
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: { color: c['muted-foreground'], fontSize: 11, hideOverlap: true },
    splitLine: { lineStyle: { color: c.border, type: 'dashed' as const, opacity: 0.8 } },
  }
}
