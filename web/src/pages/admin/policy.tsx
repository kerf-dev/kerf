import { ArrowLeftIcon, DownloadIcon, RotateCcwIcon, Trash2Icon, UsersIcon } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { usePolicies, usePolicy, usePolicyVersions, useWorkloadIndex, useWrite, writes } from '@/api/admin'
import { useAuth } from '@/api/auth'
import { useClusters } from '@/api/queries'
import type {
  AdminPolicy,
  HubPolicyType,
  PolicyChange,
  PolicySelector,
  PolicyVersion,
  RightsizingPolicySpec,
} from '@/api/types'
import { ActorChip, CodeBlock, OriginBadge, ReadOnlyNotice } from '@/components/admin/common'
import { ImpactPreview } from '@/components/admin/impact-preview'
import { PolicySpecForm } from '@/components/admin/policy-form'
import { SaveBar, usePreview } from '@/components/admin/save-bar'
import { matchesSelector, SelectorBuilder } from '@/components/admin/selector-builder'
import { WriteDialog } from '@/components/admin/write-dialog'
import { KindBadge } from '@/components/domain'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, Textarea } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { diffLines, diffStats } from '@/lib/diff'
import { formatDateTime, formatNumber, formatUSD } from '@/lib/format'
import { BUILT_IN, mergeSpecs, prune } from '@/lib/spec'
import { PRIORITY, validateName, validatePriority, validateSpec } from '@/lib/validate'
import { cn } from '@/lib/utils'
import { workloadPath } from '@/lib/workload-id'
import { toYaml } from '@/lib/yaml'
import { changedPaths, policyManifest } from '@/lib/policy'

interface Draft {
  name: string
  description: string
  clusters: string[]
  priority?: number
  selector: PolicySelector
  spec: RightsizingPolicySpec
}

const TYPE_TITLE: Record<HubPolicyType, string> = {
  policy: 'Policy',
  'global-default': 'Global defaults',
  'cluster-default': 'Cluster defaults',
  'workload-override': 'Workload override',
}

function toDraft(p?: AdminPolicy): Draft {
  const { selector, priority, ...rest } = p?.spec ?? {}
  return {
    name: p?.name ?? '',
    description: p?.description ?? '',
    clusters: p?.clusters ?? [],
    priority: p ? (priority ?? p.priority) : 100,
    selector: selector ?? {},
    spec: prune(rest),
  }
}

function draftSpec(d: Draft, type: HubPolicyType): RightsizingPolicySpec {
  if (type !== 'policy') return d.spec
  return prune({ ...d.spec, priority: d.priority, selector: d.selector })
}

/** Live list of the workloads the selector matches (client-side over the workload list). */
function MatchPanel({ selector, clusters }: { selector: PolicySelector; clusters: string[] }) {
  const idx = useWorkloadIndex()
  const matched = useMemo(
    () => (idx.data?.rows ?? []).filter((w) => matchesSelector(w, selector, clusters)),
    [idx.data, selector, clusters],
  )
  const savings = matched.reduce((s, w) => s + (w.monthlySavingsUSD?.total ?? 0), 0)
  const hasLabels = Object.keys(selector.matchLabels ?? {}).length > 0
  return (
    <Card className="xl:sticky xl:top-20" data-testid="match-panel">
      <CardHeader className="pb-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            <UsersIcon className="size-4 text-muted-foreground" />
            {idx.data ? (
              <span data-testid="match-count">
                Matches {hasLabels ? 'up to ' : ''}
                {formatNumber(matched.length)} workload{matched.length === 1 ? '' : 's'}
              </span>
            ) : (
              'Matching workloads…'
            )}
          </CardTitle>
          <CardDescription>
            {formatUSD(savings)}/mo of savings in scope. Higher-priority policies, overrides and annotations
            may still win for some of them.
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {hasLabels ? (
          <p className="mb-2 rounded-md bg-muted/50 px-2.5 py-1.5 text-xs text-muted-foreground">
            Label selectors are evaluated by the agent; this list ignores them.
          </p>
        ) : null}
        {idx.isError ? (
          <ErrorState error={idx.error} compact />
        ) : !idx.data ? (
          <div className="space-y-1.5">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : matched.length ? (
          <ul className="max-h-[26rem] divide-y overflow-auto rounded-lg border">
            {matched.slice(0, 100).map((w) => (
              <li key={w.id} className="flex items-center justify-between gap-2 px-2.5 py-1.5 text-[13px]">
                <Link to={workloadPath(w.id)} className="min-w-0" target="_blank">
                  <div className="truncate font-medium hover:underline">{w.name}</div>
                  <div className="truncate text-xs text-muted-foreground">
                    {w.cluster} · {w.namespace}
                  </div>
                </Link>
                <div className="flex shrink-0 items-center gap-1.5">
                  <KindBadge kind={w.kind} />
                </div>
              </li>
            ))}
            {matched.length > 100 ? (
              <li className="px-2.5 py-1.5 text-xs text-muted-foreground">
                and {formatNumber(matched.length - 100)} more
              </li>
            ) : null}
          </ul>
        ) : (
          <p className="rounded-lg border border-dashed px-3 py-6 text-center text-[13px] text-muted-foreground">
            No workload matches yet.
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function ClusterPicker({
  value,
  onChange,
  disabled,
}: {
  value: string[]
  onChange: (v: string[]) => void
  disabled?: boolean
}) {
  const clusters = useClusters()
  const names = (clusters.data?.items ?? []).map((c) => c.name)
  const all = !value.length
  return (
    <div className="flex flex-wrap gap-1.5" role="group" aria-label="Clusters">
      <button
        type="button"
        disabled={disabled}
        aria-pressed={all}
        onClick={() => onChange([])}
        className={cn(
          'h-7 rounded-md border px-2.5 text-[13px] disabled:opacity-60',
          all
            ? 'border-primary/40 bg-primary/10 text-primary'
            : 'bg-card text-muted-foreground hover:text-foreground',
        )}
      >
        All clusters
      </button>
      {names.map((n) => {
        const on = value.includes(n)
        const env = clusters.data?.items.find((c) => c.name === n)?.environment
        return (
          <button
            key={n}
            type="button"
            disabled={disabled}
            aria-pressed={on}
            onClick={() => onChange(on ? value.filter((x) => x !== n) : [...value, n])}
            className={cn(
              'inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 font-mono text-xs disabled:opacity-60',
              on
                ? 'border-primary/40 bg-primary/10 text-primary'
                : 'bg-card text-muted-foreground hover:text-foreground',
            )}
          >
            {n}
            {env === 'prod' ? (
              <span className="rounded bg-destructive/12 px-1 text-[9px] font-semibold text-destructive uppercase">
                prod
              </span>
            ) : null}
          </button>
        )
      })}
    </div>
  )
}

function Editor({ policy, readOnly }: { policy?: AdminPolicy; readOnly: boolean }) {
  const navigate = useNavigate()
  const clusters = useClusters()
  const globals = usePolicies({ type: ['global-default'] })
  const type: HubPolicyType = policy?.type ?? 'policy'
  const base = useMemo(() => toDraft(policy), [policy])
  const [d, setD] = useState<Draft>(base)
  const [confirm, setConfirm] = useState<'save' | 'delete' | null>(null)
  const [touched, setTouched] = useState(false)
  // Set after a create so the unsaved-changes guard lets the redirect to the new policy through.
  const [created, setCreated] = useState(false)
  useEffect(() => setD(base), [base])

  const global = globals.data?.items.find((p) => p.type === 'global-default' && !p.deletedAt)
  const inherited = mergeSpecs(BUILT_IN, type === 'global-default' ? undefined : global?.spec)
  const spec = draftSpec(d, type)
  const errors = {
    ...validateSpec(spec, type),
    ...(validateName(d.name, type) ? { name: validateName(d.name, type)! } : {}),
    ...(validatePriority(d.priority, type) ? { priority: validatePriority(d.priority, type)! } : {}),
  }
  const dirty =
    (!policy && !created) ||
    changedPaths(draftSpec(base, type), spec).length > 0 ||
    d.name !== base.name ||
    d.description !== base.description ||
    JSON.stringify(d.clusters) !== JSON.stringify(base.clusters)
  const changes: PolicyChange[] = [
    {
      op: 'upsert',
      ...(policy ? { policyId: policy.id } : {}),
      policy: {
        type,
        name: d.name || undefined,
        description: d.description || undefined,
        clusters: d.clusters,
        spec,
        ...(policy?.workloadId ? { workloadId: policy.workloadId } : {}),
      },
    },
  ]
  const preview = usePreview(changes, confirm === 'save')
  const prodEnv = new Set(
    (clusters.data?.items ?? []).filter((c) => c.environment === 'prod').map((c) => c.name),
  )
  const targets = d.clusters.length ? d.clusters : [...prodEnv]
  const prodTouched =
    confirm === 'delete'
      ? targets.filter((c) => prodEnv.has(c))
      : [...new Set((preview.data?.items ?? []).map((i) => i.cluster))].filter((c) => prodEnv.has(c))

  const save = useWrite(({ reason, overrideReadiness }: { reason: string; overrideReadiness?: boolean }) => {
    const body = {
      type,
      ...(type === 'policy' ? { name: d.name } : {}),
      ...(d.description ? { description: d.description } : {}),
      clusters: d.clusters,
      spec,
      reason,
      ...(overrideReadiness ? { overrideReadiness } : {}),
    }
    return policy
      ? writes.updatePolicy({ id: policy.id, version: policy.version, body })
      : writes.createPolicy(body)
  })
  const del = useWrite(({ reason }: { reason: string }) =>
    writes.deletePolicy({ id: policy!.id, version: policy!.version, reason }),
  )

  return (
    <>
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="space-y-6">
          {type === 'policy' ? (
            <Card>
              <CardContent className="grid gap-4 py-5 md:grid-cols-[1fr_10rem]">
                <Field
                  label="Name"
                  htmlFor="pol-name"
                  hint="The RightsizingPolicy name on the clusters."
                  error={touched || d.name ? errors.name : undefined}
                >
                  <Input
                    id="pol-name"
                    value={d.name}
                    onChange={(e) => setD({ ...d, name: e.target.value })}
                    disabled={readOnly || !!policy}
                    className="font-mono"
                    placeholder="payments-cpu-auto"
                    aria-invalid={!!(touched && errors.name)}
                    data-testid="policy-name"
                  />
                </Field>
                <Field
                  label="Priority"
                  htmlFor="pol-prio"
                  hint={`${PRIORITY.policyMin}–${PRIORITY.policyMax.toLocaleString('en-US')}; highest wins.`}
                  error={errors.priority}
                >
                  <Input
                    id="pol-prio"
                    inputMode="numeric"
                    value={d.priority ?? ''}
                    onChange={(e) =>
                      setD({ ...d, priority: e.target.value === '' ? undefined : Number(e.target.value) })
                    }
                    disabled={readOnly}
                    className="font-mono"
                    aria-invalid={!!errors.priority}
                  />
                </Field>
                <Field label="Description" htmlFor="pol-desc" optional className="md:col-span-2">
                  <Textarea
                    id="pol-desc"
                    rows={2}
                    value={d.description}
                    onChange={(e) => setD({ ...d, description: e.target.value })}
                    disabled={readOnly}
                    placeholder="What the policy is for and who owns it"
                  />
                </Field>
                <Field
                  label="Send to"
                  className="md:col-span-2"
                  hint="Clusters that get this policy as a RightsizingPolicy."
                >
                  <ClusterPicker
                    value={d.clusters}
                    onChange={(v) => setD({ ...d, clusters: v })}
                    disabled={readOnly}
                  />
                </Field>
              </CardContent>
            </Card>
          ) : null}
          {type === 'policy' ? (
            <Card>
              <CardHeader>
                <div>
                  <CardTitle>Selector</CardTitle>
                  <CardDescription>
                    Which workloads the policy applies to. All set criteria must match.
                  </CardDescription>
                </div>
              </CardHeader>
              <CardContent>
                <SelectorBuilder
                  value={d.selector}
                  onChange={(s) => setD({ ...d, selector: s })}
                  errors={errors}
                  disabled={readOnly}
                  clusters={d.clusters}
                />
              </CardContent>
            </Card>
          ) : null}
          <Card>
            <CardHeader>
              <div>
                <CardTitle>Settings</CardTitle>
                <CardDescription>
                  Unset fields inherit. For policies, the cluster defaults of each cluster sit between the
                  global defaults shown here and this policy.
                </CardDescription>
              </div>
            </CardHeader>
            <CardContent>
              <PolicySpecForm
                value={d.spec}
                onChange={(s) => setD({ ...d, spec: s })}
                inherited={inherited}
                inheritedFrom={type === 'global-default' ? 'built-in' : 'global defaults'}
                errors={errors}
                disabled={readOnly}
                sections={type === 'workload-override' ? ['modes', 'limits', 'guardrails'] : undefined}
              />
            </CardContent>
          </Card>
          {policy && !readOnly ? (
            <div className="flex justify-end">
              <Button
                variant="outline"
                size="sm"
                className="text-destructive hover:text-destructive"
                onClick={() => setConfirm('delete')}
                data-testid="delete-policy"
              >
                <Trash2Icon /> Delete policy
              </Button>
            </div>
          ) : null}
        </div>
        {type === 'policy' ? <MatchPanel selector={d.selector} clusters={d.clusters} /> : null}
      </div>
      {!readOnly ? (
        <SaveBar
          dirty={dirty}
          count={policy ? changedPaths(draftSpec(base, type), spec).length || undefined : undefined}
          errors={touched ? Object.keys(errors).length : 0}
          onDiscard={() => (policy ? setD(base) : navigate('/admin/policies'))}
          onSave={() => {
            setTouched(true)
            if (!Object.keys(errors).length) setConfirm('save')
          }}
          saveLabel={policy ? 'Review and save' : 'Review and create'}
        />
      ) : null}
      <WriteDialog
        open={confirm === 'save'}
        onOpenChange={(o) => !o && setConfirm(null)}
        wide
        testId="policy-save-dialog"
        title={policy ? `Save ${policy.name}` : `Create ${d.name || 'policy'}`}
        description="The impact once agents pull the change (within about 30 s). Guardrails still apply to every change."
        prodClusters={prodTouched}
        confirmLabel={policy ? 'Save policy' : 'Create policy'}
        confirmDisabled={!preview.data}
        onConfirm={(o) => save.mutateAsync(o)}
        successMessage={(r) => {
          const p = r as AdminPolicy
          if (!policy) {
            setCreated(true)
            setTimeout(
              () => navigate(`/admin/policies/${p.id}`, { replace: true, state: { afterSave: true } }),
              0,
            )
          }
          return policy ? `Saved ${p.name} (v${p.version})` : `Created ${p.name}`
        }}
      >
        <ImpactPreview preview={preview.data} loading={preview.isFetching} error={preview.error} />
      </WriteDialog>
      <WriteDialog
        open={confirm === 'delete'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Delete ${policy?.name}`}
        description="Agents remove the RightsizingPolicy on their next pull; workloads fall back to the next layer. The versions stay, so you can revert."
        tone="destructive"
        confirmLabel="Delete policy"
        prodClusters={prodTouched}
        onConfirm={(o) => del.mutateAsync(o)}
        successMessage={`Deleted ${policy?.name}`}
      />
    </>
  )
}

function versionYaml(v: PolicyVersion) {
  return toYaml({
    name: v.name,
    ...(v.description ? { description: v.description } : {}),
    clusters: v.clusters,
    deleted: v.deleted || undefined,
    spec: v.spec,
  })
}

function History({ policy, canRevert }: { policy: AdminPolicy; canRevert: boolean }) {
  const versions = usePolicyVersions(policy.id)
  const items = versions.data?.items ?? []
  const [sel, setSel] = useState<number>()
  const [revert, setRevert] = useState<PolicyVersion>()
  const selected = items.find((v) => v.version === sel) ?? items[0]
  const prev = selected ? items.find((v) => v.version < selected.version) : undefined
  const diff = selected ? diffLines(prev ? versionYaml(prev) : '', versionYaml(selected)) : []
  const stats = diffStats(diff)
  const write = useWrite(({ reason }: { reason: string }) =>
    writes.revertPolicy({ id: policy.id, version: policy.version, toVersion: revert!.version, reason }),
  )

  if (versions.isError) return <ErrorState error={versions.error} onRetry={() => versions.refetch()} />
  if (!versions.data) return <Skeleton className="h-80 rounded-xl" />
  return (
    <div className="grid gap-4 lg:grid-cols-[20rem_1fr]">
      <Card className="h-fit overflow-hidden">
        <ol data-testid="version-list">
          {items.map((v) => (
            <li key={v.version}>
              <button
                type="button"
                onClick={() => setSel(v.version)}
                className={cn(
                  'flex w-full flex-col gap-1 border-b px-4 py-2.5 text-left last:border-0 hover:bg-accent/40',
                  selected?.version === v.version && 'bg-accent/60',
                )}
                data-testid={`version-${v.version}`}
              >
                <span className="flex items-center gap-2">
                  <span className="font-mono text-xs font-semibold">v{v.version}</span>
                  <Badge
                    variant={
                      v.operation === 'delete'
                        ? 'destructive'
                        : v.operation === 'revert'
                          ? 'warning'
                          : v.operation === 'create'
                            ? 'success'
                            : 'muted'
                    }
                    className="text-[10px] capitalize"
                  >
                    {v.operation}
                    {v.revertedFrom ? ` to v${v.revertedFrom}` : ''}
                  </Badge>
                  {v.version === policy.version ? (
                    <Badge variant="outline" className="text-[10px]">
                      current
                    </Badge>
                  ) : null}
                </span>
                <ActorChip actor={v.actor} time={v.createdAt} />
                {v.reason ? (
                  <span className="line-clamp-2 text-xs text-muted-foreground">“{v.reason}”</span>
                ) : null}
              </button>
            </li>
          ))}
        </ol>
      </Card>
      {selected ? (
        <Card className="overflow-hidden">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
            <div>
              <div className="text-sm font-semibold">
                v{selected.version} {prev ? `compared with v${prev.version}` : '(first version)'}
              </div>
              <div className="text-xs text-muted-foreground">
                {formatDateTime(selected.createdAt)} · <span className="text-success">+{stats.added}</span>{' '}
                <span className="text-destructive">−{stats.removed}</span>
              </div>
            </div>
            {canRevert && selected.version !== policy.version ? (
              <Button size="sm" variant="outline" onClick={() => setRevert(selected)} data-testid="revert">
                <RotateCcwIcon /> Revert to v{selected.version}
              </Button>
            ) : null}
          </div>
          <pre
            className="max-h-[32rem] overflow-auto py-2 font-mono text-[12px] leading-relaxed"
            data-testid="version-diff"
          >
            {diff.map((l, i) => (
              <div
                key={i}
                className={cn(
                  'px-4 whitespace-pre',
                  l.op === 'add' &&
                    'bg-success/10 text-[color-mix(in_oklch,var(--success)_70%,var(--foreground))]',
                  l.op === 'del' &&
                    'bg-destructive/8 text-destructive line-through decoration-destructive/40',
                )}
              >
                <span className="mr-3 inline-block w-3 select-none text-muted-foreground">
                  {l.op === 'add' ? '+' : l.op === 'del' ? '−' : ' '}
                </span>
                {l.text || ' '}
              </div>
            ))}
          </pre>
        </Card>
      ) : (
        <EmptyState title="No versions" />
      )}
      <WriteDialog
        open={!!revert}
        onOpenChange={(o) => !o && setRevert(undefined)}
        testId="revert-dialog"
        title={`Revert ${policy.name} to v${revert?.version}`}
        description={`Creates v${policy.version + 1} with the content of v${revert?.version}${policy.deletedAt ? ' and restores the deleted policy' : ''}. Agents follow on their next pull.`}
        confirmLabel={`Revert to v${revert?.version}`}
        reasonPlaceholder="e.g. rollback: v3 throttled the checkout service"
        onConfirm={(o) => write.mutateAsync(o)}
        successMessage={`Reverted ${policy.name} to v${revert?.version}`}
      />
    </div>
  )
}

export default function PolicyPage() {
  const { policyId } = useParams()
  const auth = useAuth()
  const [params, setParams] = useSearchParams()
  const q = usePolicy(policyId)
  const p = q.data
  const isNew = !policyId
  const readOnly = !auth.can('admin') || !!p?.deletedAt
  const tab = params.get('tab') ?? 'edit'

  const title = isNew
    ? 'New policy'
    : p
      ? p.type === 'workload-override'
        ? `Override: ${p.workloadId?.split('/').slice(1).join('/')}`
        : p.name
      : 'Policy'
  return (
    <div className="space-y-6">
      <div>
        <Link
          to="/admin/policies"
          className="inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ArrowLeftIcon className="size-3.5" /> Policies
        </Link>
      </div>
      <PageHeader
        title={<span className={cn(!isNew && 'font-mono')}>{title}</span>}
        eyebrow={
          p ? (
            <span className="inline-flex items-center gap-2">
              {TYPE_TITLE[p.type]} <OriginBadge origin="hub" />
              <span className="font-mono">v{p.version}</span>
              {p.deletedAt ? <Badge variant="destructive">deleted</Badge> : null}
            </span>
          ) : (
            'Administration'
          )
        }
        description={
          p ? (
            <span className="inline-flex flex-wrap items-center gap-1.5">
              Last changed by <ActorChip actor={p.updatedBy} time={p.updatedAt} />
            </span>
          ) : isNew ? (
            'Set modes and guardrails for a group of workloads. The agent materialises it as a labelled RightsizingPolicy.'
          ) : undefined
        }
        actions={
          p ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                const blob = new Blob([policyManifest(p.name, p.spec)], { type: 'application/yaml' })
                const a = document.createElement('a')
                a.href = URL.createObjectURL(blob)
                a.download = `${p.name}.yaml`
                a.click()
                URL.revokeObjectURL(a.href)
              }}
            >
              <DownloadIcon /> YAML
            </Button>
          ) : undefined
        }
      />
      {readOnly && !isNew ? (
        p?.deletedAt ? (
          <p className="rounded-lg border border-destructive/25 bg-destructive/5 px-3.5 py-2.5 text-[13px]">
            Deleted {formatDateTime(p.deletedAt)}. Open History to revert it to an earlier version.
          </p>
        ) : (
          <ReadOnlyNotice need="admin" signedIn={auth.signedIn} />
        )
      ) : null}
      {isNew ? (
        <Editor readOnly={false} />
      ) : q.isError ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : !p ? (
        <Skeleton className="h-[36rem] rounded-xl" />
      ) : (
        <Tabs value={tab} onValueChange={(v) => setParams(v === 'edit' ? {} : { tab: v }, { replace: true })}>
          <TabsList>
            <TabsTrigger value="edit">{readOnly ? 'Settings' : 'Edit'}</TabsTrigger>
            <TabsTrigger value="history" data-testid="tab-history">
              History
            </TabsTrigger>
            <TabsTrigger value="yaml">YAML</TabsTrigger>
          </TabsList>
          <TabsContent value="edit" className="mt-4">
            <Editor policy={p} readOnly={readOnly} />
          </TabsContent>
          <TabsContent value="history" className="mt-4">
            <History policy={p} canRevert={auth.can('admin')} />
          </TabsContent>
          <TabsContent value="yaml" className="mt-4">
            <Card className="p-4">
              <p className="mb-3 text-[13px] text-muted-foreground">
                The RightsizingPolicy as a Git manifest. To manage it in Git instead, commit this (without the
                hub label) and delete the hub policy.
              </p>
              <CodeBlock code={policyManifest(p.name, p.spec)} maxHeight={560} />
            </Card>
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}
