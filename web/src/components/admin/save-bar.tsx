import { useQuery } from '@tanstack/react-query'
import { Loader2Icon, RotateCcwIcon } from 'lucide-react'
import { useEffect } from 'react'
import { useBlocker } from 'react-router'
import { writes } from '@/api/admin'
import type { PolicyChange } from '@/api/types'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/** Sticky bar for forms with unsaved changes; also guards navigation away from them. */
export function SaveBar({
  dirty,
  count,
  errors,
  onDiscard,
  onSave,
  saving,
  saveLabel = 'Review and save',
  disabled,
}: {
  dirty: boolean
  count?: number
  errors?: number
  onDiscard: () => void
  onSave: () => void
  saving?: boolean
  saveLabel?: string
  disabled?: boolean
}) {
  useUnsavedGuard(dirty)
  return (
    <div
      className={cn(
        'sticky bottom-4 z-20 mt-6 flex items-center justify-between gap-3 rounded-xl border bg-popover/95 px-4 py-2.5 shadow-lg backdrop-blur transition-all',
        dirty ? 'translate-y-0 opacity-100' : 'pointer-events-none translate-y-2 opacity-0',
      )}
      data-testid="save-bar"
      aria-hidden={!dirty}
    >
      <div className="text-[13px]">
        <span className="font-medium">Unsaved changes</span>
        {count ? (
          <span className="text-muted-foreground">
            {' '}
            · {count} field{count === 1 ? '' : 's'}
          </span>
        ) : null}
        {errors ? <span className="text-destructive"> · {errors} to fix</span> : null}
      </div>
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="sm" onClick={onDiscard} disabled={saving} tabIndex={dirty ? 0 : -1}>
          <RotateCcwIcon /> Discard
        </Button>
        <Button
          size="sm"
          onClick={onSave}
          disabled={saving || disabled || !!errors}
          tabIndex={dirty ? 0 : -1}
          data-testid="save"
        >
          {saving ? <Loader2Icon className="animate-spin" /> : null}
          {saveLabel}
        </Button>
      </div>
    </div>
  )
}

/**
 * Asks before leaving a page with unsaved edits (in-app navigation and tab close). Navigate with
 * `state: { afterSave: true }` to leave right after a successful save.
 */
export function useUnsavedGuard(dirty: boolean) {
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty &&
      currentLocation.pathname !== nextLocation.pathname &&
      !(nextLocation.state as { afterSave?: boolean } | null)?.afterSave,
  )
  useEffect(() => {
    if (blocker.state === 'blocked') {
      if (window.confirm('You have unsaved changes. Leave this page and discard them?')) blocker.proceed()
      else blocker.reset()
    }
  }, [blocker])
  useEffect(() => {
    if (!dirty) return
    const on = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', on)
    return () => window.removeEventListener('beforeunload', on)
  }, [dirty])
}

/** Runs the impact dry-run (`POST /v1/admin/preview`) for a set of changes while `enabled`. */
export function usePreview(changes: PolicyChange[] | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ['preview', changes],
    queryFn: () => writes.preview({ changes: changes!, limit: 50 }),
    enabled: enabled && !!changes?.length,
    staleTime: 0,
    gcTime: 0,
    retry: false,
  })
}
