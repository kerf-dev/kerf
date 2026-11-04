import { FileCodeIcon, PlusIcon, ScrollTextIcon, SearchIcon, TriangleAlertIcon, XIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useEffectivePolicy, usePolicies, usePolicyInventory } from '@/api/admin'
import { useAuth } from '@/api/auth'
import { useWorkloads } from '@/api/queries'
import type { AdminPolicy, HubPolicyType, RightsizingPolicySpec } from '@/api/types'
import { ActorChip, CodeBlock, OriginBadge, ReadOnlyNotice } from '@/components/admin/common'
import { EffectivePolicyView } from '@/components/admin/effective-policy'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip } from '@/components/ui/tooltip'
import { formatNumber } from '@/lib/format'
import { selectorSummary, specChips } from '@/lib/spec'
import { cn } from '@/lib/utils'
import { workloadPath } from '@/lib/workload-id'
import { policyManifest } from '@/lib/policy'

interface Row {
  key: string
  name: string
  origin: 'hub' | 'git'
  type: HubPolicyType | 'git'
  clusters: string[]
  spec?: RightsizingPolicySpec
  priority: number
  matched?: number
  policy?: AdminPolicy
  invalid?: { cluster: string; message?: string }[]
  workloadId?: string
}

const TYPE_LABEL: Record<HubPolicyType, string> = {
  'global-default': 'Global default',
  'cluster-default': 'Cluster default',
  policy: 'Policy',
  'workload-override': 'Override',
}

type View = 'policies' | 'defaults' | 'overrides' | 'all'

function useRows() {
  const hub = usePolicies()
  const inv = usePolicyInventory()
  const rows = useMemo<Row[]>(() => {
    if (!hub.data) return []
    const matched = new Map<string, number>()
    const invalid = new Map<string, { cluster: string; message?: string }[]>()
    const git = new Map<string, Row>()
    for (const c of inv.data?.items ?? [])
      for (const it of c.items) {
        if (it.source === 'hub' && it.hubPolicyId) {
          matched.set(it.hubPolicyId, (matched.get(it.hubPolicyId) ?? 0) + (it.matchedWorkloads ?? 0))
          if (!it.valid)
            invalid.set(it.hubPolicyId, [
              ...(invalid.get(it.hubPolicyId) ?? []),
              { cluster: c.cluster, message: it.message },
            ])
        } else if (it.source === 'git') {
          const cur = git.get(it.name)
          if (cur) {
            cur.clusters.push(c.cluster)
            cur.matched = (cur.matched ?? 0) + (it.matchedWorkloads ?? 0)
            if (!it.valid) cur.invalid = [...(cur.invalid ?? []), { cluster: c.cluster, message: it.message }]
          } else
            git.set(it.name, {
              key: `git:${it.name}`,
              name: it.name,
              origin: 'git',
              type: 'git',
              clusters: [c.cluster],
              spec: it.spec,
              priority: it.priority,
              matched: it.matchedWorkloads,
              invalid: it.valid ? undefined : [{ cluster: c.cluster, message: it.message }],
            })
        }
      }
    const hubRows: Row[] = hub.data.items
      .filter((p) => !p.deletedAt)
      .map((p) => ({
        key: p.id,
        name: p.name,
        origin: 'hub',
        type: p.type,
        clusters: p.clusters,
        spec: p.spec,
        priority: p.priority,
        matched: matched.get(p.id),
        policy: p,
        invalid: invalid.get(p.id),
        workloadId: p.workloadId,
      }))
    return [...hubRows, ...git.values()].sort(
      (a, b) => b.priority - a.priority || a.name.localeCompare(b.name),
    )
  }, [hub.data, inv.data])
  return { rows, hub, inv }
}

function GitPolicyDialog({ row, onClose }: { row?: Row; onClose: () => void }) {
  return (
    <Dialog open={!!row} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="top-[10%] w-[min(720px,calc(100vw-2rem))] p-5">
        {row ? (
          <>
            <div className="flex items-center gap-2">
              <DialogTitle className="font-mono text-base font-semibold">{row.name}</DialogTitle>
              <OriginBadge origin="git" />
            </div>
            <DialogDescription className="mt-1 mb-3 text-[13px] text-muted-foreground">
              Reported by the agent on {row.clusters.join(', ')}. Managed in Git: change it there. The agent
              never modifies it.
            </DialogDescription>
            <CodeBlock code={policyManifest(row.name, row.spec ?? { priority: row.priority })} />
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function PoliciesTable() {
  const auth = useAuth()
  const navigate = useNavigate()
  const { rows, hub, inv } = useRows()
  const [view, setView] = useState<View>('policies')
  const [q, setQ] = useState('')
  const [git, setGit] = useState<Row>()
  const filtered = rows.filter(
    (r) =>
      (view === 'all' ||
        (view === 'policies' && (r.type === 'policy' || r.type === 'git')) ||
        (view === 'defaults' && (r.type === 'global-default' || r.type === 'cluster-default')) ||
        (view === 'overrides' && r.type === 'workload-override')) &&
      (!q ||
        r.name.toLowerCase().includes(q.toLowerCase()) ||
        selectorSummary(r.spec?.selector).toLowerCase().includes(q.toLowerCase())),
  )
  const count = (v: View) =>
    rows.filter((r) =>
      v === 'policies'
        ? r.type === 'policy' || r.type === 'git'
        : v === 'defaults'
          ? r.type.endsWith('default')
          : r.type === 'workload-override',
    ).length

  if (hub.isError)
    return (
      <Card>
        <ErrorState error={hub.error} onRetry={() => hub.refetch()} />
      </Card>
    )
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Segmented
          label="Show"
          value={view}
          onChange={setView}
          options={[
            {
              value: 'policies',
              label: (
                <>
                  Policies <span className="text-muted-foreground tabular">{count('policies')}</span>
                </>
              ),
            },
            {
              value: 'defaults',
              label: (
                <>
                  Defaults <span className="text-muted-foreground tabular">{count('defaults')}</span>
                </>
              ),
            },
            {
              value: 'overrides',
              label: (
                <>
                  Overrides <span className="text-muted-foreground tabular">{count('overrides')}</span>
                </>
              ),
            },
            { value: 'all', label: 'All' },
          ]}
        />
        <div className="relative w-full sm:w-64">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Filter by name or selector"
            className="pl-8"
            aria-label="Filter policies"
          />
        </div>
      </div>
      <Card className="overflow-hidden">
        {hub.isPending ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : !filtered.length ? (
          <EmptyState
            icon={ScrollTextIcon}
            title={
              q
                ? 'No policy matches the filter'
                : view === 'overrides'
                  ? 'No workload overrides'
                  : 'No policies yet'
            }
            description={
              q
                ? 'Try another name or namespace.'
                : view === 'overrides'
                  ? 'Overrides are created from a workload’s Automation card or from bulk actions in Opportunities.'
                  : 'Policies set modes and guardrails for a group of workloads. Git policies appear here once an agent reports them.'
            }
            action={
              !q && view !== 'overrides' && auth.can('admin') ? (
                <Button size="sm" onClick={() => navigate('/admin/policies/new')}>
                  <PlusIcon /> New policy
                </Button>
              ) : undefined
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]" data-testid="policies-table">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">Name</th>
                  <th className="px-3 py-2 text-left font-medium">Source</th>
                  <th className="hidden px-3 py-2 text-left font-medium md:table-cell">Clusters</th>
                  <th className="px-3 py-2 text-left font-medium">Selector</th>
                  <th className="px-3 py-2 text-right font-medium">Priority</th>
                  <th className="px-3 py-2 text-right font-medium">
                    <Tooltip content="Workloads for which this is the winning policy, as reported by the agents.">
                      <span>Matched</span>
                    </Tooltip>
                  </th>
                  <th className="hidden px-4 py-2 text-left font-medium xl:table-cell">Last change</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => (
                  <tr
                    key={r.key}
                    className="cursor-pointer border-b last:border-0 hover:bg-accent/40"
                    onClick={() => (r.policy ? navigate(`/admin/policies/${r.policy.id}`) : setGit(r))}
                    data-testid={`policy-row-${r.name}`}
                  >
                    <td className="max-w-[18rem] px-4 py-2.5">
                      <div className="flex items-center gap-2">
                        {r.policy ? (
                          <Link
                            to={`/admin/policies/${r.policy.id}`}
                            className="truncate font-medium hover:underline"
                            onClick={(e) => e.stopPropagation()}
                          >
                            {r.type === 'workload-override' && r.workloadId
                              ? r.workloadId.split('/').slice(1).join('/')
                              : r.name}
                          </Link>
                        ) : (
                          <button type="button" className="truncate text-left font-medium hover:underline">
                            {r.name}
                          </button>
                        )}
                        {r.invalid?.length ? (
                          <Tooltip
                            content={r.invalid
                              .map((x) => `${x.cluster}: ${x.message ?? 'invalid'}`)
                              .join('\n')}
                          >
                            <TriangleAlertIcon
                              className="size-3.5 shrink-0 text-destructive"
                              aria-label="Invalid on a cluster"
                            />
                          </Tooltip>
                        ) : null}
                      </div>
                      <div className="mt-0.5 flex flex-wrap gap-1">
                        {r.type !== 'policy' && r.type !== 'git' ? (
                          <Badge variant="muted" className="text-[10px]">
                            {TYPE_LABEL[r.type]}
                          </Badge>
                        ) : null}
                        {specChips(r.spec).length ? (
                          <span className="text-[11px] text-muted-foreground">
                            {specChips(r.spec).slice(0, 3).join(' · ')}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <OriginBadge origin={r.origin} />
                    </td>
                    <td className="hidden max-w-[12rem] truncate px-3 py-2.5 text-muted-foreground md:table-cell">
                      {r.clusters.length ? r.clusters.join(', ') : 'All clusters'}
                    </td>
                    <td
                      className="max-w-[16rem] truncate px-3 py-2.5 text-muted-foreground"
                      title={selectorSummary(r.spec?.selector)}
                    >
                      {r.type.endsWith('default') ? 'Every workload' : selectorSummary(r.spec?.selector)}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono text-xs tabular">{r.priority}</td>
                    <td className="px-3 py-2.5 text-right tabular">
                      {r.matched != null ? (
                        formatNumber(r.matched)
                      ) : inv.isPending ? (
                        '…'
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="hidden px-4 py-2.5 xl:table-cell">
                      {r.policy ? (
                        <ActorChip actor={r.policy.updatedBy} time={r.policy.updatedAt} />
                      ) : (
                        <span className="text-xs text-muted-foreground">in Git</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <GitPolicyDialog row={git} onClose={() => setGit(undefined)} />
    </div>
  )
}

function Simulator() {
  const [params, setParams] = useSearchParams()
  const id = params.get('workload') ?? ''
  const [q, setQ] = useState('')
  const search = useWorkloads({ q: q || undefined, limit: 8, sort: 'name' }, { enabled: q.length >= 2 })
  const ep = useEffectivePolicy(id, !!id)
  const pick = (wid: string) => {
    const next = new URLSearchParams(params)
    next.set('workload', wid)
    next.set('tab', 'simulator')
    setParams(next, { replace: true })
    setQ('')
  }
  return (
    <div className="grid gap-4 lg:grid-cols-[22rem_1fr]">
      <Card className="h-fit p-4">
        <div className="text-sm font-semibold">Pick a workload</div>
        <p className="mt-1 text-[13px] text-muted-foreground">
          See every layer that applies to it, which policy won and which layer set each setting.
        </p>
        <div className="relative mt-3">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search by name or namespace"
            className="pl-8"
            aria-label="Search workloads"
            data-testid="simulator-search"
          />
        </div>
        {q.length >= 2 ? (
          <div className="mt-2 flex flex-col">
            {search.data?.items.map((w) => (
              <button
                key={w.id}
                type="button"
                onClick={() => pick(w.id)}
                className={cn(
                  'rounded-md px-2 py-1.5 text-left text-[13px] hover:bg-accent',
                  w.id === id && 'bg-accent',
                )}
              >
                <div className="font-medium">{w.name}</div>
                <div className="text-xs text-muted-foreground">
                  {w.cluster} · {w.namespace} · {w.kind}
                </div>
              </button>
            ))}
            {search.data && !search.data.items.length ? (
              <div className="px-2 py-2 text-xs text-muted-foreground">No workloads found</div>
            ) : null}
          </div>
        ) : null}
        {id ? (
          <div className="mt-3 flex items-center justify-between gap-2 rounded-md border bg-muted/30 px-2.5 py-1.5 text-xs">
            <Link to={workloadPath(id)} className="truncate font-mono hover:underline">
              {id}
            </Link>
            <button
              type="button"
              aria-label="Clear workload"
              onClick={() => setParams({ tab: 'simulator' }, { replace: true })}
            >
              <XIcon className="size-3.5" />
            </button>
          </div>
        ) : null}
      </Card>
      <Card className="p-4">
        {!id ? (
          <EmptyState
            icon={FileCodeIcon}
            title="No workload selected"
            description="Search on the left to simulate its effective policy."
          />
        ) : ep.isError ? (
          <ErrorState error={ep.error} onRetry={() => ep.refetch()} />
        ) : !ep.data ? (
          <div className="space-y-2">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : (
          <EffectivePolicyView ep={ep.data} />
        )}
      </Card>
    </div>
  )
}

export default function PoliciesPage() {
  const auth = useAuth()
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'simulator' ? 'simulator' : 'list'
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Policies"
        description="Hub policies are materialised on clusters by the agent; Git policies are shown read-only. The highest priority wins; defaults and overrides are layers around it."
        actions={
          auth.can('admin') ? (
            <Button asChild size="sm" data-testid="new-policy">
              <Link to="/admin/policies/new">
                <PlusIcon /> New policy
              </Link>
            </Button>
          ) : undefined
        }
      />
      {!auth.can('admin') ? <ReadOnlyNotice need="admin" signedIn={auth.signedIn} /> : null}
      <Tabs
        value={tab}
        onValueChange={(v) => setParams(v === 'simulator' ? { tab: v } : {}, { replace: true })}
      >
        <TabsList>
          <TabsTrigger value="list">Policies</TabsTrigger>
          <TabsTrigger value="simulator" data-testid="tab-simulator">
            Effective-policy simulator
          </TabsTrigger>
        </TabsList>
        <TabsContent value="list" className="mt-4">
          <PoliciesTable />
        </TabsContent>
        <TabsContent value="simulator" className="mt-4">
          <Simulator />
        </TabsContent>
      </Tabs>
    </div>
  )
}
