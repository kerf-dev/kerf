import { HistoryIcon, SparklesIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useHistory } from '@/api/queries'
import type { AuditEvent, Resources, WorkloadRecommendation } from '@/api/types'
import { EVENT_INFO, EventTypeBadge, Savings } from '@/components/domain'
import { EmptyState, ErrorState } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatPercent, formatQuantity, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'

type Item =
  | { kind: 'rec'; time: string; rec: WorkloadRecommendation; prev?: WorkloadRecommendation }
  | { kind: 'event'; time: string; ev: AuditEvent }

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
    const recs = history.data.recommendations
    const out: Item[] = [
      ...recs.map((rec, i) => ({ kind: 'rec' as const, time: rec.generatedAt, rec, prev: recs[i + 1] })),
      ...history.data.events.map((ev) => ({ kind: 'event' as const, time: ev.time, ev })),
    ]
    return out
      .sort((a, b) => b.time.localeCompare(a.time))
      .filter((x) => filter === 'all' || (filter === 'events' ? x.kind === 'event' : x.kind === 'rec'))
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
                  key={it.kind === 'rec' ? `r-${it.rec.id}` : `e-${it.ev.id}`}
                  className="relative flex gap-3 pb-5 last:pb-0"
                >
                  {it.kind === 'rec' ? <RecItem rec={it.rec} prev={it.prev} /> : <EventItem ev={it.ev} />}
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

function When({ t }: { t: string }) {
  return (
    <Tooltip content={formatDateTime(t)}>
      <time dateTime={t} className="shrink-0 text-xs whitespace-nowrap text-muted-foreground">
        {relativeTime(t)}
      </time>
    </Tooltip>
  )
}

function RecItem({ rec, prev }: { rec: WorkloadRecommendation; prev?: WorkloadRecommendation }) {
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
            <span className="font-medium">New recommendation</span>
            <span className="text-muted-foreground">
              {' '}
              · confidence {formatPercent(rec.confidence)} ·{' '}
              <Savings value={rec.monthlySavingsUSD.total} className="text-[13px]" />
              /mo
            </span>
          </div>
          <When t={rec.generatedAt} />
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

function EventItem({ ev }: { ev: AuditEvent }) {
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
            {ev.resources?.length ? (
              <span className="text-xs text-muted-foreground">· {ev.resources.join(', ')}</span>
            ) : null}
            {ev.strategy ? <span className="text-xs text-muted-foreground">· {ev.strategy}</span> : null}
          </div>
          <When t={ev.time} />
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
