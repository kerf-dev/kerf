import { PauseCircleIcon, PlayCircleIcon } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useWrite, writes } from '@/api/admin'
import type { Automation } from '@/api/types'
import { Field } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { formatDateTime } from '@/lib/format'
import { WriteDialog } from './write-dialog'

type Until = 'manual' | '1h' | '4h' | 'tomorrow' | 'custom'

function resumeAtFor(u: Until, custom: string): string | undefined {
  const now = Date.now()
  if (u === '1h') return new Date(now + 3600_000).toISOString()
  if (u === '4h') return new Date(now + 4 * 3600_000).toISOString()
  if (u === 'tomorrow') {
    const d = new Date(now + 86400_000)
    d.setHours(9, 0, 0, 0)
    return d.toISOString()
  }
  if (u === 'custom' && custom) return new Date(custom).toISOString()
  return undefined
}

/**
 * Pause or resume automation for one cluster or the whole fleet. A pause works like a
 * cluster-wide freeze: no new changes, rollbacks still allowed; agents pick it up on their next pull.
 */
export function PauseDialog({
  open,
  onOpenChange,
  target,
  automation,
  prod,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Cluster name, or `undefined` for the fleet. */
  target?: string
  automation?: Automation
  prod?: boolean
}) {
  const pausing = !automation?.paused
  const id = useId()
  const [until, setUntil] = useState<Until>('manual')
  const [custom, setCustom] = useState('')
  useEffect(() => {
    if (open) {
      setUntil('manual')
      setCustom('')
    }
  }, [open])
  const write = useWrite((reason: string) => {
    const body = pausing
      ? {
          paused: true,
          reason,
          ...(resumeAtFor(until, custom) ? { resumeAt: resumeAtFor(until, custom) } : {}),
        }
      : { paused: false, reason }
    return target ? writes.setClusterAutomation({ cluster: target, body }) : writes.setFleetAutomation(body)
  })
  const customBad = until === 'custom' && (!custom || Date.parse(custom) <= Date.now())
  const what = target ? <span className="font-mono">{target}</span> : 'the whole fleet'
  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      testId="pause-dialog"
      title={
        <span className="flex items-center gap-2">
          {pausing ? (
            <PauseCircleIcon className="size-5 text-warning" />
          ) : (
            <PlayCircleIcon className="size-5 text-success" />
          )}
          {pausing ? 'Pause automation for ' : 'Resume automation for '}
          {what}
        </span>
      }
      description={
        pausing
          ? 'Agents stop applying new changes on their next pull (within about 30 s). Rollbacks still run. Recommendations keep being computed.'
          : 'Agents resume applying auto-patch changes on their next pull, within the usual guardrails and windows.'
      }
      confirmLabel={pausing ? 'Pause automation' : 'Resume automation'}
      tone={pausing ? 'destructive' : 'default'}
      prodClusters={prod && target ? [target] : []}
      reasonPlaceholder={
        pausing ? 'e.g. incident INC-4711, change freeze for the release' : 'e.g. incident resolved'
      }
      confirmDisabled={pausing && customBad}
      onConfirm={({ reason }) => write.mutateAsync(reason)}
      successMessage={
        pausing
          ? `Automation paused for ${target ?? 'the fleet'}`
          : `Automation resumed for ${target ?? 'the fleet'}`
      }
    >
      {pausing ? (
        <Field
          label="Resume automatically"
          hint="The hub resumes at this time; you can resume earlier by hand."
        >
          <Segmented
            label="Resume automatically"
            value={until}
            onChange={setUntil}
            className="w-full flex-wrap [&>*]:flex-1"
            options={[
              { value: 'manual', label: 'Never' },
              { value: '1h', label: 'In 1 hour' },
              { value: '4h', label: 'In 4 hours' },
              { value: 'tomorrow', label: 'Tomorrow 09:00' },
              { value: 'custom', label: 'Custom' },
            ]}
          />
          {until === 'custom' ? (
            <Input
              id={`${id}-custom`}
              type="datetime-local"
              value={custom}
              onChange={(e) => setCustom(e.target.value)}
              className="mt-2"
              aria-invalid={customBad}
              aria-label="Resume at"
            />
          ) : null}
          {until !== 'manual' && resumeAtFor(until, custom) ? (
            <p className="mt-1.5 text-xs text-muted-foreground">
              Resumes {formatDateTime(resumeAtFor(until, custom))}
            </p>
          ) : null}
        </Field>
      ) : automation?.reason ? (
        <div className="rounded-lg border bg-muted/30 px-3 py-2 text-[13px]">
          <span className="text-muted-foreground">Paused because: </span>
          {automation.reason}
        </div>
      ) : null}
    </WriteDialog>
  )
}
