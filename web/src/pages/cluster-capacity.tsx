import { ChevronRightIcon, ExternalLinkIcon, InfoIcon, ServerIcon, TriangleAlertIcon } from 'lucide-react'
import { Fragment, useState } from 'react'
import { Link } from 'react-router'
import { useClusterCapacity } from '@/api/admin'
import type {
  CapacityAmounts,
  CapacityAutoscaler,
  CapacityPoolEstimate,
  ClusterCapacity,
  ConsolidationBlocker,
  NodeAutoscalerKind,
  NodeEstimate,
} from '@/api/types'
import { CopyButton } from '@/components/admin/common'
import { EmptyState, ErrorState } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Callout } from '@/components/ui/form'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatCores, formatGiB, formatNumber, formatUSD, relativeTime } from '@/lib/format'
import { BLOCKER_LABEL, kubectlGet, objectLabel, sortBlockers } from '@/lib/capacity'
import { workloadPath } from '@/lib/workload-id'
import { cn } from '@/lib/utils'

const AUTOSCALER_LABEL: Record<NodeAutoscalerKind, string> = {
  karpenter: 'Karpenter',
  'cluster-autoscaler': 'Cluster Autoscaler',
  none: 'No autoscaler',
}

const PRICE_SOURCE_LABEL: Record<string, string> = {
  metrics: 'Measured prices',
  rates: 'Cluster rates',
  mixed: 'Measured prices and cluster rates',
  none: 'No price available',
}

const usd = (v: number | undefined) => formatUSD(v, { cents: false })

function SavingsValue({ v }: { v: number }) {
  return (
    <span className={cn('tabular', v > 0 && 'text-success', v < 0 && 'text-destructive')}>
      {v > 0 ? '+' : v < 0 ? '−' : ''}
      {usd(Math.abs(v))}
    </span>
  )
}

function Stat({ label, children, hint }: { label: string; children: React.ReactNode; hint?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-lg font-semibold tabular">{children}</dd>
      {hint ? <div className="text-xs text-muted-foreground">{hint}</div> : null}
    </div>
  )
}

function AutoscalerBadge({ a }: { a: CapacityAutoscaler }) {
  const detail = [a.status, a.scaleDown].filter(Boolean).join(' · ')
  return (
    <Badge variant="outline" data-testid="autoscaler-badge">
      <ServerIcon />
      {AUTOSCALER_LABEL[a.kind]}
      {detail ? <span className="font-normal text-muted-foreground">{detail}</span> : null}
    </Badge>
  )
}

// ---------------------------------------------------------------- capacity meters

function Meter({
  label,
  allocatable,
  requested,
  after,
  format,
}: {
  label: string
  allocatable: number
  requested: number
  after: number
  format: (v: number) => string
}) {
  const pct = (v: number) => (allocatable > 0 ? Math.min(100, (v / allocatable) * 100) : 0)
  const share = (v: number) => (allocatable > 0 ? `${Math.round((v / allocatable) * 100)}%` : '—')
  return (
    <div className="space-y-2" data-testid={`meter-${label.toLowerCase()}`}>
      <div className="flex items-baseline justify-between text-[13px]">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground tabular">{format(allocatable)} allocatable</span>
      </div>
      <div
        role="img"
        aria-label={`${label}: ${format(requested)} requested now, ${format(after)} after recommendations, of ${format(allocatable)} allocatable`}
        className="relative h-2.5 overflow-hidden rounded-full bg-muted"
      >
        <div
          className="absolute inset-y-0 left-0 rounded-full bg-primary/35"
          style={{ width: `${pct(requested)}%` }}
        />
        <div
          className="absolute inset-y-0 left-0 rounded-full bg-primary"
          style={{ width: `${pct(after)}%` }}
        />
      </div>
      <dl className="grid grid-cols-2 gap-3 text-[13px]">
        <div>
          <dt className="text-xs text-muted-foreground">Requested now</dt>
          <dd className="tabular">
            {format(requested)} <span className="text-muted-foreground">({share(requested)})</span>
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">After recommendations</dt>
          <dd className="tabular">
            {format(after)} <span className="text-muted-foreground">({share(after)})</span>
          </dd>
        </div>
      </dl>
    </div>
  )
}

function CapacityCard({ data }: { data: ClusterCapacity }) {
  const t = data.totals
  return (
    <Card data-testid="capacity-card">
      <CardHeader>
        <div>
          <CardTitle>Capacity</CardTitle>
          <CardDescription>
            What pods request compared with what the {formatNumber(t.nodes)} nodes can hold. Includes
            daemonset pods.
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="grid gap-6 md:grid-cols-2">
        <Meter
          label="CPU"
          allocatable={t.allocatable.cpuCores}
          requested={t.requested.cpuCores}
          after={t.requestedAfter.cpuCores}
          format={(v) => formatCores(v, { unit: true })}
        />
        <Meter
          label="Memory"
          allocatable={t.allocatable.memoryBytes}
          requested={t.requested.memoryBytes}
          after={t.requestedAfter.memoryBytes}
          format={formatGiB}
        />
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- node estimate

function EstimateCard({ estimate }: { estimate: NodeEstimate | undefined }) {
  return (
    <Card data-testid="estimate-card">
      <CardHeader>
        <div>
          <div className="flex items-center gap-2">
            <CardTitle>Node estimate</CardTitle>
            <Badge variant="warning" data-testid="estimate-badge">
              Estimate
            </Badge>
          </div>
          <CardDescription>
            What the cluster could need if the open recommendations were applied. Kerf never adds, removes or
            drains nodes.
          </CardDescription>
        </div>
        {estimate ? (
          <a
            href={estimate.methodURL}
            target="_blank"
            rel="noreferrer"
            data-testid="estimate-method"
            className="inline-flex items-center gap-1 text-[13px] text-primary hover:underline"
          >
            How this is estimated <ExternalLinkIcon className="size-3.5" />
          </a>
        ) : null}
      </CardHeader>
      <CardContent>
        {estimate ? (
          <>
            <dl className="grid grid-cols-2 gap-x-6 gap-y-4 md:grid-cols-4">
              <Stat label="Nodes">
                <span data-testid="estimate-nodes">
                  {formatNumber(estimate.currentNodes)} → {formatNumber(estimate.estimatedNodes)}
                </span>
              </Stat>
              <Stat label="Cost per month">
                <span className="text-base">
                  {usd(estimate.currentMonthlyUSD)} → {usd(estimate.estimatedMonthlyUSD)}
                </span>
              </Stat>
              <Stat
                label="Savings per month"
                hint={estimate.savingsMonthlyUSD < 0 ? 'More nodes would be needed.' : undefined}
              >
                <span data-testid="estimate-savings">
                  <SavingsValue v={estimate.savingsMonthlyUSD} />
                </span>
              </Stat>
              <Stat label="Recommendations applied">{formatNumber(estimate.recommendations)}</Stat>
            </dl>
            <p className="mt-4 text-xs text-muted-foreground">
              Prices: {PRICE_SOURCE_LABEL[estimate.priceSource ?? 'none']}.
            </p>
          </>
        ) : (
          <p className="text-[13px] text-muted-foreground" data-testid="estimate-missing">
            There is no estimate yet. It needs a capacity report from the agent and at least one open
            recommendation.
          </p>
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- pools

const amounts = (a: CapacityAmounts) =>
  `${formatCores(a.cpuCores, { unit: true })} · ${formatGiB(a.memoryBytes)}`

function PoolRow({ pool }: { pool: CapacityPoolEstimate }) {
  const [open, setOpen] = useState(false)
  const name = pool.name || 'No node pool'
  const panel = `pool-${pool.name || 'none'}-types`
  return (
    <Fragment>
      <tr className="border-t align-top" data-testid={`pool-${pool.name || 'none'}`}>
        <td className="px-3 py-2">
          <button
            type="button"
            aria-expanded={open}
            aria-controls={panel}
            onClick={() => setOpen((o) => !o)}
            className="inline-flex items-center gap-1 rounded font-medium hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          >
            <ChevronRightIcon className={cn('size-4 transition-transform', open && 'rotate-90')} />
            <span className={cn(!pool.name && 'italic text-muted-foreground')}>{name}</span>
          </button>
          {pool.unplaced ? (
            <div className="mt-0.5 pl-5">
              <Tooltip content="These pods fit none of the known instance types of the pool. Each one is counted as its own node.">
                <Badge variant="warning" tabIndex={0} data-testid="unplaced">
                  {pool.unplaced} unplaced
                </Badge>
              </Tooltip>
            </div>
          ) : null}
        </td>
        <td className="px-3 py-2">{AUTOSCALER_LABEL[pool.autoscaler]}</td>
        <td className="px-3 py-2 tabular whitespace-nowrap">
          {pool.nodes} → {pool.estimatedNodes}
        </td>
        <td className="px-3 py-2 tabular">{pool.minNodes ?? '—'}</td>
        <td className="px-3 py-2 text-xs tabular">
          <div>
            <span className="text-muted-foreground">Requested </span>
            {amounts(pool.requested)}
          </div>
          <div>
            <span className="text-muted-foreground">Allocatable </span>
            {amounts(pool.allocatable)}
          </div>
        </td>
        <td className="px-3 py-2 tabular whitespace-nowrap">
          {pool.currentMonthlyUSD == null && pool.estimatedMonthlyUSD == null
            ? '—'
            : `${usd(pool.currentMonthlyUSD)} → ${usd(pool.estimatedMonthlyUSD)}`}
        </td>
        <td className="px-3 py-2">{pool.consolidationPolicy || '—'}</td>
      </tr>
      {open ? (
        <tr id={panel} className="bg-muted/30">
          <td colSpan={7} className="px-3 py-2 pl-8">
            <table className="w-full text-xs">
              <thead className="text-muted-foreground">
                <tr>
                  <th className="py-1 pr-3 text-left font-medium">Instance type</th>
                  <th className="py-1 pr-3 text-left font-medium">Capacity type</th>
                  <th className="py-1 pr-3 text-left font-medium">Nodes</th>
                  <th className="py-1 pr-3 text-left font-medium">Price per hour</th>
                  <th className="py-1 text-left font-medium">Price source</th>
                </tr>
              </thead>
              <tbody>
                {pool.instanceTypes.map((it) => (
                  <tr key={`${it.instanceType}/${it.capacityType ?? ''}`} className="border-t">
                    <td className="py-1 pr-3 font-mono">{it.instanceType}</td>
                    <td className="py-1 pr-3">{it.capacityType || '—'}</td>
                    <td className="py-1 pr-3 tabular">
                      {it.nodes} → {it.estimatedNodes}
                    </td>
                    <td className="py-1 pr-3 tabular">
                      {it.hourlyUSD == null ? '—' : `$${it.hourlyUSD.toFixed(4)}`}
                    </td>
                    <td className="py-1">{it.priceSource ?? '—'}</td>
                  </tr>
                ))}
                {pool.instanceTypes.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="py-1 text-muted-foreground">
                      No instance types reported.
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </td>
        </tr>
      ) : null}
    </Fragment>
  )
}

function PoolsCard({ pools }: { pools: CapacityPoolEstimate[] }) {
  return (
    <Card data-testid="pools-card">
      <CardHeader>
        <div>
          <CardTitle>Node pools</CardTitle>
          <CardDescription>
            Nodes now and in the estimate. Open a pool to see its instance types.
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="pt-4">
        {pools.length ? (
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full text-[13px]">
              <thead className="bg-muted/60 text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-1.5 text-left font-medium">Pool</th>
                  <th className="px-3 py-1.5 text-left font-medium">Autoscaler</th>
                  <th className="px-3 py-1.5 text-left font-medium">Nodes</th>
                  <th className="px-3 py-1.5 text-left font-medium">Minimum</th>
                  <th className="px-3 py-1.5 text-left font-medium">Requests and capacity</th>
                  <th className="px-3 py-1.5 text-left font-medium">Cost per month</th>
                  <th className="px-3 py-1.5 text-left font-medium">Consolidation</th>
                </tr>
              </thead>
              <tbody>
                {pools.map((p) => (
                  <PoolRow key={p.name} pool={p} />
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="text-[13px] text-muted-foreground">No node pools reported.</p>
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- blockers

function BlockerItem({ b }: { b: ConsolidationBlocker }) {
  const label = objectLabel(b.object)
  const kubectl = kubectlGet(b.object)
  return (
    <li className="space-y-1.5 px-5 py-3 text-[13px]" data-testid="blocker" data-type={b.type}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="muted">{BLOCKER_LABEL[b.type] ?? b.type}</Badge>
        {b.workloadId ? (
          <Link
            to={workloadPath(b.workloadId)}
            className="font-medium hover:underline"
            data-testid="blocker-link"
          >
            {label}
          </Link>
        ) : (
          <span className="font-medium" data-testid="blocker-object">
            {label}
          </span>
        )}
        {b.nodePool ? <span className="text-xs text-muted-foreground">Pool {b.nodePool}</span> : null}
        {kubectl ? <CopyButton value={kubectl} label="Copy kubectl" className="ml-auto" /> : null}
      </div>
      <p>{b.message}</p>
      <p className="text-muted-foreground">
        <span className="font-medium text-foreground">How to fix: </span>
        {b.fix}
      </p>
    </li>
  )
}

function BlockersCard({ data }: { data: ClusterCapacity }) {
  return (
    <Card data-testid="blockers-card">
      <CardHeader>
        <div>
          <CardTitle>Consolidation blockers</CardTitle>
          <CardDescription>Things that stop the autoscaler from removing nodes.</CardDescription>
        </div>
        {data.blockers.length ? <Badge variant="muted">{data.blockers.length}</Badge> : null}
      </CardHeader>
      <CardContent className="px-0 pb-0 pt-4">
        {data.blockers.length ? (
          <ul className="divide-y border-t">
            {sortBlockers(data.blockers).map((b, i) => (
              <BlockerItem key={`${b.type}/${objectLabel(b.object)}/${i}`} b={b} />
            ))}
          </ul>
        ) : (
          <p
            className="border-t px-5 py-6 text-center text-[13px] text-muted-foreground"
            data-testid="no-blockers"
          >
            Nothing found that blocks consolidation.
          </p>
        )}
        {data.blockersOmitted ? (
          <p className="border-t px-5 py-3 text-xs text-muted-foreground" data-testid="blockers-omitted">
            {formatNumber(data.blockersOmitted)} more blockers are not listed.
          </p>
        ) : null}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- tab

export function CapacityTab({ cluster }: { cluster: string }) {
  const q = useClusterCapacity(cluster)
  const data = q.data
  if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!data) return <Skeleton className="h-96 rounded-xl" />

  if (!data.reportedAt) {
    return (
      <Card data-testid="capacity-empty">
        <EmptyState
          icon={InfoIcon}
          title="No capacity report yet"
          description="The agent has not sent a capacity report. It needs an agent version with capacity reporting, which is on by default, and read-only access to nodes, pod disruption budgets, Karpenter node pools and node claims, and the Cluster Autoscaler status ConfigMap."
        />
      </Card>
    )
  }

  return (
    <div className="space-y-4" data-testid="capacity-tab">
      <div className="flex flex-wrap items-center gap-2 text-[13px] text-muted-foreground">
        <span data-testid="reported-at">Reported {relativeTime(data.reportedAt)}</span>
        {data.autoscalers.map((a, i) => (
          <AutoscalerBadge key={`${a.kind}/${i}`} a={a} />
        ))}
        {data.autoscalers.length === 0 ? <Badge variant="muted">No autoscaler found</Badge> : null}
      </div>
      {data.truncated ? (
        <Callout tone="warning" icon={TriangleAlertIcon}>
          <span data-testid="truncated">
            The agent report hit a list limit, so some numbers may be incomplete.
          </span>
        </Callout>
      ) : null}
      <CapacityCard data={data} />
      <EstimateCard estimate={data.estimate} />
      <PoolsCard pools={data.pools} />
      <BlockersCard data={data} />
    </div>
  )
}
