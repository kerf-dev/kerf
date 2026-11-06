import type { PricingPeriod, PricingRateSource } from '@/api/types'

export type Source = PricingRateSource['source']

/** Where a node's rate comes from (the pricing service's `source` label). */
export const RATE_SOURCE: Record<Source, { label: string; description: string; tone: string }> = {
  cur: {
    label: 'CUR',
    description:
      'Effective rate paid, from the AWS Cost and Usage Report: Savings Plans and Reserved Instances amortised.',
    tone: 'bg-success/14 text-success',
  },
  list: {
    label: 'list',
    description: 'AWS on-demand list price (the report had no usage for this instance type, or is off).',
    tone: 'bg-muted text-muted-foreground',
  },
  spot: {
    label: 'spot',
    description: 'Current EC2 spot price.',
    tone: 'bg-info/12 text-info',
  },
  static: {
    label: 'static',
    description: 'From the static price file.',
    tone: 'bg-muted text-muted-foreground',
  },
}

const dateFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' })

/** "Oct 7 – Nov 5", the usage period effective rates were computed from. */
export function formatPeriod(p?: PricingPeriod): string {
  if (!p) return ''
  const a = Date.parse(p.start)
  const b = Date.parse(p.end)
  if (Number.isNaN(a) || Number.isNaN(b)) return ''
  return `${dateFmt.format(a)} – ${dateFmt.format(b)}`
}
