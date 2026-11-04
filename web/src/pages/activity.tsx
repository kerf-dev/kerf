import { useQuery } from '@tanstack/react-query'
import { ActivityIcon, ChevronDownIcon, Loader2Icon, SearchXIcon, XIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api, unwrap } from '@/api/client'
import { rangeWindow, useAudit, useClusters, type TimeRange } from '@/api/queries'
import { EVENT_TYPES, type AuditEvent, type EventType, type QueryAuditQuery } from '@/api/types'
import { Chart } from '@/components/charts/chart'
import { axisStyle, baseOption, useChartColors } from '@/components/charts/theme'
import { EVENT_INFO, EventTypeBadge, KindBadge } from '@/components/domain'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatNumber } from '@/lib/format'
import { collapseEvents, groupBursts } from '@/lib/collapse'
import { cn } from '@/lib/utils'
import { workloadPath } from '@/lib/workload-id'
import { ResourceDiff } from './workload/history'

// Everything by default: with D-017 agents emit `skipped` once per (workload, reason), and on a fleet
// in audit mode those are the only events, so hiding them would make the page look empty.
const DEFAULT_TYPES: EventType[] = EVENT_TYPES
const ACTION_TYPES: EventType[] = EVENT_TYPES.filter((t) => t !== 'skipped')

export default function ActivityPage() {
  const [sp, setSp] = useSearchParams()
  const types = sp.has('type') ? (sp.getAll('type') as EventType[]) : DEFAULT_TYPES
  const cluster = sp.get('cluster') ?? ''
  const namespace = sp.get('namespace') ?? ''
  const workload = sp.get('workload') ?? ''
  const range = (sp.get('range') as TimeRange | null) ?? '7d'
  const [nsInput, setNsInput] = useState(namespace)
  const [wInput, setWInput] = useState(workload)
  const clusters = useClusters()
  const c = useChartColors()

  const set = (patch: Record<string, string | string[] | undefined>) =>
    setSp(
      (prev) => {
        const next = new URLSearchParams(prev)
        for (const [k, v] of Object.entries(patch)) {
          next.delete(k)
          if (Array.isArray(v)) v.forEach((x) => next.append(k, x))
          else if (v) next.set(k, v)
        }
        return next
      },
      { replace: true },
    )

  const toggleType = (t: EventType) => {
    const next = types.includes(t) ? types.filter((x) => x !== t) : [...types, t]
    set({ type: next.length ? EVENT_TYPES.filter((x) => next.includes(x)) : ['none'] })
  }
  const activeTypes = types.filter((t) => (EVENT_TYPES as string[]).includes(t))

  // `range` is resolved inside the query so the key stays stable across renders.
  const filter: Omit<QueryAuditQuery, 'continue'> = useMemo(
    () => ({
      ...(cluster ? { cluster } : {}),
      ...(namespace ? { namespace } : {}),
      ...(workload ? { workload } : {}),
      ...(activeTypes.length ? { type: activeTypes } : {}),
      ...rangeWindow(range),
      order: 'desc',
      limit: 50,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [cluster, namespace, workload, activeTypes.join(','), range],
  )
  const noTypes = !activeTypes.length
  const audit = useAudit(filter)
  const items = useMemo(
    () => (noTypes ? [] : (audit.data?.pages.flatMap((p) => p.items) ?? [])),
    [noTypes, audit.data],
  )

  // Volume chart: up to 1000 most recent events for the same filters.
  const volume = useQuery({
    queryKey: ['audit-volume', filter],
    queryFn: () => unwrap(api.GET('/v1/audit', { params: { query: { ...filter, limit: 1000 } } })),
    enabled: !noTypes,
  })
  const chartOption = useMemo(() => {
    const ev = volume.data?.items ?? []
    const bucketMs = range === '24h' ? 3600_000 : 86400_000
    const { from, to } = rangeWindow(range)
    const start = Math.floor(Date.parse(from) / bucketMs) * bucketMs
    const end = Date.parse(to)
    const buckets: number[] = []
    for (let t = start; t <= end; t += bucketMs) buckets.push(t)
    const shownTypes = activeTypes.length ? activeTypes : DEFAULT_TYPES
    const colorOf: Record<EventType, string> = {
      applied: c.success,
      skipped: c['muted-foreground'],
      blocked: c.warning,
      fallback: c.info,
      rolled_back: c.destructive,
      released: c['chart-6'],
      failed: c['chart-4'],
    }
    const base = baseOption(c)
    return {
      ...base,
      grid: { ...base.grid, top: 8 },
      tooltip: { ...base.tooltip, axisPointer: { type: 'shadow' } },
      xAxis: {
        type: 'category',
        data: buckets.map((b) => b),
        ...axisStyle(c),
        splitLine: { show: false },
        axisLabel: {
          ...axisStyle(c).axisLabel,
          formatter: (v: string) => {
            const d = new Date(Number(v))
            return range === '24h'
              ? `${String(d.getHours()).padStart(2, '0')}:00`
              : d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' })
          },
        },
      },
      yAxis: { type: 'value', minInterval: 1, ...axisStyle(c) },
      series: shownTypes.map((t) => ({
        name: EVENT_INFO[t].label,
        type: 'bar',
        stack: 'events',
        barMaxWidth: 18,
        itemStyle: { color: colorOf[t], borderRadius: 0 },
        emphasis: { focus: 'series' },
        data: buckets.map(
          (b) =>
            ev.filter((e) => e.type === t && Date.parse(e.time) >= b && Date.parse(e.time) < b + bucketMs)
              .length,
        ),
      })),
    }
  }, [volume.data, range, c, activeTypes])

  const grouped = useMemo(() => {
    const g = new Map<string, AuditEvent[]>()
    for (const e of items) {
      const day = new Date(e.time).toLocaleDateString('en-GB', {
        weekday: 'long',
        day: 'numeric',
        month: 'long',
      })
      g.set(day, [...(g.get(day) ?? []), e])
    }
    return [...g.entries()]
  }, [items])

  const hasFilters = !!(cluster || namespace || workload || sp.has('type'))

  return (
    <div className="space-y-5">
      <PageHeader
        title="Activity"
        description="What the agents decided and did: the audit log from every cluster."
      />

      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Event types">
          <Segmented
            label="Preset"
            value={
              activeTypes.length === EVENT_TYPES.length
                ? 'all'
                : activeTypes.length === ACTION_TYPES.length && !activeTypes.includes('skipped')
                  ? 'actions'
                  : 'custom'
            }
            onChange={(v) =>
              set({ type: v === 'all' ? undefined : v === 'actions' ? ACTION_TYPES : activeTypes })
            }
            options={[
              { value: 'all', label: 'All events' },
              { value: 'actions', label: 'Actions only' },
            ]}
            className="mr-1"
          />
          {EVENT_TYPES.map((t) => {
            const on = activeTypes.includes(t)
            const info = EVENT_INFO[t]
            return (
              <Tooltip key={t} content={info.desc}>
                <button
                  aria-pressed={on}
                  onClick={() => toggleType(t)}
                  className={cn(
                    'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40',
                    on
                      ? 'border-foreground/15 bg-card text-foreground shadow-xs'
                      : 'border-dashed text-muted-foreground hover:text-foreground',
                  )}
                >
                  <span className={cn('size-2 rounded-full', on ? info.dot : 'bg-muted-foreground/30')} />
                  {info.label}
                </button>
              </Tooltip>
            )
          })}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <select
            value={cluster}
            onChange={(e) => set({ cluster: e.target.value })}
            aria-label="Cluster"
            className="h-8 rounded-md border bg-card px-2 text-[13px] shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/30"
          >
            <option value="">All clusters</option>
            {clusters.data?.items.map((cl) => (
              <option key={cl.name} value={cl.name}>
                {cl.name}
              </option>
            ))}
          </select>
          <Input
            value={nsInput}
            onChange={(e) => setNsInput(e.target.value)}
            onBlur={() => set({ namespace: nsInput.trim() })}
            onKeyDown={(e) => e.key === 'Enter' && set({ namespace: nsInput.trim() })}
            placeholder="Namespace"
            aria-label="Namespace"
            className="w-36"
          />
          <Input
            value={wInput}
            onChange={(e) => setWInput(e.target.value)}
            onBlur={() => set({ workload: wInput.trim() })}
            onKeyDown={(e) => e.key === 'Enter' && set({ workload: wInput.trim() })}
            placeholder="Workload name"
            aria-label="Workload name"
            className="w-44"
          />
          <Segmented
            label="Time range"
            value={range}
            onChange={(v) => set({ range: v === '7d' ? undefined : v })}
            options={[
              { value: '24h', label: '24h' },
              { value: '7d', label: '7d' },
              { value: '30d', label: '30d' },
            ]}
          />
          {hasFilters ? (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground"
              onClick={() => {
                setNsInput('')
                setWInput('')
                setSp(new URLSearchParams(range !== '7d' ? { range } : {}), { replace: true })
              }}
            >
              Reset <XIcon />
            </Button>
          ) : null}
        </div>
      </div>

      {/* An empty volume chart adds nothing to the empty state below. */}
      {volume.data && !volume.data.items.length ? null : (
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Volume</CardTitle>
              <CardDescription>
                Events per {range === '24h' ? 'hour' : 'day'}
                {volume.data?.continue ? ' · based on the latest 1,000 events' : ''}
              </CardDescription>
            </div>
          </CardHeader>
          <CardContent className="pt-2 pb-3">
            {volume.isError ? (
              <ErrorState compact error={volume.error} />
            ) : !volume.data && !noTypes ? (
              <Skeleton className="h-36 w-full" />
            ) : (
              <Chart option={chartOption} className="h-36" ariaLabel="Audit events over time" />
            )}
          </CardContent>
        </Card>
      )}

      <Card className="overflow-clip">
        {audit.isError ? (
          <ErrorState error={audit.error} onRetry={() => audit.refetch()} />
        ) : !audit.data && !noTypes ? (
          <div className="divide-y">
            {Array.from({ length: 8 }, (_, i) => (
              <div key={i} className="flex gap-4 px-5 py-4">
                <Skeleton className="h-4 w-12" />
                <Skeleton className="h-5 w-20" />
                <div className="flex-1 space-y-1.5">
                  <Skeleton className="h-4 w-1/3" />
                  <Skeleton className="h-3 w-2/3" />
                </div>
              </div>
            ))}
          </div>
        ) : !items.length ? (
          <EmptyState
            icon={hasFilters ? SearchXIcon : ActivityIcon}
            title={
              noTypes
                ? 'No event types selected'
                : hasFilters
                  ? 'No events match these filters'
                  : 'No activity yet'
            }
            description={
              noTypes
                ? 'Select at least one event type above.'
                : hasFilters
                  ? 'Widen the time range or remove a filter.'
                  : 'Events appear here when agents apply, skip or block recommendations.'
            }
            className="py-16"
          />
        ) : (
          <div data-testid="activity-list">
            {grouped.map(([day, evs]) => (
              <section key={day}>
                <h2 className="sticky top-14 z-10 border-b bg-muted/70 px-5 py-1.5 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase backdrop-blur">
                  {day} <span className="ml-1 font-normal normal-case">· {evs.length}</span>
                </h2>
                <ul className="divide-y divide-border/60">
                  {groupBursts(dedupe(evs)).map((g) =>
                    g.length === 1 ? (
                      <EventRow key={g[0]!.id} e={g[0]!} repeats={repeatCount.get(g[0]!.id)} />
                    ) : (
                      <BurstRow key={g[0]!.id} events={g} />
                    ),
                  )}
                </ul>
              </section>
            ))}
            <div className="flex items-center justify-between border-t px-5 py-3 text-[13px] text-muted-foreground">
              <span>
                Showing{' '}
                <span className="font-medium text-foreground tabular">{formatNumber(items.length)}</span>{' '}
                events
              </span>
              {audit.hasNextPage ? (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => audit.fetchNextPage()}
                  disabled={audit.isFetchingNextPage}
                >
                  {audit.isFetchingNextPage ? <Loader2Icon className="animate-spin" /> : null} Load more
                </Button>
              ) : (
                <span>End of log</span>
              )}
            </div>
          </div>
        )}
      </Card>
    </div>
  )
}

// Identical consecutive events for the same workload (agent retries/repeats) show once with a count.
const repeatCount = new Map<string, number>()
function dedupe(evs: AuditEvent[]): AuditEvent[] {
  return collapseEvents(evs).map((r) => {
    if (r.count > 1) repeatCount.set(r.latest.id, r.count)
    return r.latest
  })
}

function EventRow({ e, repeats }: { e: AuditEvent; repeats?: number }) {
  const [open, setOpen] = useState(false)
  const expandable = !!(e.containers?.length || (e.details && Object.keys(e.details).length))
  const t = new Date(e.time)
  return (
    <li className="group px-5 py-3 transition-colors hover:bg-accent/30">
      <div className="flex gap-4">
        <Tooltip content={formatDateTime(e.time)}>
          <time
            dateTime={e.time}
            className="w-11 shrink-0 pt-0.5 font-mono text-xs text-muted-foreground tabular"
          >
            {String(t.getHours()).padStart(2, '0')}:{String(t.getMinutes()).padStart(2, '0')}
          </time>
        </Tooltip>
        <div className="w-24 shrink-0 pt-px">
          <EventTypeBadge type={e.type} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[13px]">
            {e.workloadId ? (
              <Link to={workloadPath(e.workloadId)} className="font-medium hover:underline">
                {e.workload.name}
              </Link>
            ) : (
              <span className="font-medium">{e.workload.name}</span>
            )}
            <KindBadge kind={e.workload.kind} />
            <span className="text-xs text-muted-foreground">
              {e.workload.namespace} · {e.cluster}
            </span>
          </div>
          <div className="mt-0.5 flex flex-wrap items-baseline gap-x-2 text-[13px]">
            <span className="font-mono text-xs text-foreground/90">{e.reason}</span>
            {repeats ? (
              <span className="rounded-full bg-muted px-1.5 font-mono text-[10px] font-medium text-muted-foreground">
                ×{repeats}
              </span>
            ) : null}
            {e.resources?.length ? (
              <span className="text-xs text-muted-foreground">{e.resources.join(' + ')}</span>
            ) : null}
            {e.strategy ? <span className="text-xs text-muted-foreground">via {e.strategy}</span> : null}
          </div>
          {e.message ? (
            <p className="mt-0.5 text-[13px] text-pretty text-muted-foreground">{e.message}</p>
          ) : null}
          {open ? (
            <div className="mt-2 space-y-2 rounded-lg border bg-muted/30 p-3">
              {e.containers?.map((c) => (
                <div key={c.name}>
                  <div className="font-mono text-[11px] text-muted-foreground">{c.name}</div>
                  <ResourceDiff from={c.from} to={c.to} />
                </div>
              ))}
              {e.details ? (
                <div className="flex flex-wrap gap-1">
                  {Object.entries(e.details).map(([k, v]) => (
                    <span
                      key={k}
                      className="rounded border bg-card px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                    >
                      {k}={v}
                    </span>
                  ))}
                </div>
              ) : null}
              {e.recommendationID ? (
                <div className="font-mono text-[10px] text-muted-foreground">
                  recommendation {e.recommendationID}
                </div>
              ) : null}
            </div>
          ) : null}
        </div>
        {expandable ? (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-expanded={open}
            aria-label={open ? 'Hide details' : 'Show details'}
            onClick={() => setOpen((o) => !o)}
            className="shrink-0 text-muted-foreground"
          >
            <ChevronDownIcon className={cn('transition-transform', open && 'rotate-180')} />
          </Button>
        ) : (
          <span className="size-8 shrink-0" />
        )}
      </div>
    </li>
  )
}

/** Many workloads, same decision, same moment (e.g. one agent loop over an audit-mode cluster). */
function BurstRow({ events }: { events: AuditEvent[] }) {
  const [open, setOpen] = useState(false)
  const e = events[0]!
  const t = new Date(e.time)
  const clusters = [...new Set(events.map((x) => x.cluster))]
  const shown = open ? events : events.slice(0, 6)
  return (
    <li className="px-5 py-3 transition-colors hover:bg-accent/30" data-testid="activity-burst">
      <div className="flex gap-4">
        <Tooltip content={`${formatDateTime(events[events.length - 1]!.time)} – ${formatDateTime(e.time)}`}>
          <time
            dateTime={e.time}
            className="w-11 shrink-0 pt-0.5 font-mono text-xs text-muted-foreground tabular"
          >
            {String(t.getHours()).padStart(2, '0')}:{String(t.getMinutes()).padStart(2, '0')}
          </time>
        </Tooltip>
        <div className="w-24 shrink-0 pt-px">
          <EventTypeBadge type={e.type} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2 text-[13px]">
            <span className="font-medium">{events.length} workloads</span>
            <span className="text-xs text-muted-foreground">{clusters.join(', ')}</span>
          </div>
          <div className="mt-0.5 flex flex-wrap items-baseline gap-x-2 text-[13px]">
            <span className="font-mono text-xs text-foreground/90">{e.reason}</span>
            {e.message ? <span className="text-muted-foreground">{e.message}</span> : null}
          </div>
          <div className="mt-2 flex flex-wrap gap-1">
            {shown.map((x) =>
              x.workloadId ? (
                <Link
                  key={x.id}
                  to={workloadPath(x.workloadId)}
                  className="inline-flex items-center gap-1 rounded-md border bg-card px-1.5 py-0.5 text-[11px] hover:bg-accent"
                >
                  <span className="font-medium">{x.workload.name}</span>
                  <span className="text-muted-foreground">
                    {x.workload.namespace}
                    {clusters.length > 1 ? ` · ${x.cluster}` : ''}
                  </span>
                </Link>
              ) : (
                <span key={x.id} className="rounded-md border px-1.5 py-0.5 text-[11px]">
                  {x.workload.name}
                </span>
              ),
            )}
            {events.length > 6 ? (
              <button
                onClick={() => setOpen((o) => !o)}
                aria-expanded={open}
                className="rounded-md px-1.5 py-0.5 text-[11px] font-medium text-primary hover:underline"
              >
                {open ? 'Show less' : `+${events.length - 6} more`}
              </button>
            ) : null}
          </div>
        </div>
        <span className="size-8 shrink-0" />
      </div>
    </li>
  )
}
