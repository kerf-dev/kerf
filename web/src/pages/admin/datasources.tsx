import {
  DatabaseIcon,
  FlaskConicalIcon,
  Loader2Icon,
  LockIcon,
  PencilIcon,
  PlusIcon,
  Trash2Icon,
} from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { useDatasources, useWrite, writes } from '@/api/admin'
import type { Datasource, DatasourceTestResult } from '@/api/types'
import { ActorChip } from '@/components/admin/common'
import { AUTH_LABEL, DatasourceDialog, DS_TYPE_LABEL } from '@/components/admin/datasource-dialog'
import { MetricMapping } from '@/components/admin/metric-mapping'
import { isProblem, ReadinessChecklist } from '@/components/admin/readiness'
import { WriteDialog, WriteError } from '@/components/admin/write-dialog'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Callout } from '@/components/ui/form'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'

function DatasourceCard({
  d,
  onEdit,
  onDelete,
}: {
  d: Datasource
  onEdit: () => void
  onDelete: () => void
}) {
  const [result, setResult] = useState<DatasourceTestResult>()
  const test = useWrite(() => writes.testDatasource({ id: d.id }))
  return (
    <Card className="overflow-hidden" data-testid={`datasource-${d.name}`}>
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 py-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted">
            <DatabaseIcon className="size-4 text-muted-foreground" />
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono font-semibold">{d.name}</span>
              <Badge variant="muted">{DS_TYPE_LABEL[d.type]}</Badge>
              {d.builtIn ? <Badge variant="outline">built-in</Badge> : null}
            </div>
            <div className="mt-0.5 truncate font-mono text-xs text-muted-foreground">{d.url}</div>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span className="inline-flex items-center gap-1">
                <LockIcon className="size-3" /> {AUTH_LABEL[d.auth.type]}
                {d.auth.tokenSecret ? ` · token from Secret ${d.auth.tokenSecret}` : ''}
                {d.auth.secretConfigured ? ' · secret stored' : ''}
                {d.auth.sigv4?.region ? ` · ${d.auth.sigv4.region}` : ''}
              </span>
              {d.dynatrace?.activeGateURL ? (
                <span>
                  ActiveGate <span className="font-mono">{d.dynatrace.activeGateURL}</span>
                </span>
              ) : null}
              {Object.keys(d.headers).length ? <span>{Object.keys(d.headers).join(', ')}</span> : null}
              {d.timeout ? <span>timeout {d.timeout}</span> : null}
              {d.updatedBy ? (
                <span className="inline-flex items-center gap-1">
                  changed by <ActorChip actor={d.updatedBy} time={d.updatedAt} />
                </span>
              ) : null}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            size="sm"
            variant="outline"
            disabled={test.isPending}
            onClick={() => test.mutate(undefined, { onSuccess: (r) => setResult(r as DatasourceTestResult) })}
          >
            {test.isPending ? <Loader2Icon className="animate-spin" /> : <FlaskConicalIcon />} Test
          </Button>
          {d.builtIn ? (
            // The hub manages `local` from its own configuration (prometheusURL) and answers
            // 409 builtin to any edit or delete.
            <Tooltip content="Configured by the hub (its prometheusURL setting); change it in the hub chart values.">
              <span
                className="inline-flex h-8 items-center gap-1.5 px-2 text-xs text-muted-foreground"
                data-testid="builtin-note"
              >
                <LockIcon className="size-3.5" /> Managed by the hub
              </span>
            </Tooltip>
          ) : (
            <>
              <Button size="sm" variant="ghost" onClick={onEdit} aria-label={`Edit ${d.name}`}>
                <PencilIcon />
              </Button>
              <Tooltip content={d.usedBy.length ? 'Rebind its clusters first.' : null}>
                <span>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={onDelete}
                    disabled={d.usedBy.length > 0}
                    aria-label={`Delete ${d.name}`}
                    className="text-destructive hover:text-destructive"
                  >
                    <Trash2Icon />
                  </Button>
                </span>
              </Tooltip>
            </>
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5 border-t bg-muted/20 px-5 py-2.5 text-xs">
        <span className="text-muted-foreground">Used by</span>
        {d.usedBy.length ? (
          d.usedBy.map((c) => (
            <Link
              key={c}
              to={`/clusters/${encodeURIComponent(c)}`}
              className="rounded bg-card px-1.5 py-0.5 font-mono ring-1 ring-border hover:ring-ring/40"
            >
              {c}
            </Link>
          ))
        ) : (
          <span className="text-muted-foreground">no clusters</span>
        )}
      </div>
      {test.error ? (
        <div className="border-t px-5 py-3">
          <WriteError error={test.error} />
        </div>
      ) : result ? (
        <div className="flex flex-col gap-3 border-t px-5 py-4">
          <Callout
            tone={result.ok ? 'success' : 'destructive'}
            title={result.ok ? 'Reachable' : 'Problems found'}
          >
            {result.latencyMs != null ? `${result.latencyMs} ms. ` : ''}
            {result.labelValues?.length ? `Cluster label values: ${result.labelValues.join(', ')}` : ''}
          </Callout>
          {result.metrics?.length ? <MetricMapping metrics={result.metrics} /> : null}
          {result.checks.some(isProblem) ? <ReadinessChecklist checks={result.checks} onlyProblems /> : null}
        </div>
      ) : null}
    </Card>
  )
}

export default function DatasourcesPage() {
  const q = useDatasources()
  const [edit, setEdit] = useState<Datasource | 'new' | null>(null)
  const [del, setDel] = useState<Datasource>()
  const remove = useWrite(({ reason }: { reason: string }) =>
    writes.deleteDatasource({ id: del!.id, version: del!.version, reason }),
  )
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Metrics sources"
        description="Prometheus-compatible endpoints the hub reads usage from. Each cluster is bound to one with a cluster label (set in the cluster's onboarding)."
        actions={
          <Button size="sm" onClick={() => setEdit('new')} data-testid="new-datasource">
            <PlusIcon /> New datasource
          </Button>
        }
      />
      {q.isError ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : !q.data ? (
        <div className="space-y-3">
          <Skeleton className="h-32 rounded-xl" />
          <Skeleton className="h-32 rounded-xl" />
        </div>
      ) : !q.data.items.length ? (
        <Card>
          <EmptyState
            icon={DatabaseIcon}
            title="No datasources"
            description="The hub registers its built-in VictoriaMetrics as local at start-up."
          />
        </Card>
      ) : (
        <div className="flex flex-col gap-3">
          {q.data.items.map((d) => (
            <DatasourceCard key={d.id} d={d} onEdit={() => setEdit(d)} onDelete={() => setDel(d)} />
          ))}
        </div>
      )}
      <DatasourceDialog
        open={!!edit}
        onOpenChange={(o) => !o && setEdit(null)}
        datasource={edit && edit !== 'new' ? edit : undefined}
      />
      <WriteDialog
        open={!!del}
        onOpenChange={(o) => !o && setDel(undefined)}
        tone="destructive"
        title={`Delete ${del?.name}`}
        description="Its stored credentials are removed."
        confirmLabel="Delete datasource"
        onConfirm={(o) => remove.mutateAsync(o)}
        successMessage={`Deleted ${del?.name}`}
      />
    </div>
  )
}
