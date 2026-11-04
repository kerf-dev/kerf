import {
  CheckIcon,
  CopyIcon,
  GitBranchIcon,
  KeyRoundIcon,
  PauseCircleIcon,
  RefreshCwIcon,
  ServerCogIcon,
  ShieldIcon,
  TriangleAlertIcon,
  UserIcon,
} from 'lucide-react'
import { useState } from 'react'
import type {
  Actor,
  AgentConfigSync,
  ClusterPolicySource,
  Environment,
  PolicyOrigin,
  UserRole,
} from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/form'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'

export function CopyButton({
  value,
  label = 'Copy',
  className,
}: {
  value: string
  label?: string
  className?: string
}) {
  const [done, setDone] = useState(false)
  return (
    <Button
      type="button"
      variant="outline"
      size="xs"
      className={cn('bg-card/80 backdrop-blur', className)}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
        } catch {
          /* clipboard blocked (non-secure origin): the text is selectable */
        }
        setDone(true)
        setTimeout(() => setDone(false), 1500)
      }}
      aria-label={label}
    >
      {done ? <CheckIcon className="text-success" /> : <CopyIcon />}
      {done ? 'Copied' : label}
    </Button>
  )
}

export function CodeBlock({
  code,
  className,
  maxHeight = 360,
  testId,
  copyLabel = 'Copy',
}: {
  code: string
  className?: string
  maxHeight?: number
  testId?: string
  copyLabel?: string
}) {
  return (
    <div className={cn('group relative rounded-lg border bg-muted/40', className)}>
      <pre
        className="overflow-auto py-3 pr-24 pl-3.5 font-mono text-[12px] leading-relaxed whitespace-pre text-foreground/90"
        style={{ maxHeight }}
        data-testid={testId}
      >
        {code}
      </pre>
      <CopyButton value={code} label={copyLabel} className="absolute top-2 right-2" />
    </div>
  )
}

/** A token or password the hub returns exactly once. */
export function SecretOnce({ value, what = 'token' }: { value: string; what?: string }) {
  return (
    <div className="flex flex-col gap-2.5" data-testid="secret-once">
      <Callout tone="warning" icon={KeyRoundIcon} title={`Copy the ${what} now`}>
        This is the only time the hub shows it. It is stored hashed; if it is lost, rotate it.
      </Callout>
      <div className="flex items-center gap-2 rounded-lg border bg-muted/40 py-1.5 pr-1.5 pl-3">
        <code className="min-w-0 flex-1 truncate font-mono text-[13px] select-all" data-testid="secret-value">
          {value}
        </code>
        <CopyButton value={value} />
      </div>
    </div>
  )
}

export function EnvBadge({ env }: { env?: Environment }) {
  if (!env) return null
  return env === 'prod' ? (
    <Badge variant="destructive" className="uppercase tracking-wide text-[10px]">
      prod
    </Badge>
  ) : (
    <Badge variant="muted" className="text-[10px] uppercase tracking-wide">
      non-prod
    </Badge>
  )
}

export function OriginBadge({ origin }: { origin: PolicyOrigin }) {
  return origin === 'git' ? (
    <Tooltip content="Managed in Git. Read-only here; the agent never modifies it.">
      <Badge variant="outline" className="gap-1 font-mono text-[11px]" data-testid="badge-git">
        <GitBranchIcon /> Git
      </Badge>
    </Tooltip>
  ) : (
    <Tooltip content="Managed in the hub and materialised by the agent as a labelled RightsizingPolicy.">
      <Badge variant="default" className="gap-1 font-mono text-[11px]" data-testid="badge-hub">
        <ServerCogIcon /> Hub
      </Badge>
    </Tooltip>
  )
}

export const POLICY_SOURCE_LABEL: Record<ClusterPolicySource, string> = {
  both: 'Hub + Git',
  hub: 'Hub only',
  git: 'Git only',
}

export function RoleBadge({ role }: { role: UserRole | string }) {
  const v = role === 'admin' ? 'default' : role === 'operator' ? 'info' : 'muted'
  return (
    <Badge variant={v} className="gap-1 capitalize">
      {role === 'admin' ? <ShieldIcon /> : <UserIcon />}
      {role}
    </Badge>
  )
}

export function ActorChip({ actor, time }: { actor?: Actor; time?: string }) {
  if (!actor) return <span className="text-muted-foreground">—</span>
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5 text-[13px]">
      <span
        className={cn(
          'flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold uppercase',
          actor.type === 'system' ? 'bg-muted text-muted-foreground' : 'bg-primary/12 text-primary',
        )}
        aria-hidden
      >
        {actor.type === 'system' ? (
          <RefreshCwIcon className="size-3" />
        ) : actor.type === 'token' ? (
          <KeyRoundIcon className="size-3" />
        ) : (
          actor.name.slice(0, 1)
        )}
      </span>
      <span className="truncate font-medium">{actor.type === 'system' ? 'system' : actor.name}</span>
      {time ? (
        <Tooltip content={formatDateTime(time)}>
          <span className="shrink-0 text-muted-foreground">· {relativeTime(time)}</span>
        </Tooltip>
      ) : null}
    </span>
  )
}

/** Whether the agent has applied the config the hub serves (D-022). */
export function SyncBadge({ sync }: { sync?: AgentConfigSync }) {
  if (!sync) return null
  if (!sync.lastConfigPullAt)
    return (
      <Badge variant="muted" className="gap-1">
        Never pulled
      </Badge>
    )
  return sync.inSync ? (
    <Tooltip
      content={`Agent applied config ${sync.configVersion} · last pull ${relativeTime(sync.lastConfigPullAt)}`}
    >
      <Badge variant="success" className="gap-1" data-testid="sync-badge">
        <CheckIcon /> In sync
      </Badge>
    </Tooltip>
  ) : (
    <Tooltip
      content={`Hub serves ${sync.configVersion}, the agent applied ${sync.appliedConfigVersion ?? 'nothing yet'}. It catches up on its next pull.`}
    >
      <Badge variant="warning" className="gap-1" data-testid="sync-badge">
        <RefreshCwIcon className="animate-spin [animation-duration:2.5s]" /> Syncing
      </Badge>
    </Tooltip>
  )
}

export function PausedBadge({ paused, fleet }: { paused?: boolean; fleet?: boolean }) {
  if (!paused) return null
  return (
    <Badge variant="warning" className="gap-1" data-testid="paused-badge">
      <PauseCircleIcon /> {fleet ? 'Fleet paused' : 'Paused'}
    </Badge>
  )
}

/** Read-only notice for users without the role a page's actions need. */
export function ReadOnlyNotice({ need, signedIn }: { need: UserRole; signedIn: boolean }) {
  return (
    <Callout tone="muted" icon={TriangleAlertIcon} className="py-2.5">
      {signedIn
        ? `You can view this page. Changes need the ${need} role.`
        : `You are viewing anonymously. Sign in as ${need === 'admin' ? 'an admin' : `an ${need}`} to make changes.`}
    </Callout>
  )
}
