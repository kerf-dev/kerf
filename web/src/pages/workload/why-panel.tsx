import { AlertTriangleIcon, CoinsIcon, CpuIcon, InfoIcon, LightbulbIcon, MemoryStickIcon } from 'lucide-react'
import { useState } from 'react'
import type {
  ConfidenceFactors,
  ContainerRecommendation,
  RecommendationPricing,
  ResourceExplanation,
  WorkloadRecommendation,
} from '@/api/types'
import { RISK_INFO } from '@/components/domain'
import { RateSourceChips } from '@/components/rate-source'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Tooltip } from '@/components/ui/tooltip'
import {
  formatDateTime,
  formatGoDuration,
  formatNumber,
  formatPercent,
  formatQuantity,
  formatResource,
  parseGoDuration,
  parseQuantity,
  type Resource,
} from '@/lib/format'
import { cn } from '@/lib/utils'

const PROFILE_LABEL = {
  dev: 'Dev',
  production: 'Production',
  'high-availability': 'High availability',
} as const

/** Horizontal scale showing observed percentiles vs. current and recommended request. */
function DistributionStrip({
  resource,
  ex,
  current,
  recommended,
  limit,
}: {
  resource: Resource
  ex: ResourceExplanation
  current?: string
  recommended?: string
  limit?: string
}) {
  const p = (q?: string) => parseQuantity(q)
  const vals = {
    p50: p(ex.observed.p50),
    p95: p(ex.observed.p95),
    p99: p(ex.observed.p99),
    max: p(ex.observed.max),
    cur: p(current),
    rec: p(recommended),
    lim: p(limit),
  }
  const top = Math.max(...Object.values(vals).filter((v): v is number => v != null)) * 1.08 || 1
  const pct = (v?: number) => ((v ?? 0) / top) * 100
  const pos = (v?: number) => `${pct(v)}%`
  // Keep the floating labels inside the card near the edges.
  const anchor = (v?: number) =>
    pct(v) > 78
      ? 'items-end -translate-x-[calc(100%-1px)]'
      : pct(v) < 18
        ? 'items-start translate-x-0'
        : 'items-center -translate-x-1/2'
  const fq = (q?: string) => formatQuantity(resource, q)
  return (
    <div className="pt-6 pb-7">
      <div className="relative h-2 rounded-full bg-muted">
        {/* observed band p50 → max */}
        <div
          className={cn(
            'absolute inset-y-0 rounded-full',
            resource === 'cpu' ? 'bg-chart-5/30' : 'bg-chart-2/30',
          )}
          style={{ left: pos(vals.p50), width: `calc(${pos(vals.max)} - ${pos(vals.p50)})` }}
        />
        <div
          className={cn(
            'absolute inset-y-0 rounded-full',
            resource === 'cpu' ? 'bg-chart-5/70' : 'bg-chart-2/70',
          )}
          style={{ left: pos(vals.p50), width: `calc(${pos(vals.p95)} - ${pos(vals.p50)})` }}
        />
        {(
          [
            ['p50', vals.p50, ex.observed.p50],
            ['p95', vals.p95, ex.observed.p95],
            ['p99', vals.p99, ex.observed.p99],
            ['max', vals.max, ex.observed.max],
          ] as const
        ).map(([k, v, q]) =>
          v != null ? (
            <Tooltip key={k} content={`${k.toUpperCase()} of per-pod usage: ${fq(q)}`}>
              <span
                className="absolute top-1/2 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-card bg-foreground/70"
                style={{ left: pos(v) }}
              />
            </Tooltip>
          ) : null,
        )}
        {vals.cur != null ? (
          <Tooltip content={`Current request: ${fq(current)}`}>
            <span
              className={cn('absolute -top-5 flex flex-col', anchor(vals.cur))}
              style={{ left: pos(vals.cur) }}
            >
              <span className="rounded bg-muted-foreground/15 px-1 font-mono text-[10px] whitespace-nowrap text-muted-foreground">
                now {fq(current)}
              </span>
              <span className="h-3 w-px bg-muted-foreground" />
            </span>
          </Tooltip>
        ) : null}
        {vals.rec != null ? (
          <Tooltip content={`Recommended request: ${fq(recommended)}`}>
            <span
              className={cn('absolute top-2 flex flex-col', anchor(vals.rec))}
              style={{ left: pos(vals.rec) }}
            >
              <span className="h-3 w-0.5 bg-success" />
              <span className="rounded bg-success/15 px-1 font-mono text-[10px] font-medium whitespace-nowrap text-success">
                rec {fq(recommended)}
              </span>
            </span>
          </Tooltip>
        ) : null}
        {vals.lim != null ? (
          <Tooltip content={`Current limit: ${fq(limit)}`}>
            <span
              className="absolute top-1/2 h-4 w-0.5 -translate-y-1/2 bg-destructive/70"
              style={{ left: pos(vals.lim) }}
            />
          </Tooltip>
        ) : null}
      </div>
    </div>
  )
}

function PricingWhy({ pricing, replicas }: { pricing?: RecommendationPricing; replicas?: number }) {
  return (
    <div className="space-y-2" data-testid="pricing">
      <div className="flex items-center gap-2 text-[13px] font-semibold">
        <CoinsIcon className="size-4 text-muted-foreground" />
        Savings estimate
        {pricing ? (
          pricing.source === 'metrics' ? (
            <Tooltip content="Average of the cluster's kerf_*_hourly_cost_usd pricing series, over its priced nodes.">
              <span className="ml-auto rounded-md bg-success/12 px-1.5 py-0.5 text-[11px] font-medium text-success">
                pricing metrics
              </span>
            </Tooltip>
          ) : (
            <Tooltip content="The cluster had no pricing series, so the hub used its configured fallback rates. Treat the dollar figures as rough.">
              <span className="ml-auto inline-flex items-center gap-1 rounded-md bg-warning/15 px-1.5 py-0.5 text-[11px] font-medium text-[color-mix(in_oklch,var(--warning)_70%,var(--foreground))]">
                <AlertTriangleIcon className="size-3" /> default rates
              </span>
            </Tooltip>
          )
        ) : null}
      </div>
      {pricing?.source === 'metrics' && pricing.rateSources?.length ? (
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          Rates from
          <RateSourceChips sources={pricing.rateSources} period={pricing.curPeriod} />
        </div>
      ) : null}
      {pricing ? (
        <div className="grid grid-cols-2 gap-2">
          <div className="rounded-lg border bg-muted/40 px-2.5 py-1.5">
            <div className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">CPU</div>
            <div className="font-mono text-[13px] font-medium tabular">
              ${pricing.cpuCoreHourUSD.toFixed(4)}
              <span className="text-[11px] font-normal text-muted-foreground"> /core·h</span>
            </div>
          </div>
          <div className="rounded-lg border bg-muted/40 px-2.5 py-1.5">
            <div className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
              Memory
            </div>
            <div className="font-mono text-[13px] font-medium tabular">
              ${pricing.memoryGiBHourUSD.toFixed(4)}
              <span className="text-[11px] font-normal text-muted-foreground"> /GiB·h</span>
            </div>
          </div>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">This recommendation predates pricing details.</p>
      )}
      <p className="text-xs leading-relaxed text-muted-foreground">
        (current − recommended request) × hourly rate ×{' '}
        {replicas != null ? replicas.toFixed(replicas % 1 ? 2 : 0) : 'avg.'} replicas × 730 h per container.
        Estimated, not realized.
      </p>
    </div>
  )
}

function Step({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <li className="relative flex gap-3 pb-3 last:pb-0">
      <span className="z-10 flex size-5 shrink-0 items-center justify-center rounded-full border bg-card text-[10px] font-semibold text-muted-foreground tabular">
        {n}
      </span>
      <div className="min-w-0 pt-0.5 text-[13px] leading-snug">{children}</div>
    </li>
  )
}

function ResourceWhy({ resource, c }: { resource: Resource; c: ContainerRecommendation }) {
  const ex = c.explanation?.[resource]
  if (!ex) return <p className="text-[13px] text-muted-foreground">No explanation for {resource}.</p>
  const fq = (q?: string) => formatQuantity(resource, q)
  const base = parseQuantity(ex.base)
  const withMargin = base != null && ex.margin != null ? base * (1 + ex.margin) : undefined
  const removed = c.removeLimits?.includes(resource)
  const Icon = resource === 'cpu' ? CpuIcon : MemoryStickIcon
  return (
    <div>
      <div className="flex items-center gap-2 text-[13px] font-semibold">
        <Icon className="size-4 text-muted-foreground" />
        {resource === 'cpu' ? 'CPU' : 'Memory'}
        <span className="ml-auto font-mono text-xs font-normal text-muted-foreground">
          {fq(c.current.requests?.[resource])} →{' '}
          <span className="font-medium text-foreground">{fq(c.recommended.requests?.[resource])}</span>
        </span>
      </div>
      <DistributionStrip
        resource={resource}
        ex={ex}
        current={c.current.requests?.[resource]}
        recommended={c.recommended.requests?.[resource]}
        limit={c.current.limits?.[resource]}
      />
      <ol className="relative before:absolute before:top-2 before:bottom-2 before:left-[9.5px] before:w-px before:bg-border">
        <Step n={1}>
          Take the <b className="font-semibold">{ex.statistic}</b> of per-pod usage:{' '}
          <span className="font-mono text-xs">{fq(ex.base)}</span>
          <span className="block text-xs text-muted-foreground">
            observed p50 {fq(ex.observed.p50)} · p95 {fq(ex.observed.p95)} · p99 {fq(ex.observed.p99)} · max{' '}
            {fq(ex.observed.max)}
          </span>
        </Step>
        {ex.margin != null ? (
          <Step n={2}>
            Add {formatPercent(ex.margin)} {resource === 'cpu' ? 'headroom' : 'buffer'}
            {withMargin != null ? (
              <>
                : <span className="font-mono text-xs">{formatResource(resource, withMargin)}</span>
              </>
            ) : null}
          </Step>
        ) : null}
        {ex.roundedTo ? (
          <Step n={ex.margin != null ? 3 : 2}>
            Round up to a multiple of <span className="font-mono text-xs">{fq(ex.roundedTo)}</span>:{' '}
            <b className="font-mono text-xs font-semibold text-success">
              {fq(c.recommended.requests?.[resource])}
            </b>
          </Step>
        ) : null}
        <Step n={(ex.margin != null ? 3 : 2) + (ex.roundedTo ? 1 : 0)}>
          Limit: <b className="font-semibold">{ex.limitRule ?? 'keep'}</b>
          <span className="block text-xs text-muted-foreground">
            {removed
              ? 'The limit will be removed.'
              : c.recommended.limits?.[resource]
                ? `Recommended limit ${fq(c.recommended.limits[resource])} (now ${fq(c.current.limits?.[resource]) || 'none'})`
                : 'No limit set.'}
          </span>
        </Step>
      </ol>
      {ex.notes?.length ? (
        <ul className="mt-3 space-y-1.5">
          {ex.notes.map((n) => (
            <li
              key={n}
              className="flex gap-2 rounded-md bg-warning/10 px-2.5 py-1.5 text-xs text-foreground/90"
            >
              <LightbulbIcon className="mt-0.5 size-3.5 shrink-0 text-warning" />
              {n}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}

export function WhyPanel({ rec }: { rec: WorkloadRecommendation }) {
  const candidates = rec.containers.filter((c) => c.explanation)
  const main = candidates.find((c) => !c.init && !(c.riskFlags ?? []).includes('sidecar')) ?? candidates[0]
  const [name, setName] = useState(main?.name)
  const c = candidates.find((x) => x.name === name) ?? main
  const ex = c?.explanation
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle className="flex items-center gap-2">
            <LightbulbIcon className="size-4 text-primary" /> Why this recommendation
          </CardTitle>
          <CardDescription>How the hub derived each value from observed usage</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-5 pt-3">
        {!c || !ex ? (
          <p className="text-[13px] text-muted-foreground">
            The hub did not return an explanation for this recommendation.
          </p>
        ) : (
          <>
            {candidates.length > 1 ? (
              <select
                value={c.name}
                onChange={(e) => setName(e.target.value)}
                aria-label="Container"
                className="h-8 w-full rounded-md border bg-card px-2 font-mono text-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40"
              >
                {candidates.map((x) => (
                  <option key={x.name} value={x.name}>
                    {x.name}
                    {x.init ? ' (init)' : ''}
                  </option>
                ))}
              </select>
            ) : null}
            <dl className="grid grid-cols-3 gap-2 text-center">
              {[
                ['Strategy', ex.strategyName],
                ['Profile', PROFILE_LABEL[ex.profile]],
                ['Window', formatGoDuration(ex.window)],
              ].map(([k, v]) => (
                <div key={k} className="rounded-lg border bg-muted/40 px-2 py-2">
                  <dt className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
                    {k}
                  </dt>
                  <dd className="mt-0.5 text-[13px] font-semibold">{v}</dd>
                </div>
              ))}
            </dl>
            <ResourceWhy resource="cpu" c={c} />
            <div className="border-t" />
            <ResourceWhy resource="memory" c={c} />
            <div className="border-t" />
            <PricingWhy pricing={rec.pricing} replicas={rec.averageReplicas} />
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function ConfidenceCard({ rec }: { rec: WorkloadRecommendation }) {
  const dc = rec.dataCoverage
  const windowSec = parseGoDuration(dc.window)
  const coveredSec = parseGoDuration(dc.covered)
  const conf = rec.confidence
  const tone = conf >= 0.8 ? 'text-success' : conf >= 0.6 ? 'text-warning' : 'text-destructive'
  const stroke = conf >= 0.8 ? 'stroke-success' : conf >= 0.6 ? 'stroke-warning' : 'stroke-destructive'
  const reducers = rec.riskFlags.filter((f) =>
    ['restarting', 'short-lived', 'low-confidence', 'throttled', 'oom-killed'].includes(f),
  )
  // Each container's confidence is Σ score × weight; the workload's is the minimum, so the
  // breakdown shown is the container that limits it.
  const withFactors = rec.containers
    .filter((c) => c.explanation?.confidenceFactors)
    .map((c) => ({ name: c.name, factors: c.explanation!.confidenceFactors! }))
  const limiting = withFactors.reduce<(typeof withFactors)[number] | undefined>(
    (min, c) => (!min || c.factors.confidence < min.factors.confidence ? c : min),
    undefined,
  )
  const R = 26
  const C = 2 * Math.PI * R
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Confidence</CardTitle>
          <CardDescription>How much the hub trusts this recommendation</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4 pt-3">
        <div className="flex items-center gap-4">
          <svg viewBox="0 0 64 64" className="size-16 -rotate-90" aria-hidden>
            <circle cx="32" cy="32" r={R} className="fill-none stroke-muted" strokeWidth="6" />
            <circle
              cx="32"
              cy="32"
              r={R}
              className={cn('fill-none transition-all', stroke)}
              strokeWidth="6"
              strokeLinecap="round"
              strokeDasharray={`${C * conf} ${C}`}
            />
          </svg>
          <div>
            <div className={cn('text-2xl font-semibold tabular', tone)}>{formatPercent(conf)}</div>
            <div className="text-xs text-muted-foreground">
              {conf >= 0.8
                ? 'High — safe to automate'
                : conf >= 0.6
                  ? 'Medium — review before applying'
                  : 'Low — collecting more data'}
            </div>
          </div>
        </div>
        {limiting ? (
          <FactorBreakdown
            factors={limiting.factors}
            container={limiting.name}
            multi={withFactors.length > 1}
          />
        ) : null}
        <div className="space-y-2.5 text-[13px]">
          {!limiting ? (
            <div>
              <div className="mb-1 flex justify-between">
                <span className="text-muted-foreground">Data coverage</span>
                <span className="font-medium tabular">{formatPercent(dc.ratio)}</span>
              </div>
              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div className="h-full rounded-full bg-primary" style={{ width: `${dc.ratio * 100}%` }} />
              </div>
            </div>
          ) : null}
          <Row
            k="History covered"
            v={
              coveredSec != null && windowSec
                ? `${formatGoDuration(dc.covered)} of ${formatGoDuration(dc.window)}`
                : formatGoDuration(dc.window)
            }
          />
          {dc.samples != null ? <Row k="Samples" v={formatNumber(dc.samples)} /> : null}
          {dc.firstSample ? <Row k="First sample" v={formatDateTime(dc.firstSample, true)} /> : null}
          {dc.lastSample ? <Row k="Last sample" v={formatDateTime(dc.lastSample, true)} /> : null}
          {rec.averageReplicas != null ? (
            <Row k="Avg. replicas" v={rec.averageReplicas.toFixed(rec.averageReplicas % 1 ? 2 : 0)} />
          ) : null}
        </div>
        {reducers.length ? (
          <div className="rounded-lg border border-dashed p-2.5 text-xs text-muted-foreground">
            <div className="mb-1 flex items-center gap-1.5 font-medium text-foreground">
              <InfoIcon className="size-3.5" /> Lowered by
            </div>
            {reducers.map((f) => RISK_INFO[f].label).join(', ')}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}

const FACTORS = [
  {
    key: 'coverage',
    label: 'Coverage',
    desc: 'Fraction of the window’s steps with usage data.',
    cls: 'bg-chart-1',
  },
  {
    key: 'samples',
    label: 'Samples',
    desc: 'Samples relative to the count the hub needs for full confidence.',
    cls: 'bg-chart-2',
  },
  {
    key: 'variance',
    label: 'Stability',
    desc: 'CPU usage stability, 1 / (1 + coefficient of variation).',
    cls: 'bg-chart-3',
  },
] as const

function FactorBreakdown({
  factors,
  container,
  multi,
}: {
  factors: ConfidenceFactors
  container: string
  multi: boolean
}) {
  return (
    <div className="space-y-2.5" data-testid="confidence-factors">
      <div className="flex items-baseline justify-between text-[11px] text-muted-foreground">
        <span className="font-medium tracking-wide uppercase">score × weight</span>
        {multi ? (
          <Tooltip content="The workload's confidence is the lowest of its containers'.">
            <span className="cursor-help font-mono underline decoration-dotted underline-offset-2">
              limited by {container}
            </span>
          </Tooltip>
        ) : null}
      </div>
      {/* contribution of each factor to the total, as one stacked bar */}
      <div className="flex h-2 gap-px overflow-hidden rounded-full bg-muted">
        {FACTORS.map((f) => (
          <div
            key={f.key}
            className={f.cls}
            style={{ width: `${factors[f.key].score * factors[f.key].weight * 100}%` }}
          />
        ))}
      </div>
      <table className="w-full text-[13px]">
        <tbody>
          {FACTORS.map((f) => {
            const x = factors[f.key]
            return (
              <tr key={f.key}>
                <td className="py-0.5">
                  <Tooltip content={f.desc}>
                    <span className="inline-flex cursor-help items-center gap-1.5 text-muted-foreground">
                      <span className={cn('size-2 rounded-sm', f.cls)} />
                      {f.label}
                    </span>
                  </Tooltip>
                </td>
                <td className="py-0.5 text-right font-mono text-xs tabular">{x.score.toFixed(2)}</td>
                <td className="py-0.5 text-right font-mono text-xs text-muted-foreground tabular">
                  × {x.weight.toFixed(2)}
                </td>
                <td className="py-0.5 text-right font-mono text-xs font-medium tabular">
                  {(x.score * x.weight).toFixed(2)}
                </td>
              </tr>
            )
          })}
          <tr className="border-t">
            <td className="pt-1.5 font-medium">Confidence</td>
            <td colSpan={3} className="pt-1.5 text-right font-mono text-xs font-semibold tabular">
              = {factors.confidence.toFixed(2)}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  )
}

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-3">
      <span className="text-muted-foreground">{k}</span>
      <span className="text-right font-medium tabular">{v}</span>
    </div>
  )
}
