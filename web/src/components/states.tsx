import { AlertOctagonIcon, InboxIcon, RefreshCwIcon } from 'lucide-react'
import { ApiError } from '@/api/client'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export function PageHeader({
  title,
  description,
  actions,
  eyebrow,
}: {
  title: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  eyebrow?: React.ReactNode
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
      <div className="min-w-0">
        {eyebrow ? <div className="mb-1.5 text-xs font-medium text-muted-foreground">{eyebrow}</div> : null}
        <h1 className="text-2xl font-semibold tracking-tight text-balance">{title}</h1>
        {description ? <div className="mt-1 text-sm text-muted-foreground">{description}</div> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  )
}

export function EmptyState({
  icon: Icon = InboxIcon,
  title,
  description,
  action,
  className,
}: {
  icon?: typeof InboxIcon
  title: string
  description?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex flex-col items-center justify-center px-6 py-12 text-center', className)}>
      <div className="relative mb-4">
        <div className="absolute inset-0 -m-3 rounded-full bg-primary/6 blur-md" />
        <div className="relative flex size-11 items-center justify-center rounded-xl border bg-card shadow-sm">
          <Icon className="size-5 text-muted-foreground" />
        </div>
      </div>
      <h3 className="text-sm font-semibold">{title}</h3>
      {description ? (
        <p className="mt-1 max-w-sm text-[13px] text-pretty text-muted-foreground">{description}</p>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  )
}

export function ErrorState({
  error,
  onRetry,
  className,
  compact,
}: {
  error: unknown
  onRetry?: () => void
  className?: string
  compact?: boolean
}) {
  const e = error instanceof ApiError ? error : undefined
  const title =
    e?.status === 401 || e?.status === 403
      ? 'Not authorized to read from the hub'
      : e?.status === 404
        ? 'Not found'
        : e?.status === 0
          ? 'Cannot reach the hub'
          : e && e.status >= 502 && e.status <= 504
            ? 'The hub is unavailable'
            : e && e.status >= 500
              ? 'The hub returned an error'
              : 'Something went wrong'
  const message = error instanceof Error ? error.message : String(error)
  // Server-side failures are usually transient (a hub restart or rollout): say so, since the
  // generic "Internal error." from the hub gives the reader nothing to act on.
  const hint =
    e && (e.status >= 500 || e.status === 0)
      ? 'This is usually temporary, for example while the hub restarts. Try again in a moment.'
      : undefined
  return (
    <div
      role="alert"
      className={cn(
        'flex flex-col items-center justify-center px-6 text-center',
        compact ? 'py-6' : 'py-12',
        className,
      )}
    >
      <div className="mb-3 flex size-10 items-center justify-center rounded-xl bg-destructive/10">
        <AlertOctagonIcon className="size-5 text-destructive" />
      </div>
      <h3 className="text-sm font-semibold">{title}</h3>
      <p className="mt-1 max-w-md text-[13px] text-muted-foreground">{message}</p>
      {hint && !compact ? <p className="mt-1 max-w-md text-[13px] text-muted-foreground">{hint}</p> : null}
      {e?.problem?.code || e?.status ? (
        <p className="mt-2 font-mono text-[11px] text-muted-foreground/80">
          {e.status ? `HTTP ${e.status}` : ''}
          {e.problem?.code ? ` · ${e.problem.code}` : ''}
        </p>
      ) : null}
      {onRetry ? (
        <Button variant="outline" size="sm" className="mt-4" onClick={onRetry}>
          <RefreshCwIcon /> Try again
        </Button>
      ) : null}
    </div>
  )
}
