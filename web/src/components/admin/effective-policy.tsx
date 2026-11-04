import { ArrowUpIcon, CheckCircle2Icon, CircleSlashIcon, TriangleAlertIcon } from 'lucide-react'
import { Link } from 'react-router'
import type { EffectivePolicy, LayerSource } from '@/api/types'
import { ModeBadge } from '@/components/domain'
import { Badge } from '@/components/ui/badge'
import { Callout } from '@/components/ui/form'
import { specChips } from '@/lib/spec'
import { cn } from '@/lib/utils'
import { OriginBadge } from './common'

export const LAYER_LABEL: Record<LayerSource, string> = {
  'built-in': 'Built-in defaults',
  'global-default': 'Global defaults',
  'cluster-default-chart': 'Cluster defaults (chart)',
  'cluster-default': 'Cluster defaults',
  policy: 'Policy',
  'workload-override': 'Workload override',
  annotation: 'Annotation',
}

/**
 * The precedence chain for one workload (`GET /v1/workloads/{id}/effective-policy`): every layer from
 * built-in up to annotations, which ones applied, and which layer set each field.
 */
export function EffectivePolicyView({ ep, compact }: { ep: EffectivePolicy; compact?: boolean }) {
  const layers = [...ep.layers].reverse()
  const drift =
    ep.agentModes && (ep.agentModes.cpu !== ep.modes.cpu || ep.agentModes.memory !== ep.modes.memory)
  return (
    <div className="flex flex-col gap-4" data-testid="effective-policy">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border bg-muted/30 px-4 py-3">
        <div className="flex items-center gap-2 text-[13px]">
          <span className="text-muted-foreground">CPU</span>
          <ModeBadge mode={ep.modes.cpu} source={ep.modes.cpuSource} />
        </div>
        <div className="flex items-center gap-2 text-[13px]">
          <span className="text-muted-foreground">Memory</span>
          <ModeBadge mode={ep.modes.memory} source={ep.modes.memorySource} />
        </div>
        {ep.agentModes ? (
          <div
            className={cn(
              'text-xs',
              drift
                ? 'text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]'
                : 'text-muted-foreground',
            )}
          >
            {drift
              ? `The agent still reports CPU ${ep.agentModes.cpu} / memory ${ep.agentModes.memory}; it catches up on its next pull.`
              : 'The agent reports the same modes.'}
          </div>
        ) : null}
      </div>
      {ep.warnings?.length ? (
        <Callout tone="warning" icon={TriangleAlertIcon}>
          {ep.warnings.join(' · ')}
        </Callout>
      ) : null}
      <ol className="relative flex flex-col gap-2" aria-label="Precedence, highest first">
        {layers.map((l, i) => (
          <li
            key={`${l.source}-${l.name ?? i}`}
            className={cn(
              'relative flex items-start gap-3 rounded-lg border px-3.5 py-2.5',
              l.applied ? 'bg-card' : 'border-dashed bg-transparent opacity-75',
            )}
          >
            <span className="mt-0.5 shrink-0">
              {l.applied ? (
                <CheckCircle2Icon className="size-4 text-success" />
              ) : (
                <CircleSlashIcon className="size-4 text-muted-foreground" />
              )}
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="text-[13px] font-medium">{LAYER_LABEL[l.source]}</span>
                {l.name ? (
                  l.policyId && l.source === 'policy' ? (
                    <Link
                      to={`/admin/policies/${l.policyId}`}
                      className="font-mono text-xs text-primary hover:underline"
                    >
                      {l.name}
                    </Link>
                  ) : (
                    <span className="font-mono text-xs text-muted-foreground">{l.name}</span>
                  )
                ) : null}
                {l.origin ? <OriginBadge origin={l.origin} /> : null}
                {l.priority != null && l.source === 'policy' ? (
                  <Badge variant="muted" className="font-mono text-[10px]">
                    prio {l.priority}
                  </Badge>
                ) : null}
              </div>
              {l.note ? <div className="mt-0.5 text-xs text-muted-foreground">{l.note}</div> : null}
              {!compact && l.spec ? (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {specChips(l.spec).map((c) => (
                    <span
                      key={c}
                      className="rounded bg-secondary px-1.5 py-0.5 text-[11px] text-secondary-foreground"
                    >
                      {c}
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
            {i < layers.length - 1 ? (
              <ArrowUpIcon
                className="absolute -bottom-2.5 left-5 z-10 size-3 rounded-full bg-background text-muted-foreground/60"
                aria-hidden
              />
            ) : null}
          </li>
        ))}
      </ol>
      {!compact && ep.fields.length ? (
        <div className="overflow-hidden rounded-lg border">
          <table className="w-full text-[13px]">
            <thead className="bg-muted/50 text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-1.5 text-left font-medium">Setting</th>
                <th className="px-3 py-1.5 text-left font-medium">Value</th>
                <th className="px-3 py-1.5 text-left font-medium">Set by</th>
              </tr>
            </thead>
            <tbody>
              {ep.fields.map((f) => (
                <tr key={f.path} className="border-t">
                  <td className="px-3 py-1.5 font-mono text-xs">{f.path}</td>
                  <td className="max-w-[16rem] truncate px-3 py-1.5 font-mono text-xs" title={f.value}>
                    {f.value}
                  </td>
                  <td className="px-3 py-1.5 text-xs">
                    {LAYER_LABEL[f.source]}
                    {f.name ? <span className="text-muted-foreground"> · {f.name}</span> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  )
}
