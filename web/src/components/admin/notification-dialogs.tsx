import { InfoIcon, TriangleAlertIcon } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useNotificationChannels, useNotificationStatus, useWrite, writes } from '@/api/admin'
import type {
  NotificationChannel,
  NotificationChannelInput,
  NotificationChannelType,
  NotificationEventType,
  NotificationRule,
  NotificationRuleInput,
} from '@/api/types'
import { Callout, Checkbox, Field, NativeSelect } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  CHANNEL_TYPE_LABEL,
  ChannelChecklist,
  EVENT_TYPES,
  GlobListInput,
  NAMESPACELESS_EVENTS,
} from './notification-common'
import { WriteDialog } from './write-dialog'

const NAME = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/

const URL_PLACEHOLDER: Record<NotificationChannelType, string> = {
  slack: 'https://hooks.slack.com/services/T0000/B0000/XXXXXXXX',
  teams: 'https://prod-00.westeurope.logic.azure.com:443/workflows/…',
  webhook: 'https://example.com/hooks/kerf',
}
const URL_HINT: Record<NotificationChannelType, string> = {
  slack: 'A Slack incoming webhook.',
  teams: 'The URL of a Microsoft Teams Workflows webhook.',
  webhook: 'Kerf posts a JSON document to this URL.',
}

interface ChannelForm {
  name: string
  type: NotificationChannelType
  url: string
  secret: string
  clearSecret: boolean
  enabled: boolean
}

const toChannelForm = (c?: NotificationChannel): ChannelForm => ({
  name: c?.name ?? '',
  type: c?.type ?? 'slack',
  url: '',
  secret: '',
  clearSecret: false,
  enabled: c?.enabled ?? true,
})

/** Create or edit a notification channel. The URL and the signing secret are write-only. */
export function ChannelDialog({
  open,
  onOpenChange,
  channel,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  channel?: NotificationChannel
}) {
  const id = useId()
  const status = useNotificationStatus()
  const [f, setF] = useState<ChannelForm>(toChannelForm(channel))
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (open) {
      setF(toChannelForm(channel))
      setTouched(false)
    }
  }, [open, channel])
  const set = (patch: Partial<ChannelForm>) => setF((x) => ({ ...x, ...patch }))

  const errors: Record<string, string | undefined> = {
    name: !NAME.test(f.name) ? 'Lowercase letters, digits and "-", up to 63 characters.' : undefined,
    url: (() => {
      if (!f.url) return channel ? undefined : 'Required.'
      try {
        const u = new URL(f.url)
        return u.protocol === 'https:' || u.protocol === 'http:' ? undefined : 'Use an https URL.'
      } catch {
        return `A full URL, e.g. ${URL_PLACEHOLDER[f.type]}`
      }
    })(),
    secret: f.secret && f.secret.length < 16 ? 'At least 16 characters.' : undefined,
  }
  const invalid = Object.values(errors).some(Boolean)
  const err = (k: string) => (touched ? errors[k] : undefined)
  const noKey = status.data?.secretsKey === false
  const body = (reason: string): NotificationChannelInput => ({
    name: f.name,
    type: f.type,
    ...(f.url ? { url: f.url } : {}),
    ...(f.type === 'webhook' && f.secret && !f.clearSecret ? { secret: f.secret } : {}),
    ...(f.type === 'webhook' && f.clearSecret ? { clearSecret: true } : {}),
    enabled: f.enabled,
    reason,
  })
  const write = useWrite(({ reason }: { reason: string }) =>
    channel
      ? writes.updateNotificationChannel({ id: channel.id, version: channel.version, body: body(reason) })
      : writes.createNotificationChannel(body(reason)),
  )

  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      testId="channel-dialog"
      title={channel ? `Edit ${channel.name}` : 'New notification channel'}
      description="Where Kerf posts messages: a Slack or Microsoft Teams webhook, or any HTTPS endpoint."
      confirmLabel={channel ? 'Save channel' : 'Create channel'}
      confirmDisabled={!channel && noKey}
      onSubmitAttempt={() => setTouched(true)}
      reasonAutoFocus={false}
      onConfirm={async (o) => {
        setTouched(true)
        if (invalid) throw new Error('Fix the highlighted fields first.')
        return write.mutateAsync(o)
      }}
      successMessage={channel ? 'Channel saved' : 'Channel created'}
    >
      {noKey && !channel ? (
        <Callout tone="warning" icon={TriangleAlertIcon} title="Channels cannot be created yet">
          Webhook URLs are stored encrypted, and this hub has no secrets encryption key. Configure one in the
          hub chart values, then come back.
        </Callout>
      ) : null}
      <div className="grid gap-3 sm:grid-cols-2" onBlur={() => setTouched(true)}>
        <Field label="Name" htmlFor={`${id}-name`} error={err('name')}>
          <Input
            id={`${id}-name`}
            value={f.name}
            onChange={(e) => set({ name: e.target.value })}
            className="font-mono"
            placeholder="platform-alerts"
            autoComplete="off"
            aria-invalid={!!err('name')}
          />
        </Field>
        <Field
          label="Type"
          htmlFor={`${id}-type`}
          hint={channel ? 'The type cannot be changed; create a new channel instead.' : undefined}
        >
          <NativeSelect
            id={`${id}-type`}
            value={f.type}
            disabled={!!channel}
            onChange={(e) =>
              set({ type: e.target.value as NotificationChannelType, secret: '', clearSecret: false })
            }
          >
            {Object.entries(CHANNEL_TYPE_LABEL).map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <Field
          label="Webhook URL"
          htmlFor={`${id}-url`}
          className="sm:col-span-2"
          error={err('url')}
          hint={
            channel
              ? `Stored encrypted (${channel.url}…). Leave empty to keep it, or paste a new URL to replace it.`
              : `${URL_HINT[f.type]} Stored encrypted and never shown again.`
          }
        >
          <Input
            id={`${id}-url`}
            value={f.url}
            onChange={(e) => set({ url: e.target.value })}
            className="font-mono text-[13px]"
            placeholder={channel ? '••••••••' : URL_PLACEHOLDER[f.type]}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={!!err('url')}
          />
        </Field>
        {f.type === 'webhook' ? (
          <>
            <Field
              label="Signing secret"
              htmlFor={`${id}-secret`}
              optional
              className="sm:col-span-2"
              error={err('secret')}
              hint={
                channel?.secretSet
                  ? 'A secret is stored. Leave empty to keep it, or type a new one to replace it.'
                  : 'Each request is signed with HMAC-SHA256 so the receiver can verify it. At least 16 characters.'
              }
            >
              <Input
                id={`${id}-secret`}
                type="password"
                autoComplete="new-password"
                value={f.secret}
                disabled={f.clearSecret}
                onChange={(e) => set({ secret: e.target.value })}
                placeholder={channel?.secretSet ? '••••••••' : ''}
                aria-invalid={!!err('secret')}
              />
            </Field>
            {channel?.secretSet ? (
              <label className="flex items-center gap-2 text-[13px] sm:col-span-2">
                <Checkbox
                  checked={f.clearSecret}
                  onCheckedChange={(v) =>
                    set({ clearSecret: v === true, secret: v === true ? '' : f.secret })
                  }
                />
                Clear the stored signing secret
              </label>
            ) : null}
          </>
        ) : null}
        <label className="flex items-center gap-2 text-[13px] sm:col-span-2">
          <Checkbox checked={f.enabled} onCheckedChange={(v) => set({ enabled: v === true })} />
          Enabled
          <span className="text-muted-foreground">(a disabled channel receives nothing)</span>
        </label>
      </div>
    </WriteDialog>
  )
}

interface RuleForm {
  name: string
  enabled: boolean
  eventTypes: NotificationEventType[]
  clusters: string[]
  namespaces: string[]
  channelIds: string[]
}

const toRuleForm = (r?: NotificationRule): RuleForm => ({
  name: r?.name ?? '',
  enabled: r?.enabled ?? true,
  eventTypes: r?.eventTypes ?? [],
  clusters: r?.clusters ?? [],
  namespaces: r?.namespaces ?? [],
  channelIds: r?.channelIds ?? [],
})

/** Create or edit a notification rule: which events, from where, to which channels. */
export function RuleDialog({
  open,
  onOpenChange,
  rule,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  rule?: NotificationRule
}) {
  const id = useId()
  const channels = useNotificationChannels()
  const [f, setF] = useState<RuleForm>(toRuleForm(rule))
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (open) {
      setF(toRuleForm(rule))
      setTouched(false)
    }
  }, [open, rule])
  const set = (patch: Partial<RuleForm>) => setF((x) => ({ ...x, ...patch }))
  const errors: Record<string, string | undefined> = {
    name: !f.name.trim() ? 'Required.' : f.name.length > 100 ? 'At most 100 characters.' : undefined,
    eventTypes: f.eventTypes.length ? undefined : 'Pick at least one event.',
    channelIds: f.channelIds.length
      ? undefined
      : 'Pick at least one channel; a rule without one sends nothing.',
  }
  const invalid = Object.values(errors).some(Boolean)
  const err = (k: string) => (touched ? errors[k] : undefined)
  const ignoredEvents = f.namespaces.length
    ? f.eventTypes.filter((e) => NAMESPACELESS_EVENTS.includes(e))
    : []
  const body = (reason: string): NotificationRuleInput => ({
    name: f.name.trim(),
    enabled: f.enabled,
    // Keep the order of the list above, whatever order the boxes were ticked in.
    eventTypes: EVENT_TYPES.map((e) => e.value).filter((v) => f.eventTypes.includes(v)),
    clusters: f.clusters,
    namespaces: f.namespaces,
    channelIds: f.channelIds,
    reason,
  })
  const write = useWrite(({ reason }: { reason: string }) =>
    rule
      ? writes.updateNotificationRule({ id: rule.id, version: rule.version, body: body(reason) })
      : writes.createNotificationRule(body(reason)),
  )

  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      wide
      testId="rule-dialog"
      title={rule ? `Edit ${rule.name}` : 'New notification rule'}
      description="A rule sends the events you pick, from the clusters and namespaces you match, to its channels."
      confirmLabel={rule ? 'Save rule' : 'Create rule'}
      onSubmitAttempt={() => setTouched(true)}
      reasonAutoFocus={false}
      onConfirm={async (o) => {
        setTouched(true)
        if (invalid) throw new Error('Fix the highlighted fields first.')
        return write.mutateAsync(o)
      }}
      successMessage={rule ? 'Rule saved' : 'Rule created'}
    >
      <div className="flex flex-col gap-4">
        <div className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
          <Field label="Name" htmlFor={`${id}-name`} error={err('name')}>
            <Input
              id={`${id}-name`}
              value={f.name}
              onChange={(e) => set({ name: e.target.value })}
              placeholder="Rollbacks and failures in prod"
              autoComplete="off"
              aria-invalid={!!err('name')}
            />
          </Field>
          <label className="flex h-8 items-center gap-2 text-[13px]">
            <Checkbox checked={f.enabled} onCheckedChange={(v) => set({ enabled: v === true })} />
            Enabled
          </label>
        </div>
        <Field label="Events" error={err('eventTypes')}>
          <div
            role="group"
            aria-label="Events"
            className="grid gap-x-4 gap-y-1 rounded-md border bg-card p-2 sm:grid-cols-2"
          >
            {EVENT_TYPES.map((e) => (
              <label
                key={e.value}
                htmlFor={`${id}-ev-${e.value}`}
                className="flex cursor-pointer items-start gap-2.5 rounded px-1.5 py-1.5 text-[13px] hover:bg-accent/50"
              >
                <Checkbox
                  id={`${id}-ev-${e.value}`}
                  className="mt-0.5"
                  checked={f.eventTypes.includes(e.value)}
                  onCheckedChange={(on) =>
                    set({
                      eventTypes:
                        on === true ? [...f.eventTypes, e.value] : f.eventTypes.filter((x) => x !== e.value),
                    })
                  }
                />
                <span>
                  <span className="font-medium">{e.label}</span>
                  <span className="block text-xs text-muted-foreground">{e.description}</span>
                </span>
              </label>
            ))}
          </div>
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            label="Clusters"
            htmlFor={`${id}-clusters`}
            hint="Glob patterns such as prod-*. Press Enter or comma to add. Empty: every cluster."
          >
            <GlobListInput
              id={`${id}-clusters`}
              label="clusters"
              value={f.clusters}
              onChange={(clusters) => set({ clusters })}
              placeholder="all clusters"
            />
          </Field>
          <Field
            label="Namespaces"
            htmlFor={`${id}-namespaces`}
            hint="Glob patterns such as payments or team-*. Empty: every namespace."
          >
            <GlobListInput
              id={`${id}-namespaces`}
              label="namespaces"
              value={f.namespaces}
              onChange={(namespaces) => set({ namespaces })}
              placeholder="all namespaces"
            />
          </Field>
        </div>
        {ignoredEvents.length ? (
          <Callout tone="info" icon={InfoIcon} title="Some events have no namespace">
            Readiness and automation events do not belong to a namespace, so a rule that lists namespaces
            never sends them. Clear the namespaces to receive them.
          </Callout>
        ) : null}
        <Field label="Send to" error={err('channelIds')}>
          <ChannelChecklist
            channels={channels.data?.items ?? []}
            value={f.channelIds}
            onChange={(channelIds) => set({ channelIds })}
            idPrefix={`${id}-ch`}
            invalid={!!err('channelIds')}
          />
        </Field>
      </div>
    </WriteDialog>
  )
}
