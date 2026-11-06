import { WebhookIcon, XIcon } from 'lucide-react'
import { useState } from 'react'
import type {
  NotificationChannel,
  NotificationChannelType,
  NotificationDeliveryStatus,
  NotificationEventType,
  NotificationRule,
} from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/form'
import { validateGlob } from '@/lib/validate'
import { cn } from '@/lib/utils'

export const CHANNEL_TYPE_LABEL: Record<NotificationChannelType, string> = {
  slack: 'Slack',
  teams: 'Microsoft Teams',
  webhook: 'Webhook',
}

export const EVENT_TYPES: {
  value: NotificationEventType
  label: string
  /** Used in the one-line rule summary. */
  noun: string
  description: string
}[] = [
  {
    value: 'applied',
    label: 'Change applied',
    noun: 'applied changes',
    description: 'A recommendation was applied to a workload.',
  },
  {
    value: 'rolled_back',
    label: 'Rolled back',
    noun: 'rollbacks',
    description: 'A change was rolled back after a regression.',
  },
  { value: 'failed', label: 'Apply failed', noun: 'failures', description: 'Applying a change failed.' },
  {
    value: 'blocked',
    label: 'Change blocked',
    noun: 'blocked changes',
    description: 'A change was held back by a guardrail or a window.',
  },
  {
    value: 'readiness_degraded',
    label: 'Cluster readiness dropped',
    noun: 'readiness drops',
    description: 'A cluster fell to a lower readiness state.',
  },
  {
    value: 'automation_paused',
    label: 'Automation paused',
    noun: 'automation pauses',
    description: 'Automation was paused for a cluster or the fleet.',
  },
  {
    value: 'automation_resumed',
    label: 'Automation resumed',
    noun: 'automation resumes',
    description: 'Automation was resumed.',
  },
]
export const EVENT_LABEL = Object.fromEntries(EVENT_TYPES.map((e) => [e.value, e.label])) as Record<
  NotificationEventType,
  string
>

/** Events that carry no namespace: a rule that lists namespaces never matches them. */
export const NAMESPACELESS_EVENTS: NotificationEventType[] = [
  'readiness_degraded',
  'automation_paused',
  'automation_resumed',
]

/** Simplified Slack mark (four rounded bars), drawn in the current text colour. */
function SlackMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.6"
      strokeLinecap="round"
      className={className}
      aria-hidden
    >
      <path d="M9.5 4.5v6M14.5 13.5v6M4.5 14.5h6M13.5 9.5h6" />
    </svg>
  )
}

/** Simplified Microsoft Teams mark (a "T" on a rounded tile). */
function TeamsMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden
    >
      <rect x="3.5" y="3.5" width="17" height="17" rx="4" />
      <path d="M8.5 9h7M12 9v7" />
    </svg>
  )
}

export function ChannelTypeIcon({ type, className }: { type: NotificationChannelType; className?: string }) {
  const Icon = type === 'slack' ? SlackMark : type === 'teams' ? TeamsMark : WebhookIcon
  return <Icon className={className ?? 'size-4'} />
}

const list = (xs: string[]) =>
  xs.length <= 1 ? (xs[0] ?? '') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`

/** "Rollbacks and failures in prod-* / any namespace → platform-alerts" */
export function ruleSummary(
  r: Pick<NotificationRule, 'eventTypes' | 'clusters' | 'namespaces' | 'channelIds'>,
  channels: NotificationChannel[],
) {
  const nouns = EVENT_TYPES.filter((e) => r.eventTypes.includes(e.value)).map((e) => e.noun)
  const what = list(nouns)
  const to = r.channelIds.map((id) => channels.find((c) => c.id === id)?.name ?? 'a deleted channel')
  return `${what.charAt(0).toUpperCase()}${what.slice(1)} in ${r.clusters.length ? r.clusters.join(', ') : 'any cluster'} / ${
    r.namespaces.length ? r.namespaces.join(', ') : 'any namespace'
  } → ${to.join(', ') || 'no channel'}`
}

const DELIVERY: Record<
  NotificationDeliveryStatus,
  { label: string; variant: 'success' | 'warning' | 'destructive' }
> = {
  sent: { label: 'Sent', variant: 'success' },
  pending: { label: 'Queued', variant: 'warning' },
  failed: { label: 'Failed', variant: 'destructive' },
}
export function DeliveryStatusBadge({
  status,
  attempts,
}: {
  status: NotificationDeliveryStatus
  attempts?: number
}) {
  const s = DELIVERY[status]
  return (
    <Badge variant={s.variant} data-status={status}>
      {status === 'pending' && attempts ? 'Retrying' : s.label}
    </Badge>
  )
}

/**
 * Glob patterns as chips. Enter, comma, space or leaving the field adds the typed patterns; Backspace
 * in the empty field removes the last chip. An invalid pattern stays in the field with its error.
 */
export function GlobListInput({
  id,
  value,
  onChange,
  placeholder,
  label,
}: {
  id: string
  value: string[]
  onChange: (v: string[]) => void
  placeholder: string
  label: string
}) {
  const [draft, setDraft] = useState('')
  const [error, setError] = useState<string>()
  function commit(text: string) {
    const tokens = text.split(/[\s,]+/).filter(Boolean)
    if (!tokens.length) return
    const next = [...value]
    const rest: string[] = []
    let err: string | undefined
    for (const t of tokens) {
      const e = validateGlob(t)
      if (e) {
        err ??= `"${t}": ${e}`
        rest.push(t)
      } else if (!next.includes(t)) next.push(t)
    }
    onChange(next)
    setDraft(rest.join(' '))
    setError(err)
  }
  return (
    <div className="flex flex-col gap-1.5">
      <div
        className={cn(
          'flex min-h-8 flex-wrap items-center gap-1.5 rounded-md border border-input bg-card px-1.5 py-1 shadow-xs transition-[color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/30',
          error && 'border-destructive',
        )}
      >
        {value.map((g) => (
          <span
            key={g}
            className="inline-flex items-center gap-1 rounded bg-muted py-0.5 pr-0.5 pl-1.5 font-mono text-xs"
          >
            {g}
            <button
              type="button"
              onClick={() => onChange(value.filter((x) => x !== g))}
              aria-label={`Remove ${g} from ${label}`}
              className="rounded p-0.5 text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              <XIcon className="size-3" />
            </button>
          </span>
        ))}
        <input
          id={id}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            setError(undefined)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ',') {
              e.preventDefault()
              commit(draft)
            } else if (e.key === 'Backspace' && !draft && value.length) onChange(value.slice(0, -1))
          }}
          onBlur={() => commit(draft)}
          placeholder={value.length ? '' : placeholder}
          aria-invalid={!!error}
          autoComplete="off"
          spellCheck={false}
          className="min-w-24 flex-1 bg-transparent px-1 font-mono text-[13px] outline-none placeholder:text-muted-foreground/80"
        />
      </div>
      {error ? (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  )
}

/** A checkbox per channel, for the rules and the digest. */
export function ChannelChecklist({
  channels,
  value,
  onChange,
  idPrefix,
  invalid,
}: {
  channels: NotificationChannel[]
  value: string[]
  onChange: (v: string[]) => void
  idPrefix: string
  invalid?: boolean
}) {
  if (!channels.length)
    return <p className="text-[13px] text-muted-foreground">No channels yet. Add a channel first.</p>
  return (
    <div
      role="group"
      className={cn('flex flex-col rounded-md border bg-card', invalid && 'border-destructive')}
      aria-invalid={invalid}
    >
      {channels.map((c) => (
        <label
          key={c.id}
          htmlFor={`${idPrefix}-${c.id}`}
          className="flex cursor-pointer items-center gap-2.5 border-b px-3 py-2 text-[13px] last:border-b-0 hover:bg-accent/50"
        >
          <Checkbox
            id={`${idPrefix}-${c.id}`}
            checked={value.includes(c.id)}
            onCheckedChange={(on) =>
              onChange(on === true ? [...value, c.id] : value.filter((x) => x !== c.id))
            }
          />
          <ChannelTypeIcon type={c.type} className="size-3.5 text-muted-foreground" />
          <span className="font-mono">{c.name}</span>
          <span className="text-xs text-muted-foreground">{CHANNEL_TYPE_LABEL[c.type]}</span>
          {!c.enabled ? (
            <Badge variant="muted" className="ml-auto">
              disabled
            </Badge>
          ) : null}
        </label>
      ))}
    </div>
  )
}
