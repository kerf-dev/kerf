import { ChartLineIcon, TriangleAlertIcon } from 'lucide-react'
import { useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { useClusterCapacityHistory } from '@/api/admin'
import type { CapacityHistoryEvent, CapacityHistoryRange } from '@/api/types'
import { Chart } from '@/components/charts/chart'
import { useChartColors } from '@/components/charts/theme'
import { EmptyState, ErrorState } from '@/components/states'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Callout, NativeSelect } from '@/components/ui/form'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { trendOption } from './trend-option'

const RANGES: { value: CapacityHistoryRange; label: string }[] = [
  { value: '7d', label: '7d' },
  { value: '30d', label: '30d' },
  { value: '90d', label: '90d' },
  { value: '1y', label: '1y' },
]
const sinceDate = new Intl.DateTimeFormat('en-GB', {
  day: '2-digit',
  month: 'short',
  year: 'numeric',
  timeZone: 'UTC',
})
const DEFAULT_RANGE: CapacityHistoryRange = '30d'

/** The Activity page offers 24h, 7d, 14d and 30d: the smallest that covers the trend range, else the largest. */
const ACTIVITY_RANGE: Record<CapacityHistoryRange, string> = {
  '7d': '7d',
  '30d': '30d',
  '90d': '30d',
  '1y': '30d',
}

export function CapacityTrend({ cluster }: { cluster: string }) {
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const colors = useChartColors()
  const rawRange = sp.get('trend')
  const range = RANGES.some((r) => r.value === rawRange) ? (rawRange as CapacityHistoryRange) : DEFAULT_RANGE
  const pool = sp.get('pool') || undefined
  const q = useClusterCapacityHistory(cluster, range, pool)
  const data = q.data
  const option = useMemo(() => (data ? trendOption(data, colors) : undefined), [data, colors])

  const set = (key: string, value: string | undefined) =>
    setSp(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true },
    )

  const onMarker = (p: { data?: unknown }) => {
    const e = p.data as Partial<CapacityHistoryEvent> | undefined
    if (!e?.type || e.type === 'pool_added' || e.type === 'pool_removed') return
    // ponytail: the Activity page has no day filter. Add one if users need the exact day.
    navigate(
      `/activity?${new URLSearchParams({ cluster, type: e.type, range: ACTIVITY_RANGE[range] }).toString()}`,
    )
  }

  const pools = data
    ? pool && !data.nodePools.includes(pool)
      ? [...data.nodePools, pool]
      : data.nodePools
    : []

  return (
    <Card data-testid="capacity-trend">
      <CardHeader>
        <CardTitle>Trend</CardTitle>
        <div className="flex flex-wrap items-center gap-2">
          {pools.length > 0 ? (
            <NativeSelect
              aria-label="Node pool"
              value={pool ?? ''}
              onChange={(e) => set('pool', e.target.value || undefined)}
              className="w-44"
            >
              <option value="">All node pools</option>
              {pools.map((n) => (
                <option key={n} value={n}>
                  {n || 'No node pool'}
                </option>
              ))}
            </NativeSelect>
          ) : null}
          <Segmented
            label="Trend range"
            value={range}
            onChange={(v) => set('trend', v === DEFAULT_RANGE ? undefined : v)}
            options={RANGES}
          />
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        ) : !data ? (
          <Skeleton className="h-[580px] w-full" />
        ) : (
          <>
            {data.truncated ? (
              <Callout tone="warning" icon={TriangleAlertIcon}>
                <span data-testid="trend-truncated">
                  Some capacity reports were truncated, so the totals may be low.
                </span>
              </Callout>
            ) : null}
            {data.points.length === 0 ? (
              <EmptyState
                icon={ChartLineIcon}
                title="No history yet"
                description="Kerf records capacity every hour. The first point appears within an hour."
                className="py-10"
              />
            ) : (
              <Chart
                option={option!}
                onClick={onMarker}
                ariaLabel={`Nodes, CPU and memory over the last ${range}, with the changes Kerf made marked`}
                className="h-[580px]"
              />
            )}
            {data.since ? (
              <p className="text-xs text-muted-foreground" data-testid="trend-since">
                History starts {sinceDate.format(Date.parse(data.since))}.
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  )
}
