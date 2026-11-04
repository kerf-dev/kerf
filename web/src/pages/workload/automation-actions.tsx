import {
  BanIcon,
  CheckCircle2Icon,
  GitForkIcon,
  LayersIcon,
  LogInIcon,
  PencilIcon,
  StampIcon,
  Undo2Icon,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router'
import { useEffectivePolicy, useWrite, writes } from '@/api/admin'
import { useAuth } from '@/api/auth'
import { useClusters } from '@/api/queries'
import type { Mode, Profile, RightsizingPolicySpec, Strategy, WorkloadDetail } from '@/api/types'
import { ActorChip } from '@/components/admin/common'
import { EffectivePolicyView } from '@/components/admin/effective-policy'
import { ImpactPreview } from '@/components/admin/impact-preview'
import { ModeField } from '@/components/admin/policy-form'
import { STATE_INFO } from '@/components/admin/readiness'
import { usePreview } from '@/components/admin/save-bar'
import { WriteDialog } from '@/components/admin/write-dialog'
import { ErrorState } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Callout, Field, NativeSelect } from '@/components/ui/form'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatUSD, relativeTime } from '@/lib/format'
import { withoutSelector } from '@/lib/policy'
import { MODE_LABEL, prune, specChips } from '@/lib/spec'

type Kind = 'override' | 'exclude' | 'clear' | 'approve' | 'why'

function overrideBase(w: WorkloadDetail): RightsizingPolicySpec {
  return withoutSelector(w.override?.spec)
}

/**
 * Operator actions on one workload (D-022, D-024): override modes/strategy/profile, Exclude (both
 * modes off), Clear override, Approve once. Writes go through the hub; the agent applies them on its
 * next pull, so modes here change once it reports back (the hub serves the intent immediately).
 */
export function AutomationActions({ w }: { w: WorkloadDetail }) {
  const auth = useAuth()
  const loc = useLocation()
  const clusters = useClusters()
  const cluster = clusters.data?.items.find((c) => c.name === w.cluster)
  const prod = cluster?.environment === 'prod'
  // Git-only clusters get no hub policies (D-022), so a hub override would never apply there.
  const gitOnly = cluster?.policySource === 'git'
  const ready = !cluster?.readiness || cluster.readiness.state === 'AutomationReady'
  const [open, setOpen] = useState<Kind | null>(null)
  const [draft, setDraft] = useState<RightsizingPolicySpec>({})
  useEffect(() => {
    if (open === 'override') setDraft(overrideBase(w))
  }, [open, w])

  const rec = w.recommendation
  const inAudit = w.modes.cpu === 'audit' || w.modes.memory === 'audit'
  const ap = w.approval
  const approvalOpen = ap?.state === 'pending' && ap.recommendationID === rec?.id
  const canApprove = !!rec && inAudit && !approvalOpen
  const approveWhyNot = !rec
    ? 'No recommendation yet.'
    : !inAudit
      ? 'Approve once applies to resources in audit; none is.'
      : approvalOpen
        ? 'Already approved; waiting for the agent.'
        : null

  const spec = prune(draft)
  const effectiveSpec: RightsizingPolicySpec =
    open === 'exclude'
      ? {
          ...overrideBase(w),
          cpu: { ...overrideBase(w).cpu, mode: 'off' },
          memory: { ...overrideBase(w).memory, mode: 'off' },
        }
      : spec
  const preview = usePreview(
    [
      {
        op: 'upsert',
        ...(w.override ? { policyId: w.override.id } : {}),
        policy: { type: 'workload-override', workloadId: w.id, clusters: [w.cluster], spec: effectiveSpec },
      },
    ],
    open === 'override' || open === 'exclude',
  )
  const setsAuto = effectiveSpec.cpu?.mode === 'auto-patch' || effectiveSpec.memory?.mode === 'auto-patch'

  const put = useWrite(({ reason, overrideReadiness }: { reason: string; overrideReadiness?: boolean }) =>
    writes.putOverride({
      id: w.id,
      version: w.override?.version,
      body: { spec: effectiveSpec, reason, ...(overrideReadiness ? { overrideReadiness } : {}) },
    }),
  )
  const clear = useWrite(({ reason }: { reason: string }) =>
    writes.deleteOverride({ id: w.id, version: w.override!.version, reason }),
  )
  const approve = useWrite(({ reason, overrideReadiness }: { reason: string; overrideReadiness?: boolean }) =>
    writes.approve({ id: w.id, recommendationID: rec!.id, reason, overrideReadiness }),
  )

  const can = auth.can('operator')
  return (
    <div className="space-y-3 border-t pt-3" data-testid="automation-actions">
      {w.override ? (
        <div
          className="rounded-lg border border-primary/25 bg-primary/5 px-3 py-2.5 text-[13px]"
          data-testid="override-active"
        >
          <div className="flex items-center justify-between gap-2">
            <span className="flex items-center gap-1.5 font-medium">
              <GitForkIcon className="size-3.5 text-primary" /> Workload override
            </span>
            <span className="font-mono text-[11px] text-muted-foreground">v{w.override.version}</span>
          </div>
          <div className="mt-1 flex flex-wrap gap-1">
            {specChips(overrideBase(w)).map((c) => (
              <Badge key={c} variant="secondary" className="text-[11px]">
                {c}
              </Badge>
            ))}
          </div>
          <div className="mt-1.5 text-xs text-muted-foreground">
            <ActorChip actor={w.override.updatedBy} time={w.override.updatedAt} />
          </div>
        </div>
      ) : null}
      {ap ? (
        <div
          className="flex items-start gap-2 rounded-lg border px-3 py-2.5 text-[13px]"
          data-testid="approval"
        >
          {ap.state === 'consumed' ? (
            <CheckCircle2Icon className="mt-0.5 size-4 shrink-0 text-success" />
          ) : (
            <StampIcon className="mt-0.5 size-4 shrink-0 text-info" />
          )}
          <div className="min-w-0">
            <div className="font-medium">
              {ap.state === 'pending'
                ? 'Approved once, waiting for the agent'
                : ap.state === 'consumed'
                  ? `Applied once ${ap.consumedAt ? relativeTime(ap.consumedAt) : ''}`
                  : ap.state === 'expired'
                    ? 'Approval expired'
                    : 'Approval superseded'}
            </div>
            <div className="text-xs text-muted-foreground">
              by {ap.approvedBy.name} {relativeTime(ap.approvedAt)}
              {ap.state === 'pending' ? ` · expires ${formatDateTime(ap.expiresAt, true)}` : ''}
              {ap.reason ? ` · “${ap.reason}”` : ''}
            </div>
          </div>
        </div>
      ) : null}
      {can ? (
        <div className="grid grid-cols-2 gap-2">
          {gitOnly ? (
            <p
              className="col-span-2 rounded-md bg-muted/60 px-2.5 py-2 text-xs text-muted-foreground"
              data-testid="git-only"
            >
              {w.cluster} takes its policies from Git: set modes there (a RightsizingPolicy or workload
              annotations). Approve once still works.
            </p>
          ) : (
            <>
              <Button
                size="sm"
                variant="outline"
                onClick={() => setOpen('override')}
                data-testid="set-override"
              >
                <PencilIcon /> {w.override ? 'Edit override' : 'Override'}
              </Button>
              {w.override ? (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setOpen('clear')}
                  data-testid="clear-override"
                >
                  <Undo2Icon /> Clear override
                </Button>
              ) : (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setOpen('exclude')}
                  disabled={w.modes.cpu === 'off' && w.modes.memory === 'off'}
                  data-testid="exclude"
                >
                  <BanIcon /> Exclude
                </Button>
              )}
            </>
          )}
          <Tooltip content={approveWhyNot}>
            <span className="col-span-2">
              <Button
                size="sm"
                className="w-full"
                onClick={() => setOpen('approve')}
                disabled={!canApprove}
                data-testid="approve-once"
              >
                <StampIcon /> Approve once
                {rec ? (
                  <span className="opacity-80">· {formatUSD(rec.monthlySavingsUSD.total)}/mo</span>
                ) : null}
              </Button>
            </span>
          </Tooltip>
        </div>
      ) : (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <LogInIcon className="size-3.5" />
          {auth.signedIn ? (
            'Changing automation needs the operator role.'
          ) : (
            <span>
              <Link
                to={`/login?next=${encodeURIComponent(loc.pathname)}`}
                className="font-medium text-primary hover:underline"
              >
                Sign in
              </Link>{' '}
              as an operator to change automation.
            </span>
          )}
        </p>
      )}
      <Button
        size="xs"
        variant="ghost"
        className="w-full text-muted-foreground"
        onClick={() => setOpen('why')}
        data-testid="why-mode"
      >
        <LayersIcon /> Why these modes? Show the effective policy
      </Button>

      <WriteDialog
        open={open === 'override' || open === 'exclude'}
        onOpenChange={(o) => !o && setOpen(null)}
        wide={open === 'override'}
        testId="override-dialog"
        title={
          open === 'exclude'
            ? `Exclude ${w.name}`
            : `${w.override ? 'Edit' : 'Set'} the override for ${w.name}`
        }
        description={
          open === 'exclude'
            ? 'Sets CPU and memory to off for this workload: the agent ignores it. Annotations on the workload still win.'
            : 'Applies to this workload only, on top of its policy. Unset fields inherit. Annotations on the workload still win (D-005).'
        }
        prodClusters={prod ? [w.cluster] : []}
        confirmLabel={open === 'exclude' ? 'Exclude workload' : 'Save override'}
        confirmDisabled={open === 'override' && !Object.keys(spec).length}
        reasonPlaceholder={
          open === 'exclude'
            ? 'e.g. batch job, owner asked to leave it alone'
            : 'e.g. reviewed with the payments team'
        }
        onConfirm={(o) => put.mutateAsync(o)}
        successMessage={open === 'exclude' ? `${w.name} excluded` : `Override saved for ${w.name}`}
      >
        {open === 'override' ? (
          <div className="grid gap-4">
            <div className="grid gap-4 @lg:grid-cols-2">
              <ModeField
                label="CPU mode"
                value={draft.cpu?.mode}
                inherited={w.override ? undefined : w.modes.cpu}
                onChange={(m) =>
                  setDraft(prune({ ...draft, cpu: { ...draft.cpu, mode: m as Mode | undefined } }))
                }
                testId="override-cpu"
              />
              <ModeField
                label="Memory mode"
                value={draft.memory?.mode}
                inherited={w.override ? undefined : w.modes.memory}
                onChange={(m) =>
                  setDraft(prune({ ...draft, memory: { ...draft.memory, mode: m as Mode | undefined } }))
                }
                testId="override-memory"
              />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Apply strategy">
                <NativeSelect
                  value={draft.apply?.strategy ?? ''}
                  onChange={(e) =>
                    setDraft(
                      prune({
                        ...draft,
                        apply: {
                          ...draft.apply,
                          strategy: (e.target.value || undefined) as Strategy | undefined,
                        },
                      }),
                    )
                  }
                >
                  <option value="">Inherit{w.status?.strategy ? ` (${w.status.strategy})` : ''}</option>
                  <option value="in-place">In-place resize</option>
                  <option value="rollout">Rolling restart</option>
                  <option value="deferred">Deferred to window</option>
                </NativeSelect>
              </Field>
              <Field label="Recommender profile">
                <NativeSelect
                  value={draft.profile ?? ''}
                  onChange={(e) =>
                    setDraft(
                      prune({ ...draft, profile: (e.target.value || undefined) as Profile | undefined }),
                    )
                  }
                >
                  <option value="">
                    Inherit{w.status?.settings?.profile ? ` (${w.status.settings.profile})` : ''}
                  </option>
                  <option value="dev">dev</option>
                  <option value="production">production</option>
                  <option value="high-availability">high-availability</option>
                </NativeSelect>
              </Field>
            </div>
          </div>
        ) : null}
        {setsAuto && !ready && cluster?.readiness ? (
          <Callout tone="warning" title={`${w.cluster} is ${STATE_INFO[cluster.readiness.state].label}`}>
            Auto-patch needs Automation ready. The hub refuses it unless an admin overrides readiness.
          </Callout>
        ) : null}
        {setsAuto && prod && !auth.can('admin') ? (
          <Callout tone="warning" title="Needs an admin">
            Auto-patch on a prod cluster can only be set by an admin.
          </Callout>
        ) : null}
        <ImpactPreview preview={preview.data} loading={preview.isFetching} error={preview.error} compact />
      </WriteDialog>

      <WriteDialog
        open={open === 'clear'}
        onOpenChange={(o) => !o && setOpen(null)}
        testId="clear-dialog"
        title={`Clear the override for ${w.name}`}
        description="The workload returns to the settings of its policy and defaults on the agent's next pull."
        prodClusters={prod ? [w.cluster] : []}
        confirmLabel="Clear override"
        reasonPlaceholder="e.g. back to the team policy"
        onConfirm={(o) => clear.mutateAsync(o)}
        successMessage={`Override cleared for ${w.name}`}
      />

      <WriteDialog
        open={open === 'approve'}
        onOpenChange={(o) => !o && setOpen(null)}
        testId="approve-dialog"
        title={`Approve the current recommendation for ${w.name} once`}
        description="The agent applies this recommendation once, as if the mode were auto-patch, with every guardrail. The workload stays in audit; the next recommendation is not applied (D-024)."
        prodClusters={prod ? [w.cluster] : []}
        confirmLabel="Approve once"
        reasonPlaceholder="e.g. reviewed with the owner"
        onConfirm={(o) => approve.mutateAsync(o)}
        successMessage={`Approved once: ${w.name}`}
      >
        {rec ? (
          <div className="grid grid-cols-3 gap-2 text-[13px]">
            <div className="rounded-lg border px-3 py-2">
              <div className="text-[11px] text-muted-foreground">Savings</div>
              <div className="font-semibold text-success tabular">
                {formatUSD(rec.monthlySavingsUSD.total)}/mo
              </div>
            </div>
            <div className="rounded-lg border px-3 py-2">
              <div className="text-[11px] text-muted-foreground">Resources in audit</div>
              <div className="font-semibold">
                {[w.modes.cpu === 'audit' ? 'CPU' : '', w.modes.memory === 'audit' ? 'memory' : '']
                  .filter(Boolean)
                  .join(' + ')}
              </div>
            </div>
            <div className="rounded-lg border px-3 py-2">
              <div className="text-[11px] text-muted-foreground">Expires</div>
              <div className="font-semibold">in 24 h</div>
            </div>
          </div>
        ) : null}
        {!ready && cluster?.readiness ? (
          <Callout tone="warning" title={`${w.cluster} is ${STATE_INFO[cluster.readiness.state].label}`}>
            Approve once needs Automation ready. The hub refuses it unless an admin overrides readiness.
          </Callout>
        ) : null}
      </WriteDialog>

      <WhyDialog
        id={w.id}
        open={open === 'why'}
        onOpenChange={(o) => !o && setOpen(null)}
        name={w.name}
        modes={`${MODE_LABEL[w.modes.cpu]} / ${MODE_LABEL[w.modes.memory]}`}
      />
    </div>
  )
}

function WhyDialog({
  id,
  open,
  onOpenChange,
  name,
  modes,
}: {
  id: string
  open: boolean
  onOpenChange: (o: boolean) => void
  name: string
  modes: string
}) {
  const ep = useEffectivePolicy(id, open)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="top-[6%] max-h-[88dvh] w-[min(760px,calc(100vw-2rem))] overflow-y-auto p-5">
        <DialogTitle className="text-base font-semibold">Effective policy of {name}</DialogTitle>
        <DialogDescription className="mt-1 mb-4 text-[13px] text-muted-foreground">
          Highest precedence first. Currently {modes} (CPU / memory).
        </DialogDescription>
        {ep.isError ? (
          <ErrorState error={ep.error} compact />
        ) : !ep.data ? (
          <Skeleton className="h-64" />
        ) : (
          <EffectivePolicyView ep={ep.data} />
        )}
      </DialogContent>
    </Dialog>
  )
}
