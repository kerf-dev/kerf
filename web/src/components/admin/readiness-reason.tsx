import { WrenchIcon } from 'lucide-react'
import { Link } from 'react-router'
import { useReadiness } from '@/api/admin'
import { useClusters } from '@/api/queries'
import { checkTitle, stateRank } from './readiness'

/**
 * When a workload has no recommendation because its cluster is not producing them (plan
 * cluster-onboarding.md §5), names the failing readiness check and links to it instead of a bare
 * "no data".
 */
export function ReadinessReason({ cluster }: { cluster: string }) {
  const clusters = useClusters()
  const summary = clusters.data?.items.find((c) => c.name === cluster)?.readiness
  const broken = !!summary && stateRank(summary.state) < stateRank('ReadyAudit')
  const r = useReadiness(broken ? cluster : undefined)
  if (!broken) return null
  const first = r.data?.checks.find(
    (c) =>
      c.severity === 'blocking' &&
      c.status !== 'pass' &&
      c.gates &&
      stateRank(c.gates) <= stateRank('ReadyAudit'),
  )
  return (
    <Link
      to={`/clusters/${encodeURIComponent(cluster)}?tab=readiness`}
      className="mt-4 inline-flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2 text-left text-[13px] hover:border-warning/60"
      data-testid="readiness-reason"
    >
      <WrenchIcon className="mt-0.5 size-3.5 shrink-0" />
      <span>
        <span className="font-medium">{cluster} is not producing recommendations</span>
        <span className="block text-muted-foreground">
          {first ? `${checkTitle(first.id)}: ${first.message}` : 'A blocking readiness check is failing.'}{' '}
          Open the checklist.
        </span>
      </span>
    </Link>
  )
}
