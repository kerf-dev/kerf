import { ChevronRightIcon, HistoryIcon, Loader2Icon } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { useConfigAudit } from '@/api/admin'
import type { ConfigAction, ConfigChange, ConfigTargetType } from '@/api/types'
import { EmptyState, ErrorState } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { NativeSelect } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { diffLines } from '@/lib/diff'
import { formatDateTime, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { workloadPath } from '@/lib/workload-id'
import { toYaml } from '@/lib/yaml'
import { ActorChip } from './common'

export const TARGET_LABEL: Record<ConfigTargetType, string> = {
  settings: 'Settings',
  policy: 'Policy',
  'workload-override': 'Workload override',
  approval: 'Approval',
  cluster: 'Cluster',
  automation: 'Automation',
  token: 'Token',
  user: 'User',
  datasource: 'Datasource',
  readiness: 'Readiness',
  notification: 'Notification',
  team: 'Team',
}

const ACTION_TONE: Partial<
  Record<ConfigAction, 'success' | 'destructive' | 'warning' | 'info' | 'muted' | 'default'>
> = {
  PolicyCreated: 'success',
  ClusterCreated: 'success',
  UserCreated: 'success',
  TokenCreated: 'success',
  DatasourceCreated: 'success',
  NotificationChannelCreated: 'success',
  NotificationRuleCreated: 'success',
  TeamCreated: 'success',
  PolicyDeleted: 'destructive',
  ClusterDeleted: 'destructive',
  UserDeleted: 'destructive',
  TokenRevoked: 'destructive',
  DatasourceDeleted: 'destructive',
  NotificationChannelDeleted: 'destructive',
  NotificationRuleDeleted: 'destructive',
  TeamDeleted: 'destructive',
  AutomationPaused: 'warning',
  PolicyReverted: 'warning',
  AutomationResumed: 'success',
  WorkloadApproved: 'info',
  ReadinessChanged: 'muted',
}

/** "PolicyUpdated" → "Policy updated". */
export const actionLabel = (a: string) =>
  a.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/ (\w)/g, (m) => m.toLowerCase())

function targetLink(c: ConfigChange): string | undefined {
  const t = c.target
  if (t.type === 'policy' || t.type === 'workload-override')
    return t.type === 'policy'
      ? `/admin/policies/${t.id}`
      : t.workloadId
        ? workloadPath(t.workloadId)
        : undefined
  if (t.type === 'cluster' || t.type === 'readiness')
    return t.cluster || t.id !== '*'
      ? `/clusters/${encodeURIComponent(t.cluster ?? t.id)}${t.type === 'readiness' ? '?tab=readiness' : ''}`
      : undefined
  if (t.type === 'automation') return t.id === '*' ? '/admin' : `/clusters/${encodeURIComponent(t.id)}`
  if (t.type === 'approval' && t.workloadId) return workloadPath(t.workloadId)
  if (t.type === 'settings') return t.id === 'pricing' ? '/admin/pricing' : '/admin/recommender'
  if (t.type === 'user' || t.type === 'token')
    return `/admin/access${t.type === 'token' ? '?tab=tokens' : ''}`
  if (t.type === 'datasource') return '/admin/datasources'
  if (t.type === 'notification') return '/admin/notifications'
  return undefined
}

function Diff({ before, after }: { before?: unknown; after?: unknown }) {
  const strip = (o: unknown) => {
    if (!o || typeof o !== 'object') return o
    const rest = { ...(o as Record<string, unknown>) }
    for (const k of ['createdAt', 'updatedAt', 'createdBy', 'updatedBy', 'version']) delete rest[k]
    return rest
  }
  const a = before ? toYaml(strip(before)) : ''
  const b = after ? toYaml(strip(after)) : ''
  const d = diffLines(a.trimEnd(), b.trimEnd())
  const changed = d.filter((l) => l.op !== 'same').length
  if (!changed) return <p className="px-4 py-3 text-xs text-muted-foreground">No field changes recorded.</p>
  // Show changed lines with two lines of context.
  const keep = new Set<number>()
  d.forEach((l, i) => {
    if (l.op !== 'same') for (let k = Math.max(0, i - 2); k <= Math.min(d.length - 1, i + 2); k++) keep.add(k)
  })
  return (
    <pre
      className="max-h-80 overflow-auto py-2 font-mono text-[12px] leading-relaxed"
      data-testid="audit-diff"
    >
      {d.map((l, i) =>
        keep.has(i) ? (
          <div
            key={i}
            className={cn(
              'px-4 whitespace-pre',
              l.op === 'add' &&
                'bg-success/10 text-[color-mix(in_oklch,var(--success)_70%,var(--foreground))]',
              l.op === 'del' && 'bg-destructive/8 text-destructive',
            )}
          >
            <span className="mr-3 inline-block w-3 select-none text-muted-foreground">
              {l.op === 'add' ? '+' : l.op === 'del' ? '−' : ' '}
            </span>
            {l.text || ' '}
          </div>
        ) : keep.has(i - 1) ? (
          <div key={i} className="px-4 text-muted-foreground/60 select-none">
            ⋯
          </div>
        ) : null,
      )}
    </pre>
  )
}

function ChangeRow({ c }: { c: ConfigChange }) {
  const [open, setOpen] = useState(false)
  const link = targetLink(c)
  const hasDiff = c.before != null || c.after != null
  return (
    <li className="border-b last:border-0" data-testid="config-change">
      <div className="flex items-start gap-3 px-4 py-3">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          disabled={!hasDiff}
          aria-expanded={hasDiff ? open : undefined}
          aria-label="Show the change"
          className="mt-0.5 rounded text-muted-foreground hover:text-foreground disabled:opacity-30"
        >
          <ChevronRightIcon className={cn('size-4 transition-transform', open && 'rotate-90')} />
        </button>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Badge variant={ACTION_TONE[c.action] ?? 'default'}>{actionLabel(c.action)}</Badge>
            <span className="text-xs text-muted-foreground">{TARGET_LABEL[c.target.type]}</span>
            {link ? (
              <Link to={link} className="truncate font-mono text-[13px] font-medium hover:underline">
                {c.target.name ?? c.target.workloadId ?? c.target.id}
              </Link>
            ) : (
              <span className="truncate font-mono text-[13px] font-medium">
                {c.target.name ?? c.target.id}
              </span>
            )}
            {c.target.cluster && c.target.type !== 'cluster' ? (
              <span className="text-xs text-muted-foreground">· {c.target.cluster}</span>
            ) : null}
            {c.version ? (
              <span className="font-mono text-[11px] text-muted-foreground">v{c.version}</span>
            ) : null}
          </div>
          {c.reason ? <p className="mt-1 text-[13px] text-pretty">“{c.reason}”</p> : null}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-0.5">
          <ActorChip actor={c.actor} />
          <Tooltip content={formatDateTime(c.time)}>
            <span className="text-xs text-muted-foreground">{relativeTime(c.time)}</span>
          </Tooltip>
        </div>
      </div>
      {open && hasDiff ? (
        <div className="mx-4 mb-3 overflow-hidden rounded-lg border bg-muted/20">
          <Diff before={c.before} after={c.after} />
        </div>
      ) : null}
    </li>
  )
}

/** Configuration changes (`GET /v1/audit/config`): who changed what, when, why, with a before/after diff. */
export function ConfigAuditList({
  cluster,
  workloadId,
  compact,
}: {
  cluster?: string
  workloadId?: string
  compact?: boolean
}) {
  const [targetType, setTargetType] = useState<ConfigTargetType | ''>('')
  const [actor, setActor] = useState('')
  const q = useConfigAudit({
    order: 'desc',
    limit: 50,
    ...(targetType ? { targetType: [targetType] } : {}),
    ...(actor ? { actor } : {}),
    ...(cluster ? { cluster } : {}),
    ...(workloadId ? { workloadId } : {}),
  })
  const items = q.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <div className="space-y-3">
      {!compact ? (
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect
            value={targetType}
            onChange={(e) => setTargetType(e.target.value as ConfigTargetType | '')}
            className="w-48"
            aria-label="Target type"
          >
            <option value="">All changes</option>
            {Object.entries(TARGET_LABEL).map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </NativeSelect>
          <Input
            value={actor}
            onChange={(e) => setActor(e.target.value.trim())}
            placeholder="Actor, e.g. alice"
            className="w-48"
            aria-label="Actor"
          />
        </div>
      ) : null}
      <Card className="overflow-hidden">
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : !items.length ? (
          <EmptyState
            icon={HistoryIcon}
            title="No configuration changes"
            description={
              targetType || actor
                ? 'Nothing matches the filters.'
                : 'Changes made in the admin area show up here with who made them and why.'
            }
          />
        ) : (
          <>
            <ul data-testid="config-audit">
              {items.map((c) => (
                <ChangeRow key={c.id} c={c} />
              ))}
            </ul>
            {q.hasNextPage ? (
              <div className="border-t p-2 text-center">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => q.fetchNextPage()}
                  disabled={q.isFetchingNextPage}
                >
                  {q.isFetchingNextPage ? <Loader2Icon className="animate-spin" /> : null} Load older changes
                </Button>
              </div>
            ) : null}
          </>
        )}
      </Card>
    </div>
  )
}
