import type { PricingPeriod, PricingRateSource } from '@/api/types'
import { Tooltip } from '@/components/ui/tooltip'
import { RATE_SOURCE, formatPeriod } from '@/lib/rate-source'
import { cn } from '@/lib/utils'

/** One chip per rate source with its node count; the cur chip carries the period. */
export function RateSourceChips({
  sources,
  period,
  className,
}: {
  sources: PricingRateSource[]
  period?: PricingPeriod
  className?: string
}) {
  const total = sources.reduce((n, s) => n + s.nodes, 0)
  return (
    <span className={cn('inline-flex flex-wrap items-center gap-1', className)} data-testid="rate-sources">
      {sources.map((s) => {
        const info = RATE_SOURCE[s.source]
        const range = s.source === 'cur' ? formatPeriod(period) : ''
        return (
          <Tooltip
            key={s.source}
            content={`${info.description}${range ? ` Usage ${range}.` : ''} ${s.nodes} of ${total} node${total === 1 ? '' : 's'}.`}
          >
            <span
              tabIndex={0}
              className={cn(
                'inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium tabular outline-none focus-visible:ring-2 focus-visible:ring-ring',
                info.tone,
              )}
            >
              {info.label}
              {range ? <span className="font-normal opacity-80">{range}</span> : null}
              {sources.length > 1 ? <span className="font-normal opacity-80">· {s.nodes}</span> : null}
            </span>
          </Tooltip>
        )
      })}
    </span>
  )
}
