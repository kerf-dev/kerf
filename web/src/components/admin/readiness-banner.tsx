import { useQueries } from '@tanstack/react-query'
import { ArrowRightIcon, HourglassIcon, OctagonAlertIcon, XIcon } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { api, unwrap } from '@/api/client'
import { useClusters } from '@/api/queries'
import type { ReadinessState } from '@/api/types'
import { checkTitle, isPending, isProblem, STATE_INFO, withPending } from './readiness'

const EFFECT: Record<ReadinessState, string> = {
  Registered: 'the agent is not connected',
  AgentConnected: 'the agent cannot work',
  MetricsOK: 'recommendations are stale or missing',
  ReadyAudit: 'no recommendations yet',
  AutomationReady: 'auto-patch is unavailable',
}

const KEY = 'ro-readiness-banner-dismissed'

/**
 * Overview banner (plan cluster-onboarding.md §5): one line per cluster with a blocking readiness
 * failure, naming the first failing check and what it breaks. Dismissing hides it until the set of
 * failures changes.
 */
export function ReadinessBanner() {
  const clusters = useClusters()
  // `summary.blocking` counts failing blocking checks only; blocking checks without a result are in
  // the detail's `summary.unknown`, so every cluster short of AutomationReady is looked at.
  const candidates = (clusters.data?.items ?? []).filter(
    (c) => c.readiness && ((c.readiness.blocking ?? 0) > 0 || c.readiness.state !== 'AutomationReady'),
  )
  const details = useQueries({
    queries: candidates.map((c) => ({
      queryKey: ['readiness', c.name],
      queryFn: () =>
        unwrap(api.GET('/v1/clusters/{cluster}/readiness', { params: { path: { cluster: c.name } } })),
      staleTime: 60_000,
    })),
  })
  const lines = candidates
    .map((c, i) => {
      const r = details[i]?.data
      const checks = r ? withPending(r.checks, r.blockedBy) : []
      const failing = checks.filter((x) => x.severity === 'blocking' && isProblem(x))
      const pending = checks.filter(isPending)
      return {
        cluster: c.name,
        failing: r ? failing.length : c.readiness!.blocking,
        first: failing[0],
        pending: pending.length,
        pendingFor: pending[0]?.gates,
      }
    })
    .filter((l) => l.failing || l.pending)
  const signature = lines.map((l) => `${l.cluster}:${l.first?.id ?? l.failing}:${l.pending}`).join('|')
  const [dismissed, setDismissed] = useState(() => {
    try {
      return sessionStorage.getItem(KEY) ?? ''
    } catch {
      return ''
    }
  })
  if (!lines.length || dismissed === signature) return null
  const attention = lines.filter((l) => l.failing).length
  return (
    <div
      role="status"
      className={
        attention
          ? 'flex items-start gap-3 rounded-xl border border-warning/40 bg-warning/[0.07] px-4 py-3'
          : 'flex items-start gap-3 rounded-xl border bg-muted/40 px-4 py-3'
      }
      data-testid="readiness-banner"
    >
      {attention ? (
        <OctagonAlertIcon className="mt-0.5 size-4 shrink-0 text-[color-mix(in_oklch,var(--warning)_80%,var(--foreground))]" />
      ) : (
        <HourglassIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      )}
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium">
          {attention
            ? attention === 1
              ? 'A cluster needs attention'
              : `${attention} clusters need attention`
            : 'Waiting for readiness results'}
        </div>
        <ul className="mt-1 space-y-0.5">
          {lines.map((l) => (
            <li key={l.cluster} className="text-[13px] text-pretty text-muted-foreground">
              <Link to={`/clusters/${encodeURIComponent(l.cluster)}?tab=readiness`} className="group">
                <span className="font-mono font-medium text-foreground group-hover:underline">
                  {l.cluster}
                </span>
                {': '}
                {l.failing ? (
                  l.first ? (
                    <>
                      {l.first.message.replace(/\.$/, '') || checkTitle(l.first.id)}
                      {l.first.gates ? ` — ${EFFECT[l.first.gates]}` : ''}
                      {l.failing > 1 ? ` (+${l.failing - 1} more)` : ''}
                    </>
                  ) : (
                    `${l.failing} blocking check${l.failing === 1 ? '' : 's'} failing`
                  )
                ) : null}
                {l.pending ? (
                  <span data-testid="readiness-banner-pending">
                    {l.failing ? ' · ' : ''}
                    {l.pending} check{l.pending === 1 ? '' : 's'} not reported yet
                    {!l.failing && l.pendingFor ? ` (needed for ${STATE_INFO[l.pendingFor].label})` : ''}
                  </span>
                ) : null}
                <ArrowRightIcon className="ml-1 inline size-3 opacity-0 transition-opacity group-hover:opacity-100" />
              </Link>
            </li>
          ))}
        </ul>
      </div>
      <button
        type="button"
        aria-label="Dismiss"
        className="rounded p-0.5 text-muted-foreground hover:text-foreground"
        onClick={() => {
          setDismissed(signature)
          try {
            sessionStorage.setItem(KEY, signature)
          } catch {
            /* ignore */
          }
        }}
      >
        <XIcon className="size-4" />
      </button>
    </div>
  )
}
