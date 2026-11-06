import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  BotIcon,
  ChevronRightIcon,
  ClockIcon,
  CopyIcon,
  CpuIcon,
  HourglassIcon,
  MemoryStickIcon,
  RotateCcwIcon,
  ShieldCheckIcon,
  UnlinkIcon,
} from 'lucide-react'
import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { ApiError } from '@/api/client'
import { useWorkload, useWorkloadRealized } from '@/api/queries'
import type { ContainerRecommendation, Mode, ModeSource, WorkloadDetail } from '@/api/types'
import {
  DeltaPill,
  KindBadge,
  MODE_INFO,
  MODE_SOURCE_LABEL,
  PhaseBadge,
  RISK_INFO,
  Savings,
} from '@/components/domain'
import { CopyAsMenu } from '@/components/copy-as'
import { EmptyState, ErrorState } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import {
  formatBytes,
  formatCores,
  formatDateTime,
  formatPercent,
  formatUSD,
  parseQuantity,
  relativeTime,
} from '@/lib/format'
import { realizedLabel } from '@/lib/realized'
import { cn } from '@/lib/utils'
import { workloadId } from '@/lib/workload-id'
import type { WorkloadKind } from '@/api/types'
import { ReadinessReason } from '@/components/admin/readiness-reason'
import { TeamChip } from '@/components/team'
import { AutomationActions } from './automation-actions'
import { GpuPanel } from './gpu-panel'
import { HistoryCard } from './history'
import { UsageChartCard } from './usage-chart'
import { ConfidenceCard, WhyPanel } from './why-panel'

export default function WorkloadPage() {
  const p = useParams()
  const id = workloadId({
    cluster: p.cluster!,
    namespace: p.namespace!,
    kind: p.kind as WorkloadKind,
    name: p.name!,
  })
  const q = useWorkload(id)
  const w = q.data

  if (q.isError) {
    const nf = q.error instanceof ApiError && (q.error.status === 404 || q.error.status === 400)
    return (
      <div className="space-y-6">
        <BackLink />
        <Card>
          {nf ? (
            <EmptyState
              title="Workload not found"
              description={
                <>
                  The hub does not know <span className="font-mono text-xs">{id}</span>. It may have been
                  deleted, or the agent has not reported it yet.
                </>
              }
              action={
                <Button asChild size="sm" variant="outline">
                  <Link to="/opportunities">Back to opportunities</Link>
                </Button>
              }
              className="py-20"
            />
          ) : (
            <ErrorState error={q.error} onRetry={() => q.refetch()} />
          )}
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <Header id={id} w={w} />
      {w ? <StatusBanner w={w} /> : null}
      {!w ? (
        <div className="grid gap-4 xl:grid-cols-3">
          <div className="space-y-4 xl:col-span-2">
            <Skeleton className="h-56 rounded-xl" />
            <Skeleton className="h-[420px] rounded-xl" />
          </div>
          <div className="space-y-4">
            <Skeleton className="h-[480px] rounded-xl" />
          </div>
        </div>
      ) : !w.recommendation ? (
        <div className="grid gap-4 xl:grid-cols-3">
          <Card className="xl:col-span-2">
            <EmptyState
              icon={HourglassIcon}
              title="Collecting usage data"
              description={
                <>
                  First seen {relativeTime(w.firstSeenAt)}. The hub needs enough usage samples before it
                  recommends values; the first recommendation usually appears within a day.
                  <ReadinessReason cluster={w.cluster} />
                </>
              }
              className="py-16"
            />
          </Card>
          <AutomationCard w={w} />
        </div>
      ) : (
        <div className="grid gap-4 xl:grid-cols-3">
          <div className="min-w-0 space-y-4 xl:col-span-2">
            <ContainersCard w={w} />
            {w.recommendation.gpu ? <GpuPanel gpu={w.recommendation.gpu} cluster={w.cluster} /> : null}
            <UsageChartCard id={id} containers={w.recommendation.containers} />
            <HistoryCard id={id} />
          </div>
          <div className="min-w-0 space-y-4">
            <AutomationCard w={w} />
            <WhyPanel rec={w.recommendation} />
            <ConfidenceCard rec={w.recommendation} />
            <RiskCard w={w} />
          </div>
        </div>
      )}
    </div>
  )
}

function BackLink() {
  return (
    <Link
      to="/opportunities"
      className="inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground"
    >
      <ArrowLeftIcon className="size-3.5" /> Opportunities
    </Link>
  )
}

function Header({ id, w }: { id: string; w?: WorkloadDetail }) {
  const p = id.split('/')
  const [copied, setCopied] = useState(false)
  const rec = w?.recommendation
  const realized = useWorkloadRealized(id, !!w).data
  return (
    <div className="space-y-3">
      <nav
        aria-label="Breadcrumb"
        className="flex flex-wrap items-center gap-1 text-[13px] text-muted-foreground"
      >
        <Link to="/opportunities" className="hover:text-foreground">
          Opportunities
        </Link>
        <ChevronRightIcon className="size-3.5 opacity-50" />
        <Link to={`/opportunities?cluster=${encodeURIComponent(p[0]!)}`} className="hover:text-foreground">
          {p[0]}
        </Link>
        <ChevronRightIcon className="size-3.5 opacity-50" />
        <Link
          to={`/opportunities?cluster=${encodeURIComponent(p[0]!)}&namespace=${encodeURIComponent(p[1]!)}`}
          className="hover:text-foreground"
        >
          {p[1]}
        </Link>
      </nav>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2.5">
            <h1 className="truncate text-2xl font-semibold tracking-tight">{p[3]}</h1>
            <KindBadge kind={p[2] as WorkloadKind} />
            {w ? <PhaseBadge phase={w.phase} reason={w.status?.reason} /> : <Skeleton className="h-5 w-20" />}
            {w?.team ? <TeamChip team={w.team} /> : null}
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[13px] text-muted-foreground">
            <span>{p[2]}</span>
            <span className="opacity-40">·</span>
            <button
              className="inline-flex items-center gap-1 font-mono text-xs hover:text-foreground"
              onClick={() => {
                void navigator.clipboard?.writeText(id).then(() => {
                  setCopied(true)
                  setTimeout(() => setCopied(false), 1500)
                })
              }}
              aria-label="Copy workload id"
            >
              {id}
              <CopyIcon className="size-3" />
              {copied ? <span className="font-sans text-success">copied</span> : null}
            </button>
            {rec ? (
              <>
                <span className="opacity-40">·</span>
                <span className="inline-flex items-center gap-1">
                  <ClockIcon className="size-3.5" /> Recommended {relativeTime(rec.generatedAt)}
                </span>
              </>
            ) : null}
          </div>
        </div>
        {rec || realized?.since ? (
          <div className="flex flex-wrap items-stretch gap-3">
            {rec ? (
              <div className="self-center">
                <CopyAsMenu
                  workloads={[
                    {
                      kind: p[2] as WorkloadKind,
                      namespace: p[1]!,
                      name: p[3]!,
                      cluster: p[0],
                      containers: rec.containers,
                    },
                  ]}
                />
              </div>
            ) : null}
            {realized?.since ? (
              <div
                className="rounded-xl border border-success/30 bg-success/5 px-4 py-2.5 text-right shadow-xs"
                data-testid="workload-realized"
              >
                <div className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                  {realizedLabel(realized.since)}
                </div>
                <div className="mt-0.5 text-2xl font-semibold text-success tabular">
                  {formatUSD(realized.total.total, { cents: Math.abs(realized.total.total) < 100 })}
                </div>
                <div className="text-[11px] text-muted-foreground tabular">
                  {formatUSD(realized.inRange.total, { cents: Math.abs(realized.inRange.total) < 100 })} in
                  the last 30 days
                  {releasedNow(realized.releasedNow) ? (
                    <> · {releasedNow(realized.releasedNow)} released now</>
                  ) : null}
                </div>
              </div>
            ) : null}
            {rec ? (
              <div
                className="rounded-xl border bg-card px-4 py-2.5 text-right shadow-xs"
                data-testid="workload-estimated"
              >
                <div className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                  Estimated (if applied)
                </div>
                <div className="mt-0.5 text-2xl font-semibold tabular">
                  <Savings value={rec.monthlySavingsUSD.total} className="font-semibold" />
                  <span className="ml-1 text-xs font-normal text-muted-foreground">/ month</span>
                </div>
                <div className="text-[11px] text-muted-foreground tabular">
                  CPU {formatUSD(rec.monthlySavingsUSD.cpu)} · Mem {formatUSD(rec.monthlySavingsUSD.memory)}
                </div>
              </div>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  )
}

/** "270m CPU, 128 MiB" for what is released now; "" when nothing is. */
function releasedNow(r: { cpuCores: number; memoryBytes: number }) {
  const parts: string[] = []
  if (r.cpuCores) parts.push(`${formatCores(r.cpuCores)} CPU`)
  if (r.memoryBytes) parts.push(formatBytes(r.memoryBytes, 0))
  return parts.join(', ')
}

function StatusBanner({ w }: { w: WorkloadDetail }) {
  const s = w.status
  if (!s || !['Blocked', 'RolledBack', 'Released', 'Applying'].includes(w.phase)) return null
  const map = {
    Blocked: {
      icon: AlertTriangleIcon,
      cls: 'border-warning/40 bg-warning/8',
      iconCls: 'text-warning',
      title: 'Blocked by a guardrail',
    },
    RolledBack: {
      icon: RotateCcwIcon,
      cls: 'border-destructive/30 bg-destructive/6',
      iconCls: 'text-destructive',
      title: 'Rolled back by the health watch',
    },
    Released: {
      icon: UnlinkIcon,
      cls: 'border-chart-6/30 bg-chart-6/6',
      iconCls: 'text-chart-6',
      title: 'Released to Git',
    },
    Applying: {
      icon: BotIcon,
      cls: 'border-info/30 bg-info/6',
      iconCls: 'text-info',
      title: 'Applying the recommendation',
    },
  } as const
  const m = map[w.phase as keyof typeof map]
  return (
    <div role="status" className={cn('flex gap-3 rounded-xl border px-4 py-3', m.cls)}>
      <m.icon className={cn('mt-0.5 size-4 shrink-0', m.iconCls)} />
      <div className="text-[13px]">
        <div className="font-medium">
          {m.title}
          {s.reason ? (
            <span className="ml-1.5 font-mono text-xs font-normal text-muted-foreground">{s.reason}</span>
          ) : null}
        </div>
        {s.message ? <p className="mt-0.5 text-muted-foreground">{s.message}</p> : null}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- containers

function ResourceRow({
  label,
  icon: Icon,
  resource,
  kind,
  c,
}: {
  label: string
  icon: typeof CpuIcon
  resource: 'cpu' | 'memory'
  kind: 'requests' | 'limits'
  c: ContainerRecommendation
}) {
  const cur = parseQuantity(c.current[kind]?.[resource])
  const rec = parseQuantity(c.recommended[kind]?.[resource])
  const removed = kind === 'limits' && c.removeLimits?.includes(resource)
  const fmt =
    resource === 'cpu'
      ? (v?: number) => (v == null ? 'none' : formatCores(v))
      : (v?: number) => (v == null ? 'none' : formatBytes(v))
  return (
    <tr className="border-b border-border/60 last:border-0">
      <td className="py-2 pr-3 text-[13px] whitespace-nowrap text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <Icon className="size-3.5" />
          {label}
        </span>
      </td>
      <td className="px-3 py-2 text-right font-mono text-xs text-muted-foreground tabular">{fmt(cur)}</td>
      <td className="px-3 py-2 text-right font-mono text-xs font-medium tabular">
        {removed ? <span className="text-warning">remove</span> : fmt(rec)}
      </td>
      <td className="py-2 pl-3 text-right">
        {removed || cur == null || rec == null ? null : <DeltaPill from={cur} to={rec} />}
      </td>
    </tr>
  )
}

function ContainersCard({ w }: { w: WorkloadDetail }) {
  const rec = w.recommendation!
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Recommended resources</CardTitle>
          <CardDescription>
            Per container, per pod ·{' '}
            {rec.averageReplicas != null
              ? `${rec.averageReplicas.toFixed(rec.averageReplicas % 1 ? 2 : 0)} replicas on average`
              : ''}
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent
        className={cn('grid gap-4 pt-3', rec.containers.length > 1 && 'lg:grid-cols-2')}
        data-testid="containers"
      >
        {rec.containers.map((c) => (
          <div key={c.name} className="rounded-lg border p-3.5">
            <div className="mb-2 flex items-center justify-between gap-2">
              <div className="flex min-w-0 items-center gap-1.5">
                <span className="truncate font-mono text-[13px] font-medium">{c.name}</span>
                {c.init ? <Badge variant="muted">init</Badge> : null}
                {c.riskFlags?.includes('sidecar') ? <Badge variant="muted">sidecar</Badge> : null}
              </div>
              <Savings value={c.monthlySavingsUSD?.total} className="text-[13px]" />
            </div>
            <table className="w-full">
              <thead>
                <tr className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
                  <th className="pb-1 text-left font-medium" />
                  <th className="px-3 pb-1 text-right font-medium">Current</th>
                  <th className="px-3 pb-1 text-right font-medium">Recommended</th>
                  <th className="pb-1 pl-3 text-right font-medium">Δ</th>
                </tr>
              </thead>
              <tbody>
                <ResourceRow label="CPU request" icon={CpuIcon} resource="cpu" kind="requests" c={c} />
                <ResourceRow label="CPU limit" icon={CpuIcon} resource="cpu" kind="limits" c={c} />
                <ResourceRow
                  label="Mem request"
                  icon={MemoryStickIcon}
                  resource="memory"
                  kind="requests"
                  c={c}
                />
                <ResourceRow label="Mem limit" icon={MemoryStickIcon} resource="memory" kind="limits" c={c} />
              </tbody>
            </table>
          </div>
        ))}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- risk

function RiskCard({ w }: { w: WorkloadDetail }) {
  const flags = w.recommendation?.riskFlags ?? []
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Risk flags</CardTitle>
          <CardDescription>What the hub noticed and how it changes the recommendation</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="pt-3">
        {!flags.length ? (
          <div className="flex items-center gap-3 rounded-lg bg-success/8 px-3 py-3 text-[13px]">
            <ShieldCheckIcon className="size-5 text-success" />
            <span>No risk flags. Usage is stable and well within limits.</span>
          </div>
        ) : (
          <ul className="space-y-3">
            {flags.map((f) => {
              const info = RISK_INFO[f]
              const containers = w
                .recommendation!.containers.filter((c) => c.riskFlags?.includes(f))
                .map((c) => c.name)
              return (
                <li key={f} className="flex gap-3">
                  <span
                    className={cn(
                      'flex size-7 shrink-0 items-center justify-center rounded-lg',
                      info.severity === 'high'
                        ? 'bg-destructive/10 text-destructive'
                        : info.severity === 'medium'
                          ? 'bg-warning/15 text-warning'
                          : 'bg-muted text-muted-foreground',
                    )}
                  >
                    <info.icon className="size-3.5" />
                  </span>
                  <div className="min-w-0 text-[13px]">
                    <div className="font-medium">
                      {info.label}
                      {containers.length ? (
                        <span className="ml-1.5 font-mono text-[11px] font-normal text-muted-foreground">
                          {containers.join(', ')}
                        </span>
                      ) : null}
                    </div>
                    <p className="text-muted-foreground">{info.desc}</p>
                    <p className="mt-0.5 text-xs text-foreground/80">→ {info.impact}</p>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- automation

function ModeRow({
  label,
  icon: Icon,
  mode,
  source,
  policy,
}: {
  label: string
  icon: typeof CpuIcon
  mode: Mode
  source?: ModeSource
  policy?: string
}) {
  const info = MODE_INFO[mode]
  return (
    <div className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5">
      <div className="flex items-center gap-2 text-[13px]">
        <Icon className="size-4 text-muted-foreground" />
        <span className="font-medium">{label}</span>
      </div>
      <div className="text-right">
        <span
          className={cn(
            'inline-flex h-5 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium',
            info.cls,
          )}
        >
          <info.icon className="size-3" />
          {mode}
        </span>
        <div className="mt-0.5 text-[11px] text-muted-foreground">
          {source ? (
            <>
              via {MODE_SOURCE_LABEL[source]}
              {source === 'policy' && policy ? (
                <>
                  {' '}
                  <span className="font-mono text-foreground/80">{policy}</span>
                </>
              ) : null}
            </>
          ) : (
            'source unknown'
          )}
        </div>
      </div>
    </div>
  )
}

function AutomationCard({ w }: { w: WorkloadDetail }) {
  const s = w.status
  const settings = s?.settings ?? w.recommendation?.settings
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Automation</CardTitle>
          <CardDescription>Effective modes, the policy that set them, and actions</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 pt-3">
        <ModeRow
          label="CPU"
          icon={CpuIcon}
          mode={w.modes.cpu}
          source={w.modes.cpuSource}
          policy={s?.policy}
        />
        <ModeRow
          label="Memory"
          icon={MemoryStickIcon}
          mode={w.modes.memory}
          source={w.modes.memorySource}
          policy={s?.policy}
        />
        <dl className="space-y-2 pt-1 text-[13px]">
          <KV
            k="Policy"
            v={
              s?.policy ? (
                <span className="font-mono text-xs">{s.policy}</span>
              ) : (
                <span className="text-muted-foreground">none matched</span>
              )
            }
          />
          {s?.override ? (
            <KV k="Override" v={<span className="font-mono text-xs">{s.override}</span>} />
          ) : null}
          {s?.strategy ? <KV k="Strategy" v={s.strategy} /> : null}
          {settings?.profile ? <KV k="Profile" v={settings.profile} /> : null}
          {settings?.cpuLimits ? (
            <KV
              k="CPU limits"
              v={
                settings.cpuLimits === 'ratio'
                  ? `ratio × ${settings.cpuLimitRatio ?? '?'}`
                  : settings.cpuLimits
              }
            />
          ) : null}
          {settings?.memoryLimitEqualsRequest != null ? (
            <KV k="Memory limit = request" v={settings.memoryLimitEqualsRequest ? 'yes' : 'no'} />
          ) : null}
          {s?.appliedAt ? (
            <KV
              k="Last applied"
              v={
                <Tooltip content={formatDateTime(s.appliedAt)}>
                  <span>{relativeTime(s.appliedAt)}</span>
                </Tooltip>
              }
            />
          ) : null}
          {s?.observedAt ? <KV k="Agent reported" v={relativeTime(s.observedAt)} /> : null}
          {w.recommendation ? <KV k="Confidence" v={formatPercent(w.recommendation.confidence)} /> : null}
        </dl>
        {!s ? (
          <p className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
            The agent has not reported this workload yet; modes shown are the global default.
          </p>
        ) : null}
        <AutomationActions w={w} />
      </CardContent>
    </Card>
  )
}

function KV({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-3">
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="text-right font-medium">{v}</dd>
    </div>
  )
}
