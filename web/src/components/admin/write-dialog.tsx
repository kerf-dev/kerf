import { useQueryClient } from '@tanstack/react-query'
import { AlertTriangleIcon, GitCompareArrowsIcon, Loader2Icon, LockIcon, ShieldAlertIcon } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { ApiError, isConflict } from '@/api/client'
import { useAuth } from '@/api/auth'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Callout, Checkbox, Field, Label, Textarea } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { toast } from '@/components/ui/toast'
import { cn } from '@/lib/utils'

export interface WriteOptions {
  reason: string
  /** Set after an admin ticks "override readiness" on a `409 cluster_not_automation_ready`. */
  overrideReadiness?: boolean
}

/**
 * Confirmation dialog for every configuration write (docs/contracts/admin.md §4):
 * - asks for the required `reason` (stored in the audit log);
 * - for clusters labelled `prod`, asks the user to type the cluster name;
 * - shows a version conflict (`412`) with a reload option instead of overwriting;
 * - offers admins the audited readiness override on `409 cluster_not_automation_ready` (D-025).
 */
export function WriteDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  confirmLabel = 'Save',
  tone = 'default',
  prodClusters = [],
  onConfirm,
  successMessage,
  reasonPlaceholder = 'e.g. FinOps Q4 rollout, agreed with the payments team',
  reasonRequired = true,
  wide,
  testId = 'write-dialog',
  confirmDisabled,
  onSubmitAttempt,
  reasonAutoFocus = true,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: React.ReactNode
  description?: React.ReactNode
  children?: React.ReactNode
  confirmLabel?: string
  tone?: 'default' | 'destructive'
  /** Target clusters with environment `prod`: each must be typed to confirm. */
  prodClusters?: string[]
  onConfirm: (o: WriteOptions) => Promise<unknown>
  successMessage?: string | ((result: unknown) => string)
  reasonPlaceholder?: string
  reasonRequired?: boolean
  wide?: boolean
  testId?: string
  confirmDisabled?: boolean
  /** Called on every submit, before the checks, so a form can show its own field errors. */
  onSubmitAttempt?: () => void
  /** Focus the reason on open. Turn off for forms with fields above it, so the first field gets focus. */
  reasonAutoFocus?: boolean
}) {
  const auth = useAuth()
  const qc = useQueryClient()
  const id = useId()
  const [reason, setReason] = useState('')
  const [typed, setTyped] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>()
  const [pending, setPending] = useState(false)
  const [override, setOverride] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setReason('')
      setTyped({})
      setError(undefined)
      setOverride(false)
      setTouched(false)
    }
  }, [open])

  const reasonMissing = reasonRequired && !reason.trim()
  const reasonTooLong = reason.length > 1024
  const prodOk = prodClusters.every((c) => (typed[c] ?? '').trim() === c)
  const notReady = error instanceof ApiError && error.code === 'cluster_not_automation_ready'
  const canSubmit =
    !reasonMissing && !reasonTooLong && prodOk && !pending && !confirmDisabled && (!notReady || override)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setTouched(true)
    onSubmitAttempt?.()
    if (!canSubmit) return
    setPending(true)
    setError(undefined)
    try {
      const res = await onConfirm({ reason: reason.trim(), ...(override ? { overrideReadiness: true } : {}) })
      onOpenChange(false)
      if (successMessage)
        toast.success(typeof successMessage === 'function' ? successMessage(res) : successMessage)
    } catch (err) {
      setError(err)
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent
        className={cn(
          'top-[8%] max-h-[86dvh] overflow-y-auto',
          wide ? 'w-[min(820px,calc(100vw-2rem))]' : '',
        )}
        data-testid={testId}
      >
        <form onSubmit={submit} className="flex flex-col">
          <div className="border-b px-5 pt-5 pb-4">
            <DialogTitle className="text-base font-semibold tracking-tight">{title}</DialogTitle>
            {description ? (
              <DialogDescription className="mt-1 text-[13px] text-pretty text-muted-foreground">
                {description}
              </DialogDescription>
            ) : (
              <DialogDescription className="sr-only">Confirm the change</DialogDescription>
            )}
          </div>
          <div className="@container flex flex-col gap-4 px-5 py-4">
            {children}
            <WriteError
              error={error}
              onReload={() => {
                void qc.invalidateQueries()
                onOpenChange(false)
              }}
            />
            {notReady && auth.can('admin') ? (
              <label className="flex items-start gap-2.5 rounded-lg border border-warning/35 bg-warning/6 px-3 py-2.5 text-[13px]">
                <Checkbox
                  checked={override}
                  onCheckedChange={(v) => setOverride(v === true)}
                  className="mt-0.5"
                />
                <span>
                  <span className="font-medium">Override readiness for this change</span>
                  <span className="block text-muted-foreground">
                    Applies auto-patch although the cluster is not Automation ready. The override is recorded
                    in the audit log with your reason.
                  </span>
                </span>
              </label>
            ) : null}
            {prodClusters.length ? (
              <div className="flex flex-col gap-2.5 rounded-lg border border-destructive/25 bg-destructive/5 px-3.5 py-3">
                <div className="flex items-center gap-2 text-[13px] font-medium">
                  <ShieldAlertIcon className="size-4 text-destructive" />
                  {prodClusters.length === 1
                    ? 'This changes a production cluster'
                    : 'This changes production clusters'}
                </div>
                {prodClusters.map((c) => (
                  <div key={c} className="flex flex-col gap-1.5">
                    <Label htmlFor={`${id}-prod-${c}`} className="font-normal text-muted-foreground">
                      Type <span className="font-mono font-medium text-foreground">{c}</span> to confirm
                    </Label>
                    <Input
                      id={`${id}-prod-${c}`}
                      value={typed[c] ?? ''}
                      onChange={(e) => setTyped((t) => ({ ...t, [c]: e.target.value }))}
                      autoComplete="off"
                      spellCheck={false}
                      className="font-mono"
                      aria-invalid={touched && (typed[c] ?? '').trim() !== c}
                      data-testid="prod-confirm"
                    />
                  </div>
                ))}
              </div>
            ) : null}
            <Field
              label="Reason"
              htmlFor={`${id}-reason`}
              optional={!reasonRequired}
              hint="Recorded in the audit log with your name."
              error={
                touched && reasonMissing
                  ? 'Give a reason for the change.'
                  : reasonTooLong
                    ? 'At most 1024 characters.'
                    : undefined
              }
            >
              <Textarea
                id={`${id}-reason`}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder={reasonPlaceholder}
                rows={2}
                aria-invalid={touched && (reasonMissing || reasonTooLong)}
                data-testid="reason"
                autoFocus={reasonAutoFocus}
              />
            </Field>
          </div>
          <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => onOpenChange(false)}
              disabled={pending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              size="sm"
              variant={tone === 'destructive' ? 'destructive' : 'default'}
              disabled={pending || (touched && !canSubmit)}
              data-testid="confirm-write"
            >
              {pending ? <Loader2Icon className="animate-spin" /> : null}
              {confirmLabel}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Titles for hub problem codes (hub.yaml / internal/hub) that need no special handling beyond words. */
const CODE_TITLE: Record<string, string> = {
  builtin: 'The built-in datasource is managed by the hub',
  datasource_in_use: 'The datasource is still in use',
  unknown_datasource: 'Unknown datasource',
  invalid_metrics: 'The metrics binding is invalid',
  invalid_datasource: 'The datasource settings are invalid',
  invalid_cluster: 'The cluster settings are invalid',
  already_exists: 'It already exists',
  last_admin: 'At least one enabled admin must remain',
  recommendation_stale: 'A newer recommendation exists',
  not_in_audit: 'Nothing to approve',
  not_found: 'It no longer exists',
  invalid_channel: 'The channel settings are invalid',
  invalid_rule: 'The rule is invalid',
  invalid_digest: 'The digest settings are invalid',
  channel_in_use: 'The channel is still in use',
  channel_read_only: 'This channel is managed by the chart values',
  secrets_key_missing: 'The hub cannot store webhook URLs yet',
  no_channels: 'The digest has no channel to send to',
}

/** What to do next, for problem codes where the hub's own message does not say. */
const CODE_HINT: Record<string, string> = {
  channel_in_use: 'Remove it from the rules and the digest that send to it first.',
  channel_read_only: 'Change or remove it in the hub chart values and upgrade the hub.',
  secrets_key_missing: 'Set the hub secrets encryption key in the chart values, then try again.',
  no_channels: 'Pick at least one channel for the digest and save it first.',
}

/** Explains a failed write in terms of what the user can do about it. */
export function WriteError({ error, onReload }: { error: unknown; onReload?: () => void }) {
  if (!error) return null
  if (isConflict(error))
    return (
      <Callout
        tone="warning"
        icon={GitCompareArrowsIcon}
        title="Someone else changed this in the meantime"
        action={
          onReload ? (
            <Button
              size="xs"
              variant="outline"
              type="button"
              onClick={onReload}
              data-testid="reload-after-conflict"
            >
              Reload
            </Button>
          ) : null
        }
      >
        Your change was not saved, so nothing was overwritten. Reload to see the current version, then make
        your change again.
      </Callout>
    )
  const e = error instanceof ApiError ? error : undefined
  if (e?.code === 'cluster_not_automation_ready')
    return (
      <Callout tone="warning" icon={AlertTriangleIcon} title="Cluster not ready for auto-patch">
        {e.problem?.detail || 'A target cluster is not in the Automation ready state.'} Fix the blocking
        readiness checks first, or keep the workloads in audit.
      </Callout>
    )
  if (e?.code === 'admin_required' || e?.status === 403)
    return (
      <Callout
        tone="destructive"
        icon={LockIcon}
        title={e.code === 'csrf_invalid' ? 'Session check failed' : 'Not allowed'}
      >
        {e.code === 'csrf_invalid'
          ? 'Your session token is out of date. Reload the page and try again.'
          : e.code === 'admin_required'
            ? `${e.problem?.detail || 'This change'} needs an admin.`
            : e.message}
      </Callout>
    )
  return (
    <Callout
      tone="destructive"
      icon={AlertTriangleIcon}
      title={
        (e?.code && CODE_TITLE[e.code]) ??
        (e?.status === 400 ? 'The hub rejected the change' : 'Could not save')
      }
    >
      {e ? e.message : String(error)}
      {e?.code && CODE_HINT[e.code] ? <span className="block">{CODE_HINT[e.code]}</span> : null}
      {e?.code ? <span className="ml-1 font-mono text-[11px]">({e.code})</span> : null}
    </Callout>
  )
}
