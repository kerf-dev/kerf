import { HistoryIcon, SettingsIcon, SparklesIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useHistory } from '@/api/queries'
import type { ConfigChange, Resources, WorkloadRecommendation } from '@/api/types'
import { actionLabel } from '@/components/admin/config-audit'
import { EVENT_INFO, EventTypeBadge, Savings } from '@/components/domain'
import { EmptyState, ErrorState } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatPercent, formatQuantity, relativeTime } from '@/lib/format'
import { collapseEvents, collapseRecommendations, type EventRun, type RecRun } from '@/lib/collapse'
import { cn } from '@/lib/utils'

type Item =
  | { kind: 'rec'; time: string; run: RecRun }
  | { kind: 'event'; time: string; run: EventRun }
  | { kind: 'config'; time: string; change: ConfigChange }

export function ResourceDiff({ from, to }: { from?: Resources; to?: Resources }) {
  const rows: [string, 'cpu' | 'memory', 'requests' | 'limits'][] = [
    ['CPU req', 'cpu', 'requests'],
    ['CPU lim', 'cpu', 'limits'],
    ['Mem req', 'memory', 'requests'],
    ['Mem lim', 'memory', 'limits'],
  ]
  const shown = rows.filter(([, r, k]) => from?.[k]?.[r] || to?.[k]?.[r])
  if (!shown.length) return null
  return (
    <div className="mt-1.5 inline-grid grid-cols-[auto_auto_auto_auto] items-center gap-x-2 gap-y-0.5 font-mono text-[11px]">
      {shown.map(([label, r, k]) => (
        <span key={label} className="contents">
          <span className="text-muted-foreground">{label}</span>
          <span className="text-muted-foreground">{formatQuantity(r, from?.[k]?.[r]) || '—'}</span>
          <span className="text-muted-foreground">→</span>
          <span className="font-medium">{to?.[k]?.[r] ? formatQuantity(r, to[k][r]) : 'removed'}</span>
        </span>
      ))}
    </div>
  )
}

function mainOf(rec: WorkloadRecommendation) {
  return rec.containers.find((c) => !c.init && !(c.riskFlags ?? []).includes('sidecar')) ?? rec.containers[0]
}

export function HistoryCard({ id }: { id: string }) {
  const history = useHistory(id)
  const [filter, setFilter] = useState<'all' | 'events' | 'recs'>('all')
  const [limit, setLimit] = useState(12)
  const items = useMemo<Item[]>(() => {
    if (!history.data) return []
    // Repeats (same recommended values, same event) collapse into one entry with a count.
    const out: Item[] = [
      ...collapseRecommendations(history.data.recommendations).map((run) => ({
        kind: 'rec' as const,
        time: run.latest.generatedAt,
        run,
      })),
      ...collapseEvents(history.data.events).map((run) => ({
        kind: 'event' as const,
        time: run.latest.time,
        run,
      })),
      // Overrides and approvals made in the admin area (D-022, D-024).
      ...(history.data.configChanges ?? []).map((change) => ({
        kind: 'config' as const,
        time: change.time,
        change,
      })),
    ]
    return out
      .sort((a, b) => b.time.localeCompare(a.time))
      .filter((x) => filter === 'all' || (filter === 'events' ? x.kind !== 'rec' : x.kind === 'rec'))
  }, [history.data, filter])

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>History</CardTitle>
          <CardDescription>Recommendations and agent actions, newest first (last 30 days)</CardDescription>
        </div>
        <Segmented
          label="Show"
          value={filter}
          onChange={(v) => {
            setFilter(v)
            setLimit(12)
          }}
          options={[
            { value: 'all', label: 'All' },
            { value: 'events', label: 'Actions' },
            { value: 'recs', label: 'Recommendations' },
          ]}
        />
      </CardHeader>
      <CardContent className="pt-4">
        {history.isError ? (
          <ErrorState compact error={history.error} onRetry={() => history.refetch()} />
        ) : !history.data ? (
          <div className="space-y-4">
            {Array.from({ length: 4 }, (_, i) => (
              <div key={i} className="flex gap-3">
                <Skeleton className="size-6 rounded-full" />
                <div className="flex-1 space-y-1.5">
                  <Skeleton className="h-4 w-1/3" />
                  <Skeleton className="h-3 w-2/3" />
                </div>
              </div>
            ))}
          </div>
        ) : !items.length ? (
          <EmptyState
            icon={HistoryIcon}
            title="Nothing here yet"
            description={
              filter === 'events'
                ? 'The agent has not acted on this workload in the last 30 days.'
                : 'No recommendations in the last 30 days.'
            }
          />
        ) : (
          <>
            <ol className="relative" data-testid="history-timeline">
              <span aria-hidden className="absolute top-3 bottom-3 left-3 w-px bg-border" />
              {items.slice(0, limit).map((it) => (
                <li
                  key={
                    it.kind === 'rec'
                      ? `r-${it.run.latest.id}`
                      : it.kind === 'event'
                        ? `e-${it.run.latest.id}`
                        : `c-${it.change.id}`
                  }
                  className="relative flex gap-3 pb-5 last:pb-0"
                >
                  {it.kind === 'rec' ? (
                    <RecItem run={it.run} />
                  ) : it.kind === 'event' ? (
                    <EventItem run={it.run} />
                  ) : (
                    <ConfigItem c={it.change} />
                  )}
                </li>
              ))}
            </ol>
            {items.length > limit ? (
              <div className="mt-4 flex justify-center">
                <Button variant="outline" size="sm" onClick={() => setLimit((l) => l + 20)}>
                  Show more ({items.length - limit})
                </Button>
              </div>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  )
}

function When({ t, since }: { t: string; since?: string }) {
  return (
    <Tooltip content={since ? `${formatDateTime(since)} – ${formatDateTime(t)}` : formatDateTime(t)}>
      <time dateTime={t} className="shrink-0 text-xs whitespace-nowrap text-muted-foreground">
        {relativeTime(t)}
      </time>
    </Tooltip>
  )
}

function Repeats({ count, since, what }: { count: number; since: string; what: string }) {
  if (count < 2) return null
  return (
    <Tooltip content={`${count} ${what} with the same values since ${formatDateTime(since)}`}>
      <span className="ml-1.5 inline-flex h-[18px] items-center rounded-full bg-muted px-1.5 align-middle font-mono text-[10px] font-medium text-muted-foreground">
        ×{count}
      </span>
    </Tooltip>
  )
}

function RecItem({ run }: { run: RecRun }) {
  const rec = run.latest
  const prev = run.previous
  const m = mainOf(rec)
  const pm = prev ? prev.containers.find((c) => c.name === m?.name) : undefined
  return (
    <>
      <span className="z-10 flex size-6 shrink-0 items-center justify-center rounded-full border bg-card">
        <SparklesIcon className="size-3 text-primary" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-3">
          <div className="text-[13px]">
            <span className="font-medium">{prev ? 'Recommendation changed' : 'First recommendation'}</span>
            <Repeats count={run.count} since={run.first.generatedAt} what="runs" />
            <span className="text-muted-foreground">
              {' '}
              · confidence {formatPercent(rec.confidence)} ·{' '}
              <Savings value={rec.monthlySavingsUSD.total} className="text-[13px]" />
              /mo
            </span>
          </div>
          <When t={rec.generatedAt} since={run.count > 1 ? run.first.generatedAt : undefined} />
        </div>
        {m ? (
          <div className="mt-0.5 font-mono text-[11px] text-muted-foreground">
            {m.name}: cpu {formatQuantity('cpu', m.recommended.requests?.cpu)}
            {pm && pm.recommended.requests?.cpu !== m.recommended.requests?.cpu ? (
              <span className="opacity-70"> (was {formatQuantity('cpu', pm.recommended.requests?.cpu)})</span>
            ) : null}{' '}
            · mem {formatQuantity('memory', m.recommended.requests?.memory)}
            {pm && pm.recommended.requests?.memory !== m.recommended.requests?.memory ? (
              <span className="opacity-70">
                {' '}
                (was {formatQuantity('memory', pm.recommended.requests?.memory)})
              </span>
            ) : null}
          </div>
        ) : null}
      </div>
    </>
  )
}

function ConfigItem({ c }: { c: ConfigChange }) {
  return (
    <>
      <span className="z-10 flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/12 text-primary">
        <SettingsIcon className="size-3" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex flex-wrap items-center gap-1.5 text-[13px]">
            <span className="font-medium">{actionLabel(c.action)}</span>
            <span className="text-xs text-muted-foreground">by {c.actor.name}</span>
          </div>
          <When t={c.time} />
        </div>
        {c.reason ? <p className="mt-1 text-[13px] text-pretty text-muted-foreground">“{c.reason}”</p> : null}
      </div>
    </>
  )
}

function EventItem({ run }: { run: EventRun }) {
  const ev = run.latest
  const info = EVENT_INFO[ev.type]
  const Icon = info.icon
  return (
    <>
      <span className={cn('z-10 flex size-6 shrink-0 items-center justify-center rounded-full', info.cls)}>
        <Icon className="size-3" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex flex-wrap items-center gap-1.5 text-[13px]">
            <EventTypeBadge type={ev.type} />
            <span className="font-mono text-xs font-medium">{ev.reason}</span>
            <Repeats count={run.count} since={run.first.time} what="events" />
            {ev.resources?.length ? (
              <span className="text-xs text-muted-foreground">· {ev.resources.join(', ')}</span>
            ) : null}
            {ev.strategy ? <span className="text-xs text-muted-foreground">· {ev.strategy}</span> : null}
          </div>
          <When t={ev.time} since={run.count > 1 ? run.first.time : undefined} />
        </div>
        {ev.message ? (
          <p className="mt-1 text-[13px] text-pretty text-muted-foreground">{ev.message}</p>
        ) : null}
        {ev.containers?.map((c) => (
          <div key={c.name} className="mt-1">
            <span className="font-mono text-[11px] text-muted-foreground">{c.name}</span>
            <div>
              <ResourceDiff from={c.from} to={c.to} />
            </div>
          </div>
        ))}
        {ev.details && Object.keys(ev.details).length ? (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {Object.entries(ev.details).map(([k, v]) => (
              <span
                key={k}
                className="rounded border bg-muted/40 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
              >
                {k}={v}
              </span>
            ))}
          </div>
        ) : null}
      </div>
    </>
  )
}
