import {
  ActivityIcon,
  ArrowRightIcon,
  PlusIcon,
  WrenchIcon,
  BotIcon,
  CpuIcon,
  MemoryStickIcon,
  PlugZapIcon,
  ServerIcon,
  ShieldAlertIcon,
  SparklesIcon,
} from 'lucide-react'
import { Link } from 'react-router'
import { useAuth } from '@/api/auth'
import { useClusters, useSummary } from '@/api/queries'
import { EnvBadge, PausedBadge } from '@/components/admin/common'
import { ReadinessBadge } from '@/components/admin/readiness'
import type { ClusterInfo, ClusterSummary } from '@/api/types'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import {
  formatBytes,
  formatCores,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatUSD,
  relativeTime,
} from '@/lib/format'
import { cn } from '@/lib/utils'

const STATUS = {
  connected: {
    label: 'Connected',
    dot: 'bg-success',
    text: 'text-success',
    ring: 'ring-success/20 bg-success/10',
    desc: 'The agent called the hub recently.',
  },
  stale: {
    label: 'Stale',
    dot: 'bg-warning',
    text: 'text-warning',
    ring: 'ring-warning/25 bg-warning/12',
    desc: 'No heartbeat within the hub’s threshold. The agent keeps the last values but applies nothing new.',
  },
  never: {
    label: 'Never connected',
    dot: 'bg-muted-foreground/50',
    text: 'text-muted-foreground',
    ring: 'ring-border bg-muted',
    desc: 'The cluster is registered but its agent has not called the hub yet.',
  },
} as const

export default function ClustersPage() {
  const auth = useAuth()
  const clusters = useClusters()
  const summary = useSummary()
  const items = clusters.data?.items ?? []
  const byName = new Map((summary.data?.clusters ?? []).map((c) => [c.cluster, c]))
  const counts = { connected: 0, stale: 0, never: 0 }
  for (const c of items) counts[c.agentStatus]++

  return (
    <div className="space-y-6">
      <PageHeader
        title="Clusters"
        description="Agent health, readiness, coverage and savings per cluster."
        actions={
          clusters.data ? (
            <div className="flex flex-wrap items-center gap-2 text-[13px]">
              {(Object.keys(counts) as (keyof typeof counts)[]).map((k) =>
                counts[k] ? (
                  <span
                    key={k}
                    className={cn(
                      'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 ring-1',
                      STATUS[k].ring,
                    )}
                  >
                    <span className={cn('size-1.5 rounded-full', STATUS[k].dot)} />
                    <span className="font-semibold tabular">{counts[k]}</span>
                    <span className="text-muted-foreground">{STATUS[k].label.toLowerCase()}</span>
                  </span>
                ) : null,
              )}
              {auth.can('admin') ? (
                <Button asChild size="sm" className="ml-1" data-testid="add-cluster">
                  <Link to="/clusters/new">
                    <PlusIcon /> Add cluster
                  </Link>
                </Button>
              ) : null}
            </div>
          ) : null
        }
      />
      {clusters.isError ? (
        <Card>
          <ErrorState error={clusters.error} onRetry={() => clusters.refetch()} />
        </Card>
      ) : !clusters.data ? (
        <div className="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-80 rounded-xl" />
          ))}
        </div>
      ) : !items.length ? (
        <Card>
          <EmptyState
            icon={ServerIcon}
            title="No clusters registered"
            description="Register a cluster in the hub and install the agent with its token. It appears here after the first heartbeat."
            className="py-20"
            action={
              auth.can('admin') ? (
                <Button asChild size="sm">
                  <Link to="/clusters/new">
                    <PlusIcon /> Add cluster
                  </Link>
                </Button>
              ) : undefined
            }
          />
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3" data-testid="cluster-cards">
          {items.map((c) => (
            <ClusterCard key={c.name} c={c} s={byName.get(c.name)} summaryLoading={summary.isPending} />
          ))}
        </div>
      )}
    </div>
  )
}

function Stat({
  icon: Icon,
  label,
  value,
  sub,
  tone,
}: {
  icon: typeof CpuIcon
  label: string
  value: React.ReactNode
  sub?: React.ReactNode
  tone?: string
}) {
  return (
    <div className="rounded-lg border bg-muted/30 px-3 py-2.5">
      <div className="flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">
        <Icon className={cn('size-3.5', tone)} />
        {label}
      </div>
      <div className="mt-1 text-lg leading-tight font-semibold tabular">{value}</div>
      {sub ? <div className="text-[11px] text-muted-foreground">{sub}</div> : null}
    </div>
  )
}

function Bar({
  label,
  icon: Icon,
  requested,
  recommended,
  used,
  fmt,
}: {
  label: string
  icon: typeof CpuIcon
  requested: number
  recommended: number
  used: number
  fmt: (v: number) => string
}) {
  const max = Math.max(requested, recommended, used) || 1
  return (
    <div>
      <div className="mb-1.5 flex items-center justify-between text-xs">
        <span className="flex items-center gap-1.5 font-medium">
          <Icon className="size-3.5 text-muted-foreground" />
          {label}
        </span>
        <span className="font-mono text-muted-foreground tabular">
          {fmt(requested)} → <span className="font-medium text-foreground">{fmt(recommended)}</span>
        </span>
      </div>
      <Tooltip
        content={`Requested ${fmt(requested)} · recommended ${fmt(recommended)} · used ${fmt(used)} (24h avg)`}
      >
        <div className="relative h-2 overflow-hidden rounded-full bg-muted">
          <div
            className="absolute inset-y-0 left-0 rounded-full bg-muted-foreground/25"
            style={{ width: `${(requested / max) * 100}%` }}
          />
          <div
            className="absolute inset-y-0 left-0 rounded-full bg-primary/70"
            style={{ width: `${(recommended / max) * 100}%` }}
          />
          <div
            className="absolute inset-y-0 left-0 rounded-full bg-foreground/60"
            style={{ width: `${(used / max) * 100}%` }}
          />
        </div>
      </Tooltip>
    </div>
  )
}

function ClusterCard({
  c,
  s,
  summaryLoading,
}: {
  c: ClusterInfo
  s?: ClusterSummary
  summaryLoading: boolean
}) {
  const st = STATUS[c.agentStatus]
  const coverage = c.counts.workloads ? c.counts.withRecommendation / c.counts.workloads : 0
  return (
    <Card className="overflow-hidden">
      <div className="flex items-start justify-between gap-3 border-b px-5 py-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <ServerIcon className="size-4 text-muted-foreground" />
            <h2 className="truncate font-semibold tracking-tight">
              <Link to={`/clusters/${encodeURIComponent(c.name)}`} className="hover:underline">
                {c.name}
              </Link>
            </h2>
            <EnvBadge env={c.environment} />
            <PausedBadge paused={c.paused} fleet={c.paused && !c.automation?.paused} />
          </div>
          {c.description ? (
            <p className="mt-1 truncate text-[13px] text-muted-foreground">{c.description}</p>
          ) : null}
        </div>
        <Tooltip content={st.desc}>
          <span
            className={cn(
              'inline-flex shrink-0 items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1',
              st.ring,
              st.text,
            )}
          >
            <span className="relative flex size-1.5">
              {c.agentStatus === 'connected' ? (
                <span
                  className={cn(
                    'absolute inline-flex size-full animate-ping rounded-full opacity-60',
                    st.dot,
                  )}
                />
              ) : null}
              <span className={cn('relative inline-flex size-1.5 rounded-full', st.dot)} />
            </span>
            {st.label}
          </span>
        </Tooltip>
      </div>
      <CardContent className="space-y-4 py-4">
        {c.readiness ? (
          <div className="flex flex-wrap items-center justify-between gap-2" data-testid="cluster-readiness">
            <ReadinessBadge summary={c.readiness} />
            {c.readiness.blocking || c.readiness.warnings ? (
              <Link
                to={`/clusters/${encodeURIComponent(c.name)}?tab=readiness`}
                className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
              >
                <WrenchIcon className="size-3" /> Fix
              </Link>
            ) : null}
          </div>
        ) : null}
        <div className="flex flex-wrap gap-x-5 gap-y-1 text-[13px]">
          <span className="text-muted-foreground">
            Heartbeat{' '}
            <Tooltip content={c.lastHeartbeatAt ? formatDateTime(c.lastHeartbeatAt) : 'never'}>
              <span className="font-medium text-foreground">
                {c.lastHeartbeatAt ? relativeTime(c.lastHeartbeatAt) : 'never'}
              </span>
            </Tooltip>
          </span>
          <span className="text-muted-foreground">
            Agent{' '}
            <span className="font-mono text-xs font-medium text-foreground">{c.agentVersion ?? '—'}</span>
          </span>
          {c.createdAt ? (
            <span className="text-muted-foreground">
              Registered{' '}
              <span className="font-medium text-foreground">{formatDateTime(c.createdAt).split(',')[0]}</span>
            </span>
          ) : null}
        </div>

        {c.agentStatus === 'never' && !c.counts.workloads ? (
          <div className="flex items-start gap-3 rounded-lg border border-dashed px-3.5 py-3 text-[13px]">
            <PlugZapIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <div>
              <div className="font-medium">Waiting for the agent</div>
              <p className="text-muted-foreground">
                Install the agent chart in this cluster with its hub token. Workloads, recommendations and
                savings appear after the first report.
              </p>
            </div>
          </div>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <Stat icon={ServerIcon} label="Workloads" value={formatNumber(c.counts.workloads)} />
              <Stat
                icon={SparklesIcon}
                label="Recommended"
                value={formatPercent(coverage)}
                sub={`${c.counts.withRecommendation} of ${c.counts.workloads}`}
                tone="text-primary"
              />
              <Stat
                icon={BotIcon}
                label="Auto-patch"
                value={formatNumber(c.counts.autoPatch)}
                tone="text-primary"
              />
              <Stat
                icon={ShieldAlertIcon}
                label="Blocked"
                value={formatNumber(c.counts.blocked)}
                tone={c.counts.blocked ? 'text-warning' : undefined}
              />
            </div>
            {s ? (
              <div className="space-y-3">
                <div className="flex items-baseline justify-between">
                  <span className="text-[13px] text-muted-foreground">Estimated savings</span>
                  <span className="text-xl font-semibold text-success tabular">
                    {formatUSD(s.monthlySavingsUSD.total)}
                    <span className="text-xs font-normal text-muted-foreground">/mo</span>
                  </span>
                </div>
                <Bar
                  label="CPU"
                  icon={CpuIcon}
                  requested={s.cpu.requested}
                  recommended={s.cpu.recommended}
                  used={s.cpu.used}
                  fmt={(v) => `${v >= 100 ? formatNumber(Math.round(v)) : formatCores(v)} cores`}
                />
                <Bar
                  label="Memory"
                  icon={MemoryStickIcon}
                  requested={s.memory.requested}
                  recommended={s.memory.recommended}
                  used={s.memory.used}
                  fmt={(v) => formatBytes(v, 1)}
                />
                <div className="flex gap-3 text-[11px] text-muted-foreground">
                  <span className="flex items-center gap-1">
                    <span className="size-2 rounded-sm bg-foreground/60" />
                    Used (24h avg)
                  </span>
                  <span className="flex items-center gap-1">
                    <span className="size-2 rounded-sm bg-primary/70" />
                    Recommended
                  </span>
                  <span className="flex items-center gap-1">
                    <span className="size-2 rounded-sm bg-muted-foreground/25" />
                    Requested
                  </span>
                </div>
              </div>
            ) : summaryLoading ? (
              <div className="space-y-3" aria-busy="true">
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-8 w-full" />
                <Skeleton className="h-8 w-full" />
              </div>
            ) : null}
          </>
        )}
        <div className="flex gap-2 pt-1">
          <Button asChild size="sm" variant="outline" className="flex-1">
            <Link to={`/opportunities?cluster=${encodeURIComponent(c.name)}`}>
              Opportunities <ArrowRightIcon />
            </Link>
          </Button>
          <Button asChild size="sm" variant="ghost" className="flex-1">
            <Link to={`/activity?cluster=${encodeURIComponent(c.name)}`}>
              <ActivityIcon /> Activity
            </Link>
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
