import {
  ArrowLeftIcon,
  ArrowRightIcon,
  PauseIcon,
  PencilIcon,
  PlayIcon,
  RefreshCwIcon,
  ServerIcon,
  Trash2Icon,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import {
  useClusterAdmin,
  usePolicies,
  useReadiness,
  useReadinessHistory,
  useWrite,
  writes,
} from '@/api/admin'
import { useAuth } from '@/api/auth'
import type {
  AdminPolicy,
  ClusterAdmin,
  ClusterPolicySource,
  Environment,
  RightsizingPolicySpec,
} from '@/api/types'
import {
  ActorChip,
  EnvBadge,
  OriginBadge,
  PausedBadge,
  POLICY_SOURCE_LABEL,
  ReadOnlyNotice,
  SyncBadge,
} from '@/components/admin/common'
import { GitOnlyNotice } from '@/components/admin/git-notice'
import { ImpactPreview } from '@/components/admin/impact-preview'
import { AgentTokens, InstallSnippets, Pending } from '@/components/admin/install'
import { PauseDialog } from '@/components/admin/pause-dialog'
import { PolicySpecForm } from '@/components/admin/policy-form'
import {
  checkTitle,
  isPending,
  isProblem,
  withPending,
  ReadinessBadge,
  ReadinessChecklist,
  ReadinessStepper,
  STATE_INFO,
} from '@/components/admin/readiness'
import { SaveBar, usePreview } from '@/components/admin/save-bar'
import { WriteDialog } from '@/components/admin/write-dialog'
import { ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, NativeSelect, Textarea } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatNumber, relativeTime } from '@/lib/format'
import { BUILT_IN, mergeSpecs, prune, selectorSummary } from '@/lib/spec'
import { validateSpec } from '@/lib/validate'
import { cn } from '@/lib/utils'
import { changedPaths } from '@/lib/policy'

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b py-2 text-[13px] last:border-0">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right">{children}</dd>
    </div>
  )
}

function OverviewTab({ c }: { c: ClusterAdmin }) {
  const readiness = useReadiness(c.name)
  const r = readiness.data
  const all = r ? withPending(r.checks, r.blockedBy) : []
  const problems = all.filter(isProblem)
  const pending = all.filter(isPending).length
  const inv = c.policyInventory?.items ?? []
  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <div className="space-y-4">
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Readiness</CardTitle>
              <CardDescription>{STATE_INFO[c.readiness.state].desc}</CardDescription>
            </div>
            <Button asChild size="xs" variant="outline">
              <Link to="?tab=readiness">
                Checklist <ArrowRightIcon />
              </Link>
            </Button>
          </CardHeader>
          <CardContent className="space-y-4">
            <ReadinessStepper state={c.readiness.state} nextState={r?.nextState} blockedBy={r?.blockedBy} />
            {problems.length ? (
              <ul className="divide-y rounded-lg border">
                {problems.slice(0, 4).map((p) => (
                  <li key={p.id} className="flex items-start justify-between gap-3 px-3 py-2 text-[13px]">
                    <div className="min-w-0">
                      <div className="font-medium">{checkTitle(p.id)}</div>
                      <div className="truncate text-xs text-muted-foreground">{p.message}</div>
                    </div>
                    <span
                      className={cn(
                        'shrink-0 text-xs font-medium',
                        p.severity === 'blocking'
                          ? 'text-destructive'
                          : 'text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]',
                      )}
                    >
                      {p.severity}
                    </span>
                  </li>
                ))}
              </ul>
            ) : null}
            {r && pending ? (
              <p className="text-[13px] text-muted-foreground" data-testid="readiness-pending">
                {pending} blocking check{pending === 1 ? '' : 's'} not reported yet
                {r.nextState ? ` (needed for ${STATE_INFO[r.nextState].label})` : ''}.
              </p>
            ) : r && !problems.length ? (
              <p className="text-[13px] text-muted-foreground">Every check passes.</p>
            ) : null}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Policies on the cluster</CardTitle>
              <CardDescription>
                As the agent reported them{' '}
                {c.policyInventory?.reportedAt ? relativeTime(c.policyInventory.reportedAt) : '—'}.
              </CardDescription>
            </div>
          </CardHeader>
          <CardContent className="pt-0">
            {inv.length ? (
              <table className="w-full text-[13px]">
                <thead className="text-xs text-muted-foreground">
                  <tr>
                    <th className="py-1.5 text-left font-medium">Name</th>
                    <th className="py-1.5 text-left font-medium">Source</th>
                    <th className="hidden py-1.5 text-left font-medium sm:table-cell">Selector</th>
                    <th className="py-1.5 text-right font-medium">Priority</th>
                    <th className="py-1.5 text-right font-medium">Matched</th>
                  </tr>
                </thead>
                <tbody>
                  {inv.map((p) => (
                    <tr key={p.name} className="border-t">
                      <td className="max-w-[14rem] truncate py-1.5 font-mono text-xs">
                        {p.hubPolicyId ? (
                          <Link to={`/admin/policies/${p.hubPolicyId}`} className="hover:underline">
                            {p.name}
                          </Link>
                        ) : (
                          p.name
                        )}
                        {!p.valid ? (
                          <span className="ml-1.5 text-destructive" title={p.message}>
                            invalid
                          </span>
                        ) : null}
                      </td>
                      <td className="py-1.5">
                        <OriginBadge origin={p.source} />
                      </td>
                      <td className="hidden max-w-[14rem] truncate py-1.5 text-muted-foreground sm:table-cell">
                        {selectorSummary(p.spec?.selector)}
                      </td>
                      <td className="py-1.5 text-right font-mono text-xs">{p.priority}</td>
                      <td className="py-1.5 text-right tabular">
                        {p.matchedWorkloads != null ? formatNumber(p.matchedWorkloads) : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p className="rounded-lg border border-dashed px-3 py-5 text-center text-[13px] text-muted-foreground">
                No policy inventory reported yet.
              </p>
            )}
          </CardContent>
        </Card>
      </div>
      <div className="space-y-4">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle>Cluster</CardTitle>
          </CardHeader>
          <CardContent className="pt-0">
            <dl>
              <Row label="Environment">{c.environment ? <EnvBadge env={c.environment} /> : '—'}</Row>
              <Row label="Owner">{c.owner || '—'}</Row>
              <Row label="Policy source">{POLICY_SOURCE_LABEL[c.policySource]}</Row>
              <Row label="Provider · region">{[c.provider, c.region].filter(Boolean).join(' · ') || '—'}</Row>
              <Row label="Datasource">
                <Link to="/admin/datasources" className="font-mono text-xs hover:underline">
                  {c.metrics.datasourceId}
                </Link>
              </Row>
              <Row label="Cluster label">
                <code className="font-mono text-xs">
                  {c.metrics.clusterLabel
                    ? `${c.metrics.clusterLabel}="${c.metrics.clusterLabelValue ?? c.name}"`
                    : 'none (whole datasource)'}
                </code>
              </Row>
              {c.metrics.extraMatchers?.length ? (
                <Row label="Extra matchers">{c.metrics.extraMatchers.join(', ')}</Row>
              ) : null}
            </dl>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle>Agent</CardTitle>
          </CardHeader>
          <CardContent className="pt-0">
            <dl>
              <Row label="Version">
                <span className="font-mono text-xs">{c.agent.agentVersion ?? '—'}</span>
              </Row>
              <Row label="Last heartbeat">
                {c.agent.lastHeartbeatAt ? relativeTime(c.agent.lastHeartbeatAt) : 'never'}
              </Row>
              <Row label="Last config pull">
                {c.agent.lastConfigPullAt ? relativeTime(c.agent.lastConfigPullAt) : 'never'}
              </Row>
              <Row label="Config">
                <span className="inline-flex items-center gap-2">
                  <span className="font-mono text-xs">{c.agent.configVersion}</span>
                  <SyncBadge sync={c.agent} />
                </span>
              </Row>
            </dl>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function ReadinessTab({ cluster }: { cluster: string }) {
  const auth = useAuth()
  const q = useReadiness(cluster, { poll: true })
  const history = useReadinessHistory(cluster)
  const recheck = useWrite(() => writes.recheck(cluster))
  const r = q.data
  if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!r) return <Skeleton className="h-96 rounded-xl" />
  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_20rem]">
      <div className="space-y-4">
        <Card>
          <CardContent className="space-y-4 py-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="text-sm font-semibold">{STATE_INFO[r.state].label}</div>
                <div className="text-[13px] text-muted-foreground">
                  {r.stateSince ? `Since ${relativeTime(r.stateSince)} · ` : ''}
                  {r.checkedAt ? `checked ${relativeTime(r.checkedAt)}` : 'not checked yet'}
                  {r.nextState && r.blockedBy?.length
                    ? ` · ${r.blockedBy.length} blocking check(s) before ${STATE_INFO[r.nextState].label}`
                    : ''}
                  {r.summary.unknown ? ` · ${r.summary.unknown} not reported yet` : ''}
                </div>
              </div>
              {r.recheckRequestedAt ? (
                <Pending label="Re-checking… agent checks arrive with its next pull" />
              ) : auth.can('operator') ? (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={recheck.isPending}
                  data-testid="recheck"
                  onClick={() =>
                    recheck.mutate(undefined, {
                      onSuccess: () =>
                        toast.info(
                          'Re-check requested',
                          'Hub checks run now; agent checks on its next pull.',
                        ),
                      onError: (e) => toast.error('Could not request a re-check', e.message),
                    })
                  }
                >
                  <RefreshCwIcon className={cn(recheck.isPending && 'animate-spin')} /> Re-check
                </Button>
              ) : null}
            </div>
            <ReadinessStepper state={r.state} nextState={r.nextState} blockedBy={r.blockedBy} />
          </CardContent>
        </Card>
        <ReadinessChecklist checks={r.checks} capabilities={r.capabilities} blockedBy={r.blockedBy} />
      </div>
      <Card className="h-fit">
        <CardHeader className="pb-2">
          <div>
            <CardTitle>History</CardTitle>
            <CardDescription>When states and checks changed.</CardDescription>
          </div>
        </CardHeader>
        <CardContent className="pt-1">
          {history.isError ? (
            <ErrorState error={history.error} compact />
          ) : !history.data ? (
            <Skeleton className="h-40" />
          ) : !history.data.items.length ? (
            <p className="text-[13px] text-muted-foreground">No changes recorded yet.</p>
          ) : (
            <ol className="relative space-y-3 border-l pl-4" data-testid="readiness-history">
              {history.data.items.map((h, i) => (
                <li key={i} className="relative text-[13px]">
                  <span
                    className={cn(
                      'absolute top-1.5 -left-[21px] size-2.5 rounded-full border-2 border-card',
                      h.kind === 'state'
                        ? 'bg-primary'
                        : h.toStatus === 'pass'
                          ? 'bg-success'
                          : 'bg-destructive',
                    )}
                  />
                  <div className="font-medium">
                    {h.kind === 'state'
                      ? `${h.fromState ? STATE_INFO[h.fromState].label : '—'} → ${h.toState ? STATE_INFO[h.toState].label : '—'}`
                      : `${checkTitle(h.checkId ?? '')}: ${h.fromStatus ?? '—'} → ${h.toStatus ?? '—'}`}
                  </div>
                  {h.message ? <div className="text-xs text-muted-foreground">{h.message}</div> : null}
                  <Tooltip content={formatDateTime(h.time)}>
                    <div className="w-fit text-[11px] text-muted-foreground">{relativeTime(h.time)}</div>
                  </Tooltip>
                </li>
              ))}
            </ol>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function SettingsTab({ c }: { c: ClusterAdmin }) {
  const auth = useAuth()
  const all = usePolicies({ type: ['global-default', 'cluster-default'] })
  const global = all.data?.items.find((p) => p.type === 'global-default' && !p.deletedAt)
  const current: AdminPolicy | undefined = all.data?.items.find(
    (p) => p.type === 'cluster-default' && !p.deletedAt && p.clusters.includes(c.name),
  )
  const base = useMemo(
    () => prune({ ...(current?.spec ?? {}), priority: undefined, selector: undefined }),
    [current],
  )
  const [spec, setSpec] = useState<RightsizingPolicySpec>(base)
  const [confirm, setConfirm] = useState(false)
  useEffect(() => setSpec(base), [base])
  // Git-only clusters get no hub policies (D-022): their defaults are managed in Git.
  const gitOnly = c.policySource === 'git'
  const readOnly = !auth.can('admin') || gitOnly
  const errors = validateSpec(spec, 'cluster-default')
  const changed = changedPaths(base, spec)
  const preview = usePreview(
    [
      {
        op: 'upsert',
        ...(current ? { policyId: current.id } : {}),
        policy: { type: 'cluster-default', clusters: [c.name], spec },
      },
    ],
    confirm,
  )
  const save = useWrite(({ reason, overrideReadiness }: { reason: string; overrideReadiness?: boolean }) => {
    const body = {
      type: 'cluster-default' as const,
      clusters: [c.name],
      spec,
      reason,
      ...(overrideReadiness ? { overrideReadiness } : {}),
    }
    return current
      ? writes.updatePolicy({ id: current.id, version: current.version, body })
      : writes.createPolicy(body)
  })
  if (all.isError) return <ErrorState error={all.error} onRetry={() => all.refetch()} />
  if (!all.data) return <Skeleton className="h-96 rounded-xl" />
  return (
    <div className="space-y-4">
      {gitOnly ? (
        <GitOnlyNotice clusters={[c.name]} what="Cluster defaults set here" />
      ) : readOnly ? (
        <ReadOnlyNotice need="admin" signedIn={auth.signedIn} />
      ) : null}
      <p className="text-[13px] text-muted-foreground">
        Cluster defaults (<code className="font-mono">ro-cluster-defaults</code>) sit between the global
        defaults and policies. Each field shows the global value it differs from.
        {current ? (
          <span className="ml-1 inline-flex items-center gap-1.5">
            v{current.version}, last changed by{' '}
            <ActorChip actor={current.updatedBy} time={current.updatedAt} />
          </span>
        ) : null}
      </p>
      <Card>
        <CardContent className="py-6">
          <PolicySpecForm
            value={spec}
            onChange={setSpec}
            inherited={mergeSpecs(BUILT_IN, global?.spec)}
            inheritedFrom="global defaults"
            errors={errors}
            disabled={readOnly}
          />
        </CardContent>
      </Card>
      {!readOnly ? (
        <SaveBar
          dirty={changed.length > 0}
          count={changed.length}
          errors={Object.keys(errors).length}
          onDiscard={() => setSpec(base)}
          onSave={() => setConfirm(true)}
        />
      ) : null}
      <WriteDialog
        open={confirm}
        onOpenChange={setConfirm}
        wide
        title={`Save cluster defaults for ${c.name}`}
        description="Agents on this cluster apply the new defaults on their next pull."
        prodClusters={c.environment === 'prod' ? [c.name] : []}
        confirmLabel="Save cluster defaults"
        confirmDisabled={!preview.data}
        onConfirm={(o) => save.mutateAsync(o)}
        successMessage={`Cluster defaults saved for ${c.name}`}
      >
        <ImpactPreview preview={preview.data} loading={preview.isFetching} error={preview.error} />
      </WriteDialog>
    </div>
  )
}

function EditClusterDialog({
  c,
  open,
  onOpenChange,
}: {
  c: ClusterAdmin
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const [f, setF] = useState({
    description: c.description,
    environment: c.environment ?? 'non-prod',
    owner: c.owner,
    policySource: c.policySource,
    provider: c.provider ?? '',
    region: c.region ?? '',
  })
  useEffect(() => {
    if (open)
      setF({
        description: c.description,
        environment: c.environment ?? 'non-prod',
        owner: c.owner,
        policySource: c.policySource,
        provider: c.provider ?? '',
        region: c.region ?? '',
      })
  }, [open, c])
  const write = useWrite(({ reason }: { reason: string }) =>
    writes.updateCluster({
      cluster: c.name,
      version: c.version,
      body: { ...f, environment: f.environment as Environment, reason },
    }),
  )
  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Edit ${c.name}`}
      prodClusters={c.environment === 'prod' || f.environment === 'prod' ? [c.name] : []}
      onConfirm={(o) => write.mutateAsync(o)}
      successMessage="Cluster updated"
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Environment" hint="prod asks for confirmation on every write.">
          <NativeSelect
            value={f.environment}
            onChange={(e) => setF({ ...f, environment: e.target.value as Environment })}
          >
            <option value="prod">prod</option>
            <option value="non-prod">non-prod</option>
          </NativeSelect>
        </Field>
        <Field label="Policy source">
          <NativeSelect
            value={f.policySource}
            onChange={(e) => setF({ ...f, policySource: e.target.value as ClusterPolicySource })}
          >
            <option value="both">Hub + Git</option>
            <option value="hub">Hub only</option>
            <option value="git">Git only (admin pages read-only)</option>
          </NativeSelect>
        </Field>
        <Field label="Owner team">
          <Input value={f.owner} onChange={(e) => setF({ ...f, owner: e.target.value })} />
        </Field>
        <Field label="Provider · region">
          <div className="flex gap-2">
            <Input
              value={f.provider}
              onChange={(e) => setF({ ...f, provider: e.target.value })}
              placeholder="aws"
            />
            <Input
              value={f.region}
              onChange={(e) => setF({ ...f, region: e.target.value })}
              placeholder="eu-west-1"
            />
          </div>
        </Field>
        <Field label="Description" className="sm:col-span-2">
          <Textarea
            rows={2}
            value={f.description}
            onChange={(e) => setF({ ...f, description: e.target.value })}
          />
        </Field>
      </div>
    </WriteDialog>
  )
}

export default function ClusterDetailPage() {
  const { cluster = '' } = useParams()
  const auth = useAuth()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const q = useClusterAdmin(cluster)
  const c = q.data
  const [dialog, setDialog] = useState<'pause' | 'edit' | 'delete' | null>(null)
  const tab = params.get('tab') ?? 'overview'
  const del = useWrite(({ reason }: { reason: string }) =>
    writes.deleteCluster({ cluster, version: c!.version, reason }),
  )
  const prod = c?.environment === 'prod'
  const canToggle = c?.automation.paused
    ? prod
      ? auth.can('admin')
      : auth.can('operator')
    : auth.can('operator')

  return (
    <div className="space-y-6">
      <Link
        to="/clusters"
        className="inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-3.5" /> Clusters
      </Link>
      {q.isError ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : !c ? (
        <div className="space-y-4">
          <Skeleton className="h-14 w-96" />
          <Skeleton className="h-96 rounded-xl" />
        </div>
      ) : (
        <>
          <PageHeader
            title={
              <span className="flex flex-wrap items-center gap-2.5">
                <ServerIcon className="size-5 text-muted-foreground" />
                {c.name}
                <EnvBadge env={c.environment} />
                <PausedBadge paused={c.paused} fleet={c.paused && !c.automation.paused} />
              </span>
            }
            description={
              <span className="flex flex-wrap items-center gap-2">
                {c.description ? <span>{c.description}</span> : null}
                <ReadinessBadge summary={c.readiness} />
              </span>
            }
            actions={
              <>
                {auth.can('operator') ? (
                  <Tooltip content={canToggle ? null : 'Resuming a prod cluster needs an admin.'}>
                    <span>
                      <Button
                        size="sm"
                        variant={c.automation.paused ? 'default' : 'outline'}
                        disabled={!canToggle}
                        onClick={() => setDialog('pause')}
                        data-testid={c.automation.paused ? 'resume-cluster' : 'pause-cluster'}
                      >
                        {c.automation.paused ? <PlayIcon /> : <PauseIcon />}
                        {c.automation.paused ? 'Resume automation' : 'Pause automation'}
                      </Button>
                    </span>
                  </Tooltip>
                ) : null}
                {auth.can('admin') ? (
                  <>
                    <Button size="sm" variant="outline" onClick={() => setDialog('edit')}>
                      <PencilIcon /> Edit
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:text-destructive"
                      onClick={() => setDialog('delete')}
                      aria-label="Delete cluster"
                    >
                      <Trash2Icon />
                    </Button>
                  </>
                ) : null}
              </>
            }
          />
          {c.automation.paused ? (
            <div className="rounded-lg border border-warning/35 bg-warning/8 px-4 py-2.5 text-[13px]">
              <span className="font-medium">Automation paused</span>
              {c.automation.reason ? (
                <span className="text-muted-foreground"> · {c.automation.reason}</span>
              ) : null}
              {c.automation.resumeAt ? (
                <span className="text-muted-foreground">
                  {' '}
                  · resumes {formatDateTime(c.automation.resumeAt, true)}
                </span>
              ) : null}
            </div>
          ) : null}
          <Tabs
            value={tab}
            onValueChange={(v) => setParams(v === 'overview' ? {} : { tab: v }, { replace: true })}
          >
            <TabsList className="max-w-full overflow-x-auto">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="readiness" data-testid="tab-readiness">
                Readiness
                {c.readiness.blocking ? (
                  <span className="rounded bg-destructive/12 px-1 text-[10px] text-destructive tabular">
                    {c.readiness.blocking}
                  </span>
                ) : null}
              </TabsTrigger>
              <TabsTrigger value="settings">Cluster defaults</TabsTrigger>
              {auth.can('admin') ? <TabsTrigger value="agent">Agent and install</TabsTrigger> : null}
            </TabsList>
            <TabsContent value="overview" className="mt-4">
              <OverviewTab c={c} />
            </TabsContent>
            <TabsContent value="readiness" className="mt-4">
              <ReadinessTab cluster={c.name} />
            </TabsContent>
            <TabsContent value="settings" className="mt-4">
              <SettingsTab c={c} />
            </TabsContent>
            {auth.can('admin') ? (
              <TabsContent value="agent" className="mt-4">
                <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                  <Card>
                    <CardHeader>
                      <div>
                        <CardTitle>Agent token</CardTitle>
                        <CardDescription>Create, rotate or revoke the token the agent uses.</CardDescription>
                      </div>
                    </CardHeader>
                    <CardContent>
                      <AgentTokens cluster={c.name} prod={prod} />
                    </CardContent>
                  </Card>
                  <Card>
                    <CardHeader>
                      <div>
                        <CardTitle>Install the agent</CardTitle>
                        <CardDescription>
                          The snippets read the token from the Secret resource-optimizer-agent-token.
                        </CardDescription>
                      </div>
                    </CardHeader>
                    <CardContent>
                      <InstallSnippets cluster={c.name} />
                    </CardContent>
                  </Card>
                </div>
              </TabsContent>
            ) : null}
          </Tabs>
          <PauseDialog
            open={dialog === 'pause'}
            onOpenChange={(o) => !o && setDialog(null)}
            target={c.name}
            automation={c.automation}
            prod={prod}
          />
          <EditClusterDialog c={c} open={dialog === 'edit'} onOpenChange={(o) => !o && setDialog(null)} />
          <WriteDialog
            open={dialog === 'delete'}
            onOpenChange={(o) => !o && setDialog(null)}
            tone="destructive"
            title={`Remove ${c.name} from the hub`}
            description="Revokes its agent tokens; its history and audit stay. The agent stops reaching the hub, and the hub-managed policies on the cluster stay as they are until you delete them there."
            prodClusters={[c.name]}
            confirmLabel="Remove cluster"
            onConfirm={async (o) => {
              await del.mutateAsync(o)
              navigate('/clusters')
            }}
            successMessage={`Removed ${c.name}`}
          />
        </>
      )}
    </div>
  )
}
