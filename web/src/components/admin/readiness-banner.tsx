import { useQueries } from '@tanstack/react-query'
import { ArrowRightIcon, OctagonAlertIcon, XIcon } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { api, unwrap } from '@/api/client'
import { useClusters } from '@/api/queries'
import type { ReadinessCheck, ReadinessState } from '@/api/types'
import { checkTitle } from './readiness'

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
  const affected = (clusters.data?.items ?? []).filter((c) => (c.readiness?.blocking ?? 0) > 0)
  const details = useQueries({
    queries: affected.map((c) => ({
      queryKey: ['readiness', c.name],
      queryFn: () =>
        unwrap(api.GET('/v1/clusters/{cluster}/readiness', { params: { path: { cluster: c.name } } })),
      staleTime: 60_000,
    })),
  })
  const lines = affected.map((c, i) => {
    const r = details[i]?.data
    const first: ReadinessCheck | undefined = r?.checks.find(
      (x) => x.severity === 'blocking' && x.status !== 'pass',
    )
    return { cluster: c.name, blocking: c.readiness!.blocking, first }
  })
  const signature = lines.map((l) => `${l.cluster}:${l.first?.id ?? l.blocking}`).join('|')
  const [dismissed, setDismissed] = useState(() => {
    try {
      return sessionStorage.getItem(KEY) ?? ''
    } catch {
      return ''
    }
  })
  if (!lines.length || dismissed === signature) return null
  return (
    <div
      role="status"
      className="flex items-start gap-3 rounded-xl border border-warning/40 bg-warning/[0.07] px-4 py-3"
      data-testid="readiness-banner"
    >
      <OctagonAlertIcon className="mt-0.5 size-4 shrink-0 text-[color-mix(in_oklch,var(--warning)_80%,var(--foreground))]" />
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium">
          {lines.length === 1 ? 'A cluster needs attention' : `${lines.length} clusters need attention`}
        </div>
        <ul className="mt-1 space-y-0.5">
          {lines.map((l) => (
            <li key={l.cluster} className="text-[13px] text-pretty text-muted-foreground">
              <Link to={`/clusters/${encodeURIComponent(l.cluster)}?tab=readiness`} className="group">
                <span className="font-mono font-medium text-foreground group-hover:underline">
                  {l.cluster}
                </span>
                {': '}
                {l.first ? (
                  <>
                    {l.first.message.replace(/\.$/, '') || checkTitle(l.first.id)}
                    {l.first.gates ? ` — ${EFFECT[l.first.gates]}` : ''}
                  </>
                ) : (
                  `${l.blocking} blocking check${l.blocking === 1 ? '' : 's'} failing`
                )}
                {l.blocking > 1 ? ` (+${l.blocking - 1} more)` : ''}
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
