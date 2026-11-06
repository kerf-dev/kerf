import {
  BellIcon,
  CircleCheckIcon,
  CircleXIcon,
  FlaskConicalIcon,
  Loader2Icon,
  LockIcon,
  PencilIcon,
  PlusIcon,
  RotateCwIcon,
  SendIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import {
  useNotificationChannels,
  useNotificationDeliveries,
  useNotificationDigest,
  useNotificationRules,
  useNotificationStatus,
  useWrite,
  writes,
} from '@/api/admin'
import { useAuth } from '@/api/auth'
import type {
  NotificationChannel,
  NotificationDeliveryStatus,
  NotificationDigest,
  NotificationRule,
  NotificationTestResult,
} from '@/api/types'
import { ActorChip } from '@/components/admin/common'
import {
  CHANNEL_TYPE_LABEL,
  ChannelChecklist,
  ChannelTypeIcon,
  DeliveryStatusBadge,
  EVENT_LABEL,
  ruleSummary,
} from '@/components/admin/notification-common'
import { ChannelDialog, RuleDialog } from '@/components/admin/notification-dialogs'
import { WriteDialog, WriteError } from '@/components/admin/write-dialog'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Callout, Field, NativeSelect, Switch } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toast'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, relativeTime } from '@/lib/format'

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

const FALLBACK_ZONES = [
  'UTC',
  'America/Los_Angeles',
  'America/Denver',
  'America/Chicago',
  'America/New_York',
  'America/Sao_Paulo',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Paris',
  'Europe/Madrid',
  'Europe/Athens',
  'Africa/Johannesburg',
  'Asia/Jerusalem',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Australia/Sydney',
  'Pacific/Auckland',
]

/** "Mon, 09 Nov, 08:00 GMT+1": a moment as the wall clock of `timeZone` shows it. */
function formatInZone(iso: string, timeZone: string) {
  try {
    return new Intl.DateTimeFormat('en-GB', {
      timeZone,
      weekday: 'short',
      day: '2-digit',
      month: 'short',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
      timeZoneName: 'short',
    }).format(Date.parse(iso))
  } catch {
    return formatDateTime(iso)
  }
}

function timeZones(): string[] {
  try {
    const zones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.(
      'timeZone',
    )
    if (zones?.length) return zones.includes('UTC') ? zones : ['UTC', ...zones]
  } catch {
    /* an older engine: use the short list */
  }
  return FALLBACK_ZONES
}

// ---------------------------------------------------------------- channels

function LastDelivery({ c }: { c: NotificationChannel }) {
  const l = c.lastDelivery
  return (
    <div className="flex min-w-0 flex-col gap-1 text-xs" data-testid="last-delivery">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        {l ? (
          <>
            <Badge
              variant={l.status === 'sent' ? 'success' : l.status === 'retrying' ? 'warning' : 'destructive'}
              data-status={l.status}
            >
              {l.status === 'sent' ? (
                <CircleCheckIcon />
              ) : l.status === 'retrying' ? (
                <RotateCwIcon />
              ) : (
                <CircleXIcon />
              )}
              {l.status === 'sent' ? 'Sent' : l.status === 'retrying' ? 'Retrying' : 'Failed'}
            </Badge>
            <Tooltip content={formatDateTime(l.at)}>
              <span className="text-muted-foreground">{relativeTime(l.at)}</span>
            </Tooltip>
          </>
        ) : (
          <span className="text-muted-foreground">No deliveries yet</span>
        )}
        {c.pending > 0 ? (
          <Badge variant="muted" data-testid="pending-count">
            {c.pending} queued
          </Badge>
        ) : null}
        {l?.status !== 'sent' && c.lastSuccessAt ? (
          <span className="text-muted-foreground">last success {relativeTime(c.lastSuccessAt)}</span>
        ) : null}
      </div>
      {l?.error && l.status !== 'sent' ? (
        <p className="font-mono text-[11px] break-words text-destructive" data-testid="last-error">
          {l.error}
        </p>
      ) : null}
    </div>
  )
}

function TestResult({ r }: { r: NotificationTestResult }) {
  return (
    <div role={r.ok ? 'status' : 'alert'} data-testid="test-result" data-ok={r.ok}>
      <Callout
        tone={r.ok ? 'success' : 'destructive'}
        icon={r.ok ? CircleCheckIcon : TriangleAlertIcon}
        title={r.ok ? 'Test message delivered' : 'Test message failed'}
      >
        {r.ok ? (
          <>
            The destination accepted it in {r.latencyMs} ms{r.httpStatus ? ` (HTTP ${r.httpStatus})` : ''}.
          </>
        ) : (
          <>
            <span className="font-mono text-xs break-words" data-testid="test-error">
              {r.error || 'The destination refused the message.'}
            </span>
            <span className="block text-xs">
              {r.httpStatus ? `HTTP ${r.httpStatus} · ` : ''}
              after {r.latencyMs} ms
            </span>
          </>
        )}
      </Callout>
    </div>
  )
}

function ChannelRow({
  c,
  canEdit,
  usedBy,
  onEdit,
  onDelete,
}: {
  c: NotificationChannel
  canEdit: boolean
  usedBy: string[]
  onEdit: () => void
  onDelete: () => void
}) {
  const [result, setResult] = useState<NotificationTestResult>()
  const test = useWrite(() => writes.testNotificationChannel({ id: c.id }))
  const readOnly = c.source === 'config'
  return (
    <li className="border-b last:border-b-0" data-testid={`channel-${c.name}`}>
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 py-4">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            <ChannelTypeIcon type={c.type} className="size-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono font-semibold">{c.name}</span>
              <Badge variant="muted">{CHANNEL_TYPE_LABEL[c.type]}</Badge>
              {readOnly ? (
                <Badge variant="outline" data-testid="source-badge">
                  <LockIcon /> Chart values
                </Badge>
              ) : (
                <Badge variant="outline" data-testid="source-badge">
                  API
                </Badge>
              )}
              {!c.enabled ? <Badge variant="warning">Disabled</Badge> : null}
              {c.secretSet ? <Badge variant="muted">Signed</Badge> : null}
            </div>
            <Tooltip content="The rest of the URL is stored encrypted and never shown.">
              <div
                className="mt-0.5 w-fit max-w-full truncate font-mono text-xs text-muted-foreground"
                data-testid="channel-url"
              >
                {c.url}
              </div>
            </Tooltip>
            <div className="mt-2">
              <LastDelivery c={c} />
            </div>
            {c.updatedBy ? (
              <div className="mt-1.5 flex items-center gap-1 text-xs text-muted-foreground">
                changed by <ActorChip actor={c.updatedBy} time={c.updatedAt} />
              </div>
            ) : null}
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          {canEdit ? (
            <Button
              size="sm"
              variant="outline"
              disabled={test.isPending}
              onClick={() => {
                setResult(undefined)
                test.mutate(undefined, { onSuccess: (r) => setResult(r) })
              }}
              aria-label={`Send test to ${c.name}`}
              data-testid="send-test"
            >
              {test.isPending ? <Loader2Icon className="animate-spin" /> : <FlaskConicalIcon />} Send test
            </Button>
          ) : null}
          {readOnly ? (
            <Tooltip content="Defined in the hub chart values; change it there and upgrade the hub.">
              <span
                className="inline-flex h-8 items-center gap-1.5 px-2 text-xs text-muted-foreground"
                data-testid="read-only-note"
              >
                <LockIcon className="size-3.5" /> Managed by chart values
              </span>
            </Tooltip>
          ) : null}
          {canEdit ? (
            <>
              <Button
                size="sm"
                variant="ghost"
                onClick={onEdit}
                disabled={readOnly}
                aria-label={`Edit ${c.name}`}
              >
                <PencilIcon />
              </Button>
              <Tooltip
                content={
                  readOnly
                    ? null
                    : usedBy.length
                      ? `Still used by ${usedBy.join(' and ')}. Remove it there first.`
                      : null
                }
              >
                <span>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={onDelete}
                    disabled={readOnly || usedBy.length > 0}
                    aria-label={`Delete ${c.name}`}
                    className="text-destructive hover:text-destructive"
                  >
                    <Trash2Icon />
                  </Button>
                </span>
              </Tooltip>
            </>
          ) : null}
        </div>
      </div>
      {test.error ? (
        <div className="px-5 pb-4" role="alert">
          <WriteError error={test.error} />
        </div>
      ) : result ? (
        <div className="px-5 pb-4">
          <TestResult r={result} />
        </div>
      ) : null}
    </li>
  )
}

function ChannelsCard({ canEdit }: { canEdit: boolean }) {
  const q = useNotificationChannels()
  const rules = useNotificationRules()
  const digest = useNotificationDigest()
  const status = useNotificationStatus()
  const [edit, setEdit] = useState<NotificationChannel | 'new' | null>(null)
  const [del, setDel] = useState<NotificationChannel>()
  const remove = useWrite(({ reason }: { reason: string }) =>
    writes.deleteNotificationChannel({ id: del!.id, version: del!.version, reason }),
  )
  const usedBy = (id: string) => [
    ...(rules.data?.items ?? []).filter((r) => r.channelIds.includes(id)).map((r) => `rule ${r.name}`),
    ...(digest.data?.channelIds.includes(id) ? ['the weekly digest'] : []),
  ]
  const noKey = status.data?.secretsKey === false
  return (
    <Card data-testid="channels-card" className="overflow-hidden">
      <CardHeader className="pb-4">
        <div>
          <CardTitle>Channels</CardTitle>
          <CardDescription>
            Slack and Microsoft Teams webhooks, or any HTTPS endpoint. Rules and the digest send to them.
          </CardDescription>
        </div>
        {canEdit ? (
          <Tooltip content={noKey ? 'The hub needs a secrets encryption key first.' : null}>
            <span>
              <Button size="sm" onClick={() => setEdit('new')} disabled={noKey} data-testid="new-channel">
                <PlusIcon /> Add channel
              </Button>
            </span>
          </Tooltip>
        ) : null}
      </CardHeader>
      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <div className="space-y-3 px-5 pb-5">
          <Skeleton className="h-20 rounded-xl" />
          <Skeleton className="h-20 rounded-xl" />
        </div>
      ) : !q.data.items.length ? (
        <EmptyState
          icon={BellIcon}
          title="No channels yet"
          description="Add a Slack, Microsoft Teams or webhook channel to start receiving messages."
        />
      ) : (
        <ul className="border-t" aria-label="Notification channels">
          {q.data.items.map((c) => (
            <ChannelRow
              key={c.id}
              c={c}
              canEdit={canEdit}
              usedBy={usedBy(c.id)}
              onEdit={() => setEdit(c)}
              onDelete={() => setDel(c)}
            />
          ))}
        </ul>
      )}
      <ChannelDialog
        open={!!edit}
        onOpenChange={(o) => !o && setEdit(null)}
        channel={edit && edit !== 'new' ? edit : undefined}
      />
      <WriteDialog
        open={!!del}
        onOpenChange={(o) => !o && setDel(undefined)}
        tone="destructive"
        title={`Delete ${del?.name}`}
        description="Its stored URL and secret are removed, and deliveries still queued for it are dropped."
        confirmLabel="Delete channel"
        onConfirm={(o) => remove.mutateAsync(o)}
        successMessage={`Deleted ${del?.name}`}
        testId="delete-channel-dialog"
      />
    </Card>
  )
}

// ---------------------------------------------------------------- rules

function RulesCard({ canEdit }: { canEdit: boolean }) {
  const rules = useNotificationRules()
  const channels = useNotificationChannels()
  const [edit, setEdit] = useState<NotificationRule | 'new' | null>(null)
  const [del, setDel] = useState<NotificationRule>()
  const remove = useWrite(({ reason }: { reason: string }) =>
    writes.deleteNotificationRule({ id: del!.id, version: del!.version, reason }),
  )
  const chans = channels.data?.items ?? []
  return (
    <Card data-testid="rules-card" className="overflow-hidden">
      <CardHeader className="pb-4">
        <div>
          <CardTitle>Rules</CardTitle>
          <CardDescription>
            Which events go to which channels, filtered by cluster and namespace.
          </CardDescription>
        </div>
        {canEdit ? (
          <Tooltip content={channels.data && !chans.length ? 'Add a channel first.' : null}>
            <span>
              <Button
                size="sm"
                onClick={() => setEdit('new')}
                disabled={!chans.length}
                data-testid="new-rule"
              >
                <PlusIcon /> Add rule
              </Button>
            </span>
          </Tooltip>
        ) : null}
      </CardHeader>
      {rules.isError ? (
        <ErrorState error={rules.error} onRetry={() => rules.refetch()} />
      ) : !rules.data ? (
        <div className="space-y-3 px-5 pb-5">
          <Skeleton className="h-14 rounded-xl" />
          <Skeleton className="h-14 rounded-xl" />
        </div>
      ) : !rules.data.items.length ? (
        <EmptyState
          icon={BellIcon}
          title="No rules yet"
          description="Without a rule, only the weekly digest and test messages are sent."
        />
      ) : (
        <ul className="border-t" aria-label="Notification rules">
          {rules.data.items.map((r) => (
            <li
              key={r.id}
              className="flex flex-wrap items-center justify-between gap-3 border-b px-5 py-3.5 last:border-b-0"
              data-testid={`rule-${r.name}`}
            >
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-semibold">{r.name}</span>
                  {!r.enabled ? <Badge variant="warning">Disabled</Badge> : null}
                </div>
                <p
                  className="mt-0.5 text-[13px] text-pretty text-muted-foreground"
                  data-testid="rule-summary"
                >
                  {ruleSummary(r, chans)}
                </p>
              </div>
              {canEdit ? (
                <div className="flex items-center gap-1.5">
                  <Button size="sm" variant="ghost" onClick={() => setEdit(r)} aria-label={`Edit ${r.name}`}>
                    <PencilIcon />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setDel(r)}
                    aria-label={`Delete ${r.name}`}
                    className="text-destructive hover:text-destructive"
                  >
                    <Trash2Icon />
                  </Button>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      <RuleDialog
        open={!!edit}
        onOpenChange={(o) => !o && setEdit(null)}
        rule={edit && edit !== 'new' ? edit : undefined}
      />
      <WriteDialog
        open={!!del}
        onOpenChange={(o) => !o && setDel(undefined)}
        tone="destructive"
        title={`Delete ${del?.name}`}
        description="Events it matched stop being sent to its channels unless another rule matches them."
        confirmLabel="Delete rule"
        onConfirm={(o) => remove.mutateAsync(o)}
        successMessage={`Deleted ${del?.name}`}
        testId="delete-rule-dialog"
      />
    </Card>
  )
}

// ---------------------------------------------------------------- digest

interface DigestForm {
  enabled: boolean
  weekday: number
  time: string
  timezone: string
  channelIds: string[]
}

const toDigestForm = (d: NotificationDigest): DigestForm => ({
  enabled: d.enabled,
  weekday: d.weekday,
  time: d.time,
  timezone: d.timezone,
  channelIds: d.channelIds,
})

function DigestCard({ canEdit }: { canEdit: boolean }) {
  const id = useId()
  const q = useNotificationDigest()
  const channels = useNotificationChannels()
  const [f, setF] = useState<DigestForm>()
  const [confirm, setConfirm] = useState(false)
  const [touched, setTouched] = useState(false)
  const digest = q.data
  useEffect(() => {
    if (digest) setF(toDigestForm(digest))
  }, [digest])
  const zones = useMemo(() => {
    const z = timeZones()
    return f && !z.includes(f.timezone) ? [f.timezone, ...z] : z
  }, [f])
  const save = useWrite(({ reason }: { reason: string }) =>
    writes.putNotificationDigest({ version: digest!.version, body: { ...f!, reason } }),
  )
  const send = useWrite(() => writes.sendNotificationDigest())
  const set = (patch: Partial<DigestForm>) => setF((x) => x && { ...x, ...patch })

  const errors = {
    time: /^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(f?.time ?? '') ? undefined : 'A time as HH:MM.',
    channelIds:
      f?.enabled && !f.channelIds.length ? 'Pick at least one channel, or turn the digest off.' : undefined,
  }
  const invalid = Object.values(errors).some(Boolean)
  const dirty = !!f && !!digest && JSON.stringify(f) !== JSON.stringify(toDigestForm(digest))
  const nothingToSend = !digest?.channelIds.length
  return (
    <Card data-testid="digest-card">
      <CardHeader>
        <div>
          <CardTitle>Weekly digest</CardTitle>
          <CardDescription>
            A weekly summary of what Kerf changed and saved, sent to the channels you pick.
          </CardDescription>
          <p className="mt-1 text-xs text-muted-foreground" data-testid="digest-teams-note">
            When teams exist, the digest also includes the savings by team. There is nothing to set for it.
          </p>
        </div>
        {canEdit && digest ? (
          <div className="flex flex-col items-end gap-2">
            <Tooltip
              content={
                nothingToSend
                  ? 'Save the digest with at least one channel first.'
                  : dirty
                    ? 'Sends the saved settings; your unsaved changes are not used.'
                    : null
              }
            >
              <span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={send.isPending || nothingToSend}
                  onClick={() =>
                    send.mutate(undefined, {
                      onSuccess: (r) =>
                        toast.success(`Digest queued for ${r.queued} channel${r.queued === 1 ? '' : 's'}`),
                    })
                  }
                  data-testid="send-digest"
                >
                  {send.isPending ? <Loader2Icon className="animate-spin" /> : <SendIcon />} Send digest now
                </Button>
              </span>
            </Tooltip>
          </div>
        ) : null}
      </CardHeader>
      <CardContent>
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} compact />
        ) : !digest || !f ? (
          <Skeleton className="h-40 rounded-xl" />
        ) : (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              setTouched(true)
              if (!invalid) setConfirm(true)
            }}
          >
            {send.error ? <WriteError error={send.error} /> : null}
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-[13px]">
              <label className="flex items-center gap-2 font-medium">
                <Switch
                  checked={f.enabled}
                  onCheckedChange={(v) => set({ enabled: v })}
                  disabled={!canEdit}
                  aria-label="Send the weekly digest"
                  data-testid="digest-enabled"
                />
                {f.enabled ? 'Digest is on' : 'Digest is off'}
              </label>
              <span className="text-muted-foreground" data-testid="digest-schedule">
                {digest.nextSendAt ? (
                  <Tooltip content={`${formatDateTime(digest.nextSendAt)} in your time zone`}>
                    <span>
                      Next: {formatInZone(digest.nextSendAt, digest.timezone)} (
                      {relativeTime(digest.nextSendAt)})
                    </span>
                  </Tooltip>
                ) : (
                  'Not scheduled while it is off'
                )}
                {' · '}
                {digest.lastSentAt ? (
                  <Tooltip content={formatDateTime(digest.lastSentAt)}>
                    <span>Last: {relativeTime(digest.lastSentAt)}</span>
                  </Tooltip>
                ) : (
                  'Not sent yet'
                )}
              </span>
              {digest.updatedBy ? (
                <span className="flex items-center gap-1 text-xs text-muted-foreground">
                  changed by <ActorChip actor={digest.updatedBy} time={digest.updatedAt} />
                </span>
              ) : null}
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Day" htmlFor={`${id}-day`}>
                <NativeSelect
                  id={`${id}-day`}
                  value={f.weekday}
                  disabled={!canEdit}
                  onChange={(e) => set({ weekday: Number(e.target.value) })}
                >
                  {WEEKDAYS.map((d, i) => (
                    <option key={d} value={i}>
                      {d}
                    </option>
                  ))}
                </NativeSelect>
              </Field>
              <Field label="Time" htmlFor={`${id}-time`} error={touched ? errors.time : undefined}>
                <Input
                  id={`${id}-time`}
                  type="time"
                  value={f.time}
                  disabled={!canEdit}
                  onChange={(e) => set({ time: e.target.value })}
                  className="font-mono"
                  aria-invalid={touched && !!errors.time}
                />
              </Field>
              <Field label="Time zone" htmlFor={`${id}-tz`}>
                <NativeSelect
                  id={`${id}-tz`}
                  value={f.timezone}
                  disabled={!canEdit}
                  onChange={(e) => set({ timezone: e.target.value })}
                >
                  {zones.map((z) => (
                    <option key={z} value={z}>
                      {z}
                    </option>
                  ))}
                </NativeSelect>
              </Field>
            </div>
            <Field label="Send to" error={touched ? errors.channelIds : undefined}>
              {canEdit ? (
                <ChannelChecklist
                  channels={channels.data?.items ?? []}
                  value={f.channelIds}
                  onChange={(channelIds) => set({ channelIds })}
                  idPrefix={`${id}-ch`}
                  invalid={touched && !!errors.channelIds}
                />
              ) : (
                <p className="text-[13px]" data-testid="digest-channels">
                  {f.channelIds.length
                    ? f.channelIds
                        .map(
                          (cid) =>
                            channels.data?.items.find((c) => c.id === cid)?.name ?? 'a deleted channel',
                        )
                        .join(', ')
                    : 'No channel'}
                </p>
              )}
            </Field>
            {canEdit ? (
              <div className="flex items-center gap-2">
                <Button type="submit" size="sm" disabled={!dirty} data-testid="save-digest">
                  Save digest
                </Button>
                {dirty ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setF(toDigestForm(digest))
                      setTouched(false)
                    }}
                  >
                    Discard changes
                  </Button>
                ) : null}
              </div>
            ) : null}
          </form>
        )}
      </CardContent>
      <WriteDialog
        open={confirm}
        onOpenChange={setConfirm}
        testId="digest-dialog"
        title="Save the weekly digest"
        description={
          f
            ? f.enabled
              ? `Sent on ${WEEKDAYS[f.weekday]} at ${f.time} (${f.timezone}).`
              : 'The digest will be turned off.'
            : undefined
        }
        confirmLabel="Save digest"
        onConfirm={(o) => save.mutateAsync(o)}
        successMessage="Digest saved"
      />
    </Card>
  )
}

// ---------------------------------------------------------------- deliveries

const KIND_LABEL = { event: 'Event', digest: 'Digest', test: 'Test' } as const

function DeliveriesCard() {
  const channels = useNotificationChannels()
  const [channelId, setChannelId] = useState('')
  const [status, setStatus] = useState<NotificationDeliveryStatus | ''>('')
  const q = useNotificationDeliveries({
    ...(channelId ? { channelId } : {}),
    ...(status ? { status } : {}),
  })
  const rows = q.data?.pages.flatMap((p) => p.items) ?? []
  const filtered = !!channelId || !!status
  return (
    <Card data-testid="deliveries-card" className="overflow-hidden">
      <CardHeader className="pb-4">
        <div>
          <CardTitle>Recent deliveries</CardTitle>
          <CardDescription>
            Newest first, kept for 30 days. A failing delivery is retried with growing pauses for an hour
            before it is marked failed.
          </CardDescription>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect
            className="w-44"
            aria-label="Filter by channel"
            value={channelId}
            onChange={(e) => setChannelId(e.target.value)}
          >
            <option value="">All channels</option>
            {(channels.data?.items ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </NativeSelect>
          <NativeSelect
            className="w-36"
            aria-label="Filter by status"
            value={status}
            onChange={(e) => setStatus(e.target.value as NotificationDeliveryStatus | '')}
          >
            <option value="">Any status</option>
            <option value="sent">Sent</option>
            <option value="pending">Queued</option>
            <option value="failed">Failed</option>
          </NativeSelect>
        </div>
      </CardHeader>
      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <div className="space-y-2 px-5 pb-5">
          <Skeleton className="h-10 rounded-lg" />
          <Skeleton className="h-10 rounded-lg" />
          <Skeleton className="h-10 rounded-lg" />
        </div>
      ) : !rows.length ? (
        <EmptyState
          icon={BellIcon}
          title={filtered ? 'No deliveries match the filter' : 'No deliveries yet'}
          description={
            filtered
              ? 'Try another channel or status.'
              : 'Messages show up here once an event matches a rule, the digest runs or you send a test.'
          }
        />
      ) : (
        <div className="overflow-x-auto border-t">
          <table className="w-full min-w-[680px] text-[13px]" data-testid="deliveries-table">
            <thead className="bg-muted/30 text-xs text-muted-foreground">
              <tr>
                <th scope="col" className="px-5 py-2 text-left font-medium">
                  When
                </th>
                <th scope="col" className="px-2 py-2 text-left font-medium">
                  Channel
                </th>
                <th scope="col" className="px-2 py-2 text-left font-medium">
                  What
                </th>
                <th scope="col" className="px-2 py-2 text-left font-medium">
                  Status and attempts
                </th>
                <th scope="col" className="px-5 py-2 text-left font-medium">
                  Next attempt / last error
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => (
                <tr key={d.id} className="border-t align-top" data-testid="delivery-row">
                  <td className="px-5 py-2.5 whitespace-nowrap text-muted-foreground">
                    <Tooltip content={formatDateTime(d.createdAt)}>
                      <span>{relativeTime(d.createdAt)}</span>
                    </Tooltip>
                  </td>
                  <td className="px-2 py-2.5 font-mono">{d.channelName}</td>
                  <td className="max-w-md px-2 py-2.5">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge variant="muted">{KIND_LABEL[d.kind]}</Badge>
                      {d.eventType ? (
                        <span className="text-xs font-medium">{EVENT_LABEL[d.eventType]}</span>
                      ) : null}
                    </div>
                    <p className="mt-1 text-pretty text-muted-foreground">{d.summary}</p>
                  </td>
                  <td className="px-2 py-2.5">
                    <DeliveryStatusBadge status={d.status} attempts={d.attempts} />
                    <div className="mt-1 text-xs whitespace-nowrap text-muted-foreground">
                      {d.attempts} attempt{d.attempts === 1 ? '' : 's'}
                    </div>
                  </td>
                  <td className="max-w-xs px-5 py-2.5 text-xs">
                    {d.status === 'pending' && d.nextAttemptAt ? (
                      <div className="text-muted-foreground">
                        {d.attempts ? 'Retry' : 'Send'}{' '}
                        <Tooltip content={formatDateTime(d.nextAttemptAt)}>
                          <span>{relativeTime(d.nextAttemptAt)}</span>
                        </Tooltip>
                      </div>
                    ) : null}
                    {d.lastError && d.status !== 'sent' ? (
                      <div className="font-mono text-[11px] break-words text-destructive">{d.lastError}</div>
                    ) : null}
                    {d.status === 'sent' && d.sentAt ? (
                      <span className="text-muted-foreground">Delivered {relativeTime(d.sentAt)}</span>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage ? (
        <div className="flex justify-center border-t py-3">
          <Button
            size="sm"
            variant="outline"
            onClick={() => q.fetchNextPage()}
            disabled={q.isFetchingNextPage}
            data-testid="load-more"
          >
            {q.isFetchingNextPage ? <Loader2Icon className="animate-spin" /> : null} Load more
          </Button>
        </div>
      ) : null}
    </Card>
  )
}

// ---------------------------------------------------------------- page

export default function NotificationsPage() {
  const auth = useAuth()
  const status = useNotificationStatus()
  const canEdit = auth.can('admin')
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Notifications"
        description="Send rollbacks, failures and a weekly digest to Slack, Microsoft Teams or any webhook."
      />
      {status.data && !status.data.secretsKey ? (
        <Callout tone="warning" icon={TriangleAlertIcon} title="Channels cannot be created yet">
          Webhook URLs are stored encrypted, and this hub has no secrets encryption key. Set one in the hub
          chart values; until then no channel can be added.
        </Callout>
      ) : null}
      {status.data && !status.data.links ? (
        <Callout tone="info" title="Messages will not link to this UI">
          Set <span className="font-mono text-xs">notifications.baseURL</span> in the hub chart values to the
          address people open Kerf at, and messages get a link to the workload or cluster.
        </Callout>
      ) : null}
      {!canEdit ? (
        <Callout tone="muted" icon={LockIcon}>
          Changing notifications needs the admin role. You can see everything here.
        </Callout>
      ) : null}
      <ChannelsCard canEdit={canEdit} />
      <RulesCard canEdit={canEdit} />
      <DigestCard canEdit={canEdit} />
      <DeliveriesCard />
    </div>
  )
}
