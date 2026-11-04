import {
  ArrowRightIcon,
  BotIcon,
  CircleOffIcon,
  EyeIcon,
  PauseCircleIcon,
  PauseIcon,
  PlayIcon,
  ShieldCheckIcon,
} from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { useFleetAutomation } from '@/api/admin'
import { useAuth } from '@/api/auth'
import { useClusters, useSummary } from '@/api/queries'
import type { Automation, ClusterInfo, ClusterSummary } from '@/api/types'
import { ActorChip, EnvBadge, PausedBadge, POLICY_SOURCE_LABEL, SyncBadge } from '@/components/admin/common'
import { PauseDialog } from '@/components/admin/pause-dialog'
import { ReadinessBadge } from '@/components/admin/readiness'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatNumber, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'

function PauseSummary({ a }: { a: Automation }) {
  if (!a.paused) return null
  return (
    <div className="flex flex-col gap-1 text-[13px]">
      <div className="text-pretty">
        <span className="text-muted-foreground">Reason: </span>
        {a.reason || '—'}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground">
        <ActorChip actor={a.by} time={a.since} />
        {a.resumeAt ? (
          <span>· resumes {formatDateTime(a.resumeAt, true)}</span>
        ) : (
          <span>· until resumed by hand</span>
        )}
      </div>
    </div>
  )
}

function ModeBar({ s }: { s?: ClusterSummary }) {
  if (!s || !s.workloads) return <div className="h-2 rounded-full bg-muted" />
  // A workload counts as auto if either resource is auto-patch (as the cluster counts do).
  const total = s.workloads
  const segs = [
    {
      k: 'auto-patch',
      n: Math.max(s.workloadsByMode.cpu['auto-patch'], s.workloadsByMode.memory['auto-patch']),
      cls: 'bg-primary',
      icon: BotIcon,
      label: 'auto-patch',
    },
    {
      k: 'audit',
      n: Math.min(s.workloadsByMode.cpu.audit, s.workloadsByMode.memory.audit),
      cls: 'bg-info/70',
      icon: EyeIcon,
      label: 'audit',
    },
    {
      k: 'off',
      n: Math.min(s.workloadsByMode.cpu.off, s.workloadsByMode.memory.off),
      cls: 'bg-muted-foreground/30',
      icon: CircleOffIcon,
      label: 'off',
    },
  ]
  return (
    <div>
      <div className="flex h-2 overflow-hidden rounded-full bg-muted">
        {segs.map((x) => (
          <div key={x.k} className={x.cls} style={{ width: `${(x.n / total) * 100}%` }} />
        ))}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
        {segs.map((x) => (
          <span key={x.k} className="inline-flex items-center gap-1">
            <x.icon className="size-3" /> {formatNumber(x.n)} {x.label}
          </span>
        ))}
      </div>
    </div>
  )
}

function ClusterAutomationCard({
  c,
  s,
  fleetPaused,
  onToggle,
  canPause,
  canResume,
}: {
  c: ClusterInfo
  s?: ClusterSummary
  fleetPaused: boolean
  onToggle: () => void
  canPause: boolean
  canResume: boolean
}) {
  const own = c.automation ?? { paused: false, version: 0 }
  const allowed = own.paused ? canResume : canPause
  return (
    <Card
      className={cn('overflow-hidden', own.paused && 'border-warning/40')}
      data-testid={`automation-${c.name}`}
    >
      <div
        className={cn('flex items-start justify-between gap-3 px-5 pt-4 pb-3', own.paused && 'bg-warning/6')}
      >
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Link
              to={`/clusters/${encodeURIComponent(c.name)}`}
              className="truncate font-semibold hover:underline"
            >
              {c.name}
            </Link>
            <EnvBadge env={c.environment} />
            <PausedBadge paused={own.paused} />
            {!own.paused && fleetPaused ? <PausedBadge paused fleet /> : null}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {c.owner ? `${c.owner} · ` : ''}
            {c.policySource ? POLICY_SOURCE_LABEL[c.policySource] : 'Policies'}
          </div>
        </div>
        <Tooltip
          content={
            allowed
              ? null
              : own.paused
                ? 'Resuming a prod cluster needs an admin.'
                : 'Pausing needs the operator role.'
          }
        >
          <span>
            <Button
              size="sm"
              variant={own.paused ? 'default' : 'outline'}
              onClick={onToggle}
              disabled={!allowed}
              data-testid={own.paused ? 'resume-cluster' : 'pause-cluster'}
            >
              {own.paused ? <PlayIcon /> : <PauseIcon />}
              {own.paused ? 'Resume' : 'Pause'}
            </Button>
          </span>
        </Tooltip>
      </div>
      <div className="flex flex-col gap-4 px-5 pt-1 pb-5">
        {own.paused ? <PauseSummary a={own} /> : null}
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-[13px] sm:grid-cols-4">
          <div>
            <dt className="text-xs text-muted-foreground">Agent</dt>
            <dd className="truncate font-mono text-xs">{c.agentVersion ?? '—'}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Last pull</dt>
            <dd>
              {c.configSync?.lastConfigPullAt ? (
                <Tooltip content={formatDateTime(c.configSync.lastConfigPullAt)}>
                  <span>{relativeTime(c.configSync.lastConfigPullAt)}</span>
                </Tooltip>
              ) : (
                <span className="text-muted-foreground">never</span>
              )}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Policy sync</dt>
            <dd>
              {c.configSync ? (
                <SyncBadge sync={c.configSync} />
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Readiness</dt>
            <dd>
              {c.readiness ? (
                <ReadinessBadge summary={c.readiness} compact />
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </dd>
          </div>
        </dl>
        <div>
          <div className="mb-1.5 flex items-center justify-between text-xs">
            <span className="font-medium">Workloads by effective mode</span>
            <span className="text-muted-foreground">{formatNumber(c.counts.workloads)} total</span>
          </div>
          <ModeBar s={s} />
        </div>
      </div>
    </Card>
  )
}

export default function AutomationPage() {
  const auth = useAuth()
  const fleet = useFleetAutomation()
  const clusters = useClusters()
  const summary = useSummary()
  const [dialog, setDialog] = useState<{ target?: string; automation?: Automation; prod?: boolean } | null>(
    null,
  )
  const byName = new Map((summary.data?.clusters ?? []).map((c) => [c.cluster, c]))
  const fa = fleet.data?.fleet
  const items = clusters.data?.items ?? []
  const pausedCount = items.filter((c) => c.automation?.paused).length
  const canPause = auth.can('operator')
  const canResumeFleet = auth.can('admin')

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Automation"
        description="Pause or resume automatic changes per cluster or for the whole fleet. A pause stops new changes on the next agent pull; rollbacks still run."
      />

      {fleet.isError ? (
        <Card>
          <ErrorState error={fleet.error} onRetry={() => fleet.refetch()} compact />
        </Card>
      ) : !fa ? (
        <Skeleton className="h-28 rounded-xl" />
      ) : (
        <Card
          className={cn(
            'relative overflow-hidden',
            fa.paused
              ? 'border-warning/45 bg-[linear-gradient(120deg,color-mix(in_oklch,var(--warning)_9%,var(--card)),var(--card)_60%)]'
              : '',
          )}
          data-testid="fleet-automation"
        >
          <div className="flex flex-col gap-4 p-5 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-start gap-4">
              <div
                className={cn(
                  'flex size-12 shrink-0 items-center justify-center rounded-xl',
                  fa.paused
                    ? 'bg-warning/15 text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]'
                    : 'bg-success/12 text-success',
                )}
              >
                {fa.paused ? <PauseCircleIcon className="size-6" /> : <ShieldCheckIcon className="size-6" />}
              </div>
              <div className="min-w-0">
                <div className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Fleet</div>
                <div className="mt-0.5 text-lg font-semibold tracking-tight">
                  {fa.paused ? 'Automation is paused fleet-wide' : 'Automation is active'}
                </div>
                {fa.paused ? (
                  <div className="mt-1.5">
                    <PauseSummary a={fa} />
                  </div>
                ) : (
                  <p className="mt-0.5 text-[13px] text-muted-foreground">
                    {pausedCount
                      ? `${pausedCount} of ${items.length} clusters are paused individually.`
                      : `All ${items.length} clusters apply auto-patch changes within their guardrails.`}
                  </p>
                )}
              </div>
            </div>
            <Tooltip content={fa.paused && !canResumeFleet ? 'Resuming the fleet needs an admin.' : null}>
              <span className="shrink-0">
                <Button
                  size="lg"
                  variant={fa.paused ? 'default' : 'outline'}
                  className={cn(!fa.paused && 'border-warning/50 hover:bg-warning/10')}
                  disabled={fa.paused ? !canResumeFleet : !canPause}
                  onClick={() => setDialog({ automation: fa })}
                  data-testid={fa.paused ? 'resume-fleet' : 'pause-fleet'}
                >
                  {fa.paused ? <PlayIcon /> : <PauseIcon />}
                  {fa.paused ? 'Resume fleet' : 'Pause fleet'}
                </Button>
              </span>
            </Tooltip>
          </div>
        </Card>
      )}

      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold">Clusters</h2>
          <Link
            to="/clusters"
            className="inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground"
          >
            Cluster details <ArrowRightIcon className="size-3.5" />
          </Link>
        </div>
        {clusters.isError ? (
          <Card>
            <ErrorState error={clusters.error} onRetry={() => clusters.refetch()} />
          </Card>
        ) : !clusters.data ? (
          <div className="grid gap-4 lg:grid-cols-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} className="h-56 rounded-xl" />
            ))}
          </div>
        ) : !items.length ? (
          <Card>
            <EmptyState
              title="No clusters registered"
              description="Register a cluster to manage its automation here."
            />
          </Card>
        ) : (
          <div className="grid gap-4 lg:grid-cols-2">
            {items.map((c) => (
              <ClusterAutomationCard
                key={c.name}
                c={c}
                s={byName.get(c.name)}
                fleetPaused={!!fa?.paused}
                canPause={canPause}
                canResume={c.environment === 'prod' ? auth.can('admin') : canPause}
                onToggle={() =>
                  setDialog({
                    target: c.name,
                    automation: c.automation ?? { paused: false, version: 0 },
                    prod: c.environment === 'prod',
                  })
                }
              />
            ))}
          </div>
        )}
      </section>

      <PauseDialog
        open={!!dialog}
        onOpenChange={(o) => !o && setDialog(null)}
        target={dialog?.target}
        automation={dialog?.automation}
        prod={dialog?.prod}
      />
    </div>
  )
}
