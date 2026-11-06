import {
  ArrowDownWideNarrowIcon,
  CircleHelpIcon,
  Grid2x2Icon,
  LayersIcon,
  LightbulbIcon,
  PowerIcon,
  ThumbsUpIcon,
  WrenchIcon,
} from 'lucide-react'
import { Link } from 'react-router'
import { useReadiness } from '@/api/admin'
import type { GPUSuggestion, GPUSuggestionKind, GPUUsage } from '@/api/types'
import { GPUStatusBadge, Savings } from '@/components/domain'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { formatBytes, formatGoDuration, formatNumber, formatUSD } from '@/lib/format'
import { cn } from '@/lib/utils'

const SUGGESTION_INFO: Record<GPUSuggestionKind, { label: string; icon: typeof PowerIcon }> = {
  'time-slicing': { label: 'Time-slicing', icon: LayersIcon },
  mig: { label: 'MIG partitions', icon: Grid2x2Icon },
  'smaller-gpu': { label: 'Smaller GPU', icon: ArrowDownWideNarrowIcon },
  'scale-to-zero': { label: 'Scale to zero', icon: PowerIcon },
}

const pct = (v: number) => `${v.toFixed(v < 10 && v > 0 ? 1 : 0)} %`

/** A horizontal gauge with its value as text, for utilisation and memory. */
function Gauge({
  label,
  value,
  text,
  detail,
  tone,
}: {
  label: string
  /** Percent, 0..100. */
  value?: number
  text: string
  detail?: string
  tone?: 'default' | 'low'
}) {
  const v = value == null ? undefined : Math.min(100, Math.max(0, value))
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between gap-3 text-[13px]">
        <span className="text-muted-foreground">{label}</span>
        <span className="font-medium tabular">
          {text}
          {detail ? <span className="ml-1.5 text-xs font-normal text-muted-foreground">{detail}</span> : null}
        </span>
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={v}
        aria-valuetext={text}
        className="relative h-2 overflow-hidden rounded-full bg-muted"
      >
        {v != null ? (
          <span
            className={cn(
              'absolute inset-y-0 left-0 rounded-full',
              tone === 'low' ? 'bg-warning' : 'bg-primary',
            )}
            style={{ width: `${Math.max(v, v > 0 ? 1.5 : 0)}%` }}
          />
        ) : null}
      </div>
    </div>
  )
}

function Fact({ k, v, testId }: { k: string; v: React.ReactNode; testId?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">{k}</dt>
      <dd className="mt-0.5 truncate text-[15px] font-semibold tabular" data-testid={testId}>
        {v}
      </dd>
    </div>
  )
}

/** "–" instead of $0 when no GPU price is known. */
const cost = (gpu: GPUUsage, v: number) => (gpu.priced ? formatUSD(v) : '–')

function Suggestion({ s }: { s: GPUSuggestion }) {
  const info = SUGGESTION_INFO[s.kind]
  return (
    <li className="flex gap-3" data-testid="gpu-suggestion" data-kind={s.kind}>
      <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-info/12 text-info">
        <info.icon className="size-3.5" />
      </span>
      <div className="min-w-0 flex-1 text-[13px]">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium">{info.label}</span>
          {s.targetModel ? (
            <Badge variant="outline" className="font-mono">
              {s.targetModel}
            </Badge>
          ) : null}
          {s.monthlySavingsUSD != null ? (
            <span className="ml-auto text-xs text-muted-foreground">
              saves <Savings value={s.monthlySavingsUSD} className="text-[13px]" />
              <span className="text-xs"> / mo</span>
            </span>
          ) : null}
        </div>
        <p className="text-muted-foreground">{s.message}</p>
      </div>
    </li>
  )
}

/** Why a workload has no GPU data: names the missing DCGM series from the cluster's readiness check. */
function NoDataExplanation({ cluster, gpus }: { cluster: string; gpus: number }) {
  const readiness = useReadiness(cluster)
  const check = readiness.data?.checks.find((c) => c.id === 'metrics.dcgm')
  return (
    <div
      role="status"
      className="flex gap-3 rounded-lg border border-border bg-muted/50 px-3 py-3"
      data-testid="gpu-no-data"
    >
      <CircleHelpIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 text-[13px]">
        <div className="font-medium">No GPU metrics for this workload</div>
        <p className="mt-0.5 text-muted-foreground">
          The workload requests {gpus} GPU{gpus === 1 ? '' : 's'}, but the NVIDIA DCGM exporter has no series
          for its pods, so utilisation, memory and suggestions are not available. Check that the DCGM exporter
          runs on the GPU nodes and that its metrics reach the datasource.
        </p>
        {check && check.status !== 'pass' ? (
          <p className="mt-1.5 text-foreground/80" data-testid="gpu-dcgm-check">
            <span className="font-mono text-xs">metrics.dcgm</span> · {check.message}
          </p>
        ) : null}
        <Link
          to={`/clusters/${encodeURIComponent(cluster)}?tab=readiness`}
          className="mt-2 inline-flex items-center gap-1.5 rounded text-[13px] font-medium underline-offset-2 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/40"
        >
          <WrenchIcon className="size-3.5" />
          Open the readiness checks of {cluster}
        </Link>
      </div>
    </div>
  )
}

/** GPUs of a workload: status, utilisation, memory, cost and suggestions (visibility only). */
export function GpuPanel({ gpu, cluster }: { gpu: GPUUsage; cluster: string }) {
  const noData = gpu.status === 'no-data'
  const memPct =
    gpu.memoryUtilizationP95 ??
    (gpu.memoryUsedP95Bytes != null && gpu.memoryTotalBytes
      ? (gpu.memoryUsedP95Bytes / gpu.memoryTotalBytes) * 100
      : undefined)
  const window = gpu.window ? formatGoDuration(gpu.window) : undefined
  return (
    <Card data-testid="gpu-panel" role="region" aria-label="GPU">
      <CardHeader>
        <div>
          <CardTitle>GPU</CardTitle>
          <CardDescription>
            NVIDIA DCGM
            {window ? ` · last ${window}` : ''}
            {!noData ? ` · ${formatNumber(gpu.samples)} samples` : ''} · visibility only, nothing is changed
          </CardDescription>
        </div>
        <GPUStatusBadge status={gpu.status} />
      </CardHeader>
      <CardContent className="space-y-5 pt-3">
        <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
          <Fact k="GPUs per pod" v={gpu.gpusPerPod} testId="gpu-per-pod" />
          <Fact
            k="GPUs total"
            v={
              <>
                {gpu.gpus}
                <span className="ml-1 text-xs font-normal text-muted-foreground">
                  in {gpu.pods} pod{gpu.pods === 1 ? '' : 's'}
                </span>
              </>
            }
            testId="gpu-total"
          />
          <div className="col-span-2 min-w-0">
            <Fact k="Model" v={gpu.model ?? <span className="text-muted-foreground">unknown</span>} />
          </div>
        </dl>

        {noData ? (
          <NoDataExplanation cluster={cluster} gpus={gpu.gpus} />
        ) : (
          <div className="grid gap-x-8 gap-y-4 md:grid-cols-2">
            <div className="space-y-3">
              <h3 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                Utilisation
              </h3>
              <Gauge
                label="p95"
                value={gpu.utilizationP95}
                text={gpu.utilizationP95 != null ? pct(gpu.utilizationP95) : '–'}
                tone={gpu.utilizationP95 != null && gpu.utilizationP95 < 40 ? 'low' : 'default'}
              />
              <Gauge
                label="Average"
                value={gpu.utilizationAvg}
                text={gpu.utilizationAvg != null ? pct(gpu.utilizationAvg) : '–'}
                tone={gpu.utilizationP95 != null && gpu.utilizationP95 < 40 ? 'low' : 'default'}
              />
            </div>
            <div className="space-y-3">
              <h3 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                Memory
              </h3>
              <Gauge
                label="Used p95 of total"
                value={memPct}
                text={
                  gpu.memoryUsedP95Bytes != null && gpu.memoryTotalBytes != null
                    ? `${formatBytes(gpu.memoryUsedP95Bytes)} of ${formatBytes(gpu.memoryTotalBytes)}`
                    : '–'
                }
                detail={memPct != null ? pct(memPct) : undefined}
                tone={memPct != null && memPct < 30 ? 'low' : 'default'}
              />
              <p className="text-xs text-muted-foreground">
                Framebuffer of the busiest GPU of a pod; one GPU has{' '}
                {gpu.memoryTotalBytes != null ? formatBytes(gpu.memoryTotalBytes) : 'an unknown amount of'}{' '}
                memory.
              </p>
            </div>
          </div>
        )}

        <div>
          <h3 className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
            Cost per month
          </h3>
          <dl className="grid grid-cols-3 gap-x-6 gap-y-3">
            <Fact k="GPUs" v={cost(gpu, gpu.monthlyCostUSD)} testId="gpu-cost" />
            <Fact k="Per pod" v={cost(gpu, gpu.perPodMonthlyCostUSD)} testId="gpu-cost-pod" />
            <Fact
              k="Unused"
              v={
                <span className={cn(gpu.priced && gpu.unusedMonthlyCostUSD > 0 && 'text-warning')}>
                  {cost(gpu, gpu.unusedMonthlyCostUSD)}
                </span>
              }
              testId="gpu-cost-unused"
            />
          </dl>
          {!gpu.priced ? (
            <p className="mt-2 text-xs text-muted-foreground" data-testid="gpu-unpriced">
              No GPU price is known for the nodes of this workload, so costs are not shown. Prices for GPU
              instance types come from the pricing service.
            </p>
          ) : null}
        </div>

        {!noData ? (
          <div>
            <h3 className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
              Suggestions
            </h3>
            {gpu.suggestions.length ? (
              <ul className="space-y-3" data-testid="gpu-suggestions">
                {gpu.suggestions.map((s) => (
                  <Suggestion key={`${s.kind}-${s.targetModel ?? ''}`} s={s} />
                ))}
              </ul>
            ) : gpu.status === 'ok' ? (
              <div className="flex items-center gap-3 rounded-lg bg-success/8 px-3 py-3 text-[13px]">
                <ThumbsUpIcon className="size-5 text-success" />
                <span>The GPUs are used well. Nothing to suggest.</span>
              </div>
            ) : (
              <div className="flex items-center gap-3 rounded-lg bg-muted px-3 py-3 text-[13px] text-muted-foreground">
                <LightbulbIcon className="size-5" />
                <span>No suggestion for this workload.</span>
              </div>
            )}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}
