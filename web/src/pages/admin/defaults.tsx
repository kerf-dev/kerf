import { FileCodeIcon, HistoryIcon, SlidersHorizontalIcon } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useGitOnlyClusters, usePolicies, useWrite, writes } from '@/api/admin'
import { useClusters } from '@/api/queries'
import type { AdminPolicy, PolicyChange, RightsizingPolicySpec } from '@/api/types'
import { ActorChip, CodeBlock } from '@/components/admin/common'
import { GitOnlyNotice } from '@/components/admin/git-notice'
import { ImpactPreview } from '@/components/admin/impact-preview'
import { PolicySpecForm } from '@/components/admin/policy-form'
import { SaveBar, usePreview } from '@/components/admin/save-bar'
import { WriteDialog } from '@/components/admin/write-dialog'
import { ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { changedPaths, policyManifest } from '@/lib/policy'
import { BUILT_IN, prune } from '@/lib/spec'
import { validateSpec } from '@/lib/validate'

/**
 * Global defaults: the hub policy `ro-global-defaults` (priority 0, every cluster). Each field shows the
 * agent's built-in default it overrides; saving shows the impact preview first.
 */
export default function DefaultsPage() {
  const policies = usePolicies({ type: ['global-default'] })
  const clusters = useClusters()
  const git = useGitOnlyClusters()
  const current: AdminPolicy | undefined = policies.data?.items.find(
    (p) => p.type === 'global-default' && !p.deletedAt,
  )
  const base = useMemo(
    () => prune({ ...(current?.spec ?? {}), priority: undefined, selector: undefined }),
    [current],
  )
  const [spec, setSpec] = useState<RightsizingPolicySpec>(base)
  const [confirm, setConfirm] = useState(false)
  const [yaml, setYaml] = useState(false)
  useEffect(() => setSpec(base), [base])

  const errors = validateSpec(spec, 'global-default')
  const changed = changedPaths(base, spec)
  const dirty = changed.length > 0
  const changes: PolicyChange[] = [
    { op: 'upsert', ...(current ? { policyId: current.id } : {}), policy: { type: 'global-default', spec } },
  ]
  const preview = usePreview(changes, confirm)
  const prodEnv = new Set(
    (clusters.data?.items ?? []).filter((c) => c.environment === 'prod').map((c) => c.name),
  )
  const prodTouched = [...new Set((preview.data?.items ?? []).map((i) => i.cluster))].filter((c) =>
    prodEnv.has(c),
  )

  const save = useWrite(({ reason, overrideReadiness }: { reason: string; overrideReadiness?: boolean }) => {
    const body = {
      type: 'global-default' as const,
      spec,
      reason,
      ...(overrideReadiness ? { overrideReadiness } : {}),
    }
    return current
      ? writes.updatePolicy({ id: current.id, version: current.version, body })
      : writes.createPolicy(body)
  })

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Global defaults"
        description="The lowest hub layer: applies to every workload on every cluster unless a cluster default, policy, override or annotation sets the field."
        actions={
          <>
            {current ? (
              <Button asChild variant="outline" size="sm">
                <Link to={`/admin/policies/${current.id}?tab=history`}>
                  <HistoryIcon /> History
                </Link>
              </Button>
            ) : null}
            <Button variant="outline" size="sm" onClick={() => setYaml(true)}>
              <FileCodeIcon /> Export YAML
            </Button>
          </>
        }
      />
      {policies.isError ? (
        <Card>
          <ErrorState error={policies.error} onRetry={() => policies.refetch()} />
        </Card>
      ) : policies.isPending ? (
        <Skeleton className="h-[40rem] rounded-xl" />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-[13px] text-muted-foreground">
            {current ? (
              <>
                <Badge variant="outline" className="font-mono">
                  ro-global-defaults · v{current.version}
                </Badge>
                <span className="inline-flex items-center gap-1.5">
                  Last changed by <ActorChip actor={current.updatedBy} time={current.updatedAt} />
                </span>
              </>
            ) : (
              <span className="inline-flex items-center gap-2">
                <SlidersHorizontalIcon className="size-4" /> No global defaults saved yet: the agent's
                built-in defaults apply. Saving creates <code className="font-mono">ro-global-defaults</code>.
              </span>
            )}
          </div>
          <GitOnlyNotice clusters={git.names} what="The global defaults" />
          <Card>
            <CardContent className="py-6">
              <PolicySpecForm
                value={spec}
                onChange={setSpec}
                inherited={BUILT_IN}
                inheritedFrom="built-in"
                errors={errors}
              />
            </CardContent>
          </Card>
          <SaveBar
            dirty={dirty}
            count={changed.length}
            errors={Object.keys(errors).length}
            onDiscard={() => setSpec(base)}
            onSave={() => setConfirm(true)}
          />
        </>
      )}

      <WriteDialog
        open={confirm}
        onOpenChange={setConfirm}
        wide
        testId="defaults-dialog"
        title="Save global defaults"
        description="This is what changes once agents pull the new defaults (within about 30 s). Guardrails, cooldowns and learning periods still apply."
        prodClusters={prodTouched}
        confirmLabel="Save defaults"
        confirmDisabled={!preview.data}
        onConfirm={(o) => save.mutateAsync(o)}
        successMessage="Global defaults saved"
      >
        <ImpactPreview preview={preview.data} loading={preview.isFetching} error={preview.error} />
      </WriteDialog>

      <Dialog open={yaml} onOpenChange={setYaml}>
        <DialogContent className="top-[10%] w-[min(720px,calc(100vw-2rem))] p-5">
          <DialogTitle className="text-base font-semibold">Global defaults as YAML</DialogTitle>
          <DialogDescription className="mt-1 mb-3 text-[13px] text-muted-foreground">
            For clusters with policy source Git: commit this as a RightsizingPolicy (without the hub label, so
            the agent treats it as a Git policy).
          </DialogDescription>
          <CodeBlock code={policyManifest('team-global-defaults', { priority: 1, ...spec })} />
        </DialogContent>
      </Dialog>
    </div>
  )
}
