import {
  ActivityIcon,
  AlarmClockIcon,
  ArrowDownRightIcon,
  ArrowRightIcon,
  ArrowUpRightIcon,
  BanIcon,
  BotIcon,
  CheckCircle2Icon,
  CircleDashedIcon,
  CircleOffIcon,
  CpuIcon,
  EyeIcon,
  FlameIcon,
  GaugeIcon,
  LayersIcon,
  Loader2Icon,
  MemoryStickIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  ShieldAlertIcon,
  ShuffleIcon,
  SkipForwardIcon,
  SparklesIcon,
  TimerIcon,
  TriangleAlertIcon,
  UnlinkIcon,
  XCircleIcon,
  ZapIcon,
} from 'lucide-react'
import type { EventType, Mode, ModeSource, Phase, RiskFlag, WorkloadKind } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import { delta, formatDelta, formatPercent, formatUSD, type Resource } from '@/lib/format'
import { cn } from '@/lib/utils'

// ------------------------------------------------------------------ modes

export const MODE_INFO: Record<Mode, { label: string; desc: string; icon: typeof BotIcon; cls: string }> = {
  'auto-patch': {
    label: 'Auto',
    desc: 'auto-patch — the agent applies recommendations automatically, within guardrails.',
    icon: BotIcon,
    cls: 'bg-primary/12 text-primary',
  },
  audit: {
    label: 'Audit',
    desc: 'audit — recommendations are computed and shown, nothing is changed.',
    icon: EyeIcon,
    cls: 'bg-info/12 text-info',
  },
  off: {
    label: 'Off',
    desc: 'off — the workload is ignored by the agent.',
    icon: CircleOffIcon,
    cls: 'bg-muted text-muted-foreground',
  },
}

export const MODE_SOURCE_LABEL: Record<ModeSource, string> = {
  'global-default': 'global default',
  'cluster-default': 'cluster default',
  policy: 'policy',
  annotation: 'annotation',
}

export function ModeBadge({
  mode,
  resource,
  source,
  policy,
}: {
  mode: Mode
  resource?: Resource
  source?: ModeSource
  policy?: string
}) {
  const info = MODE_INFO[mode]
  const Icon = info.icon
  const R = resource === 'cpu' ? CpuIcon : resource === 'memory' ? MemoryStickIcon : null
  const tip = (
    <span>
      {resource ? <b className="font-semibold">{resource === 'cpu' ? 'CPU' : 'Memory'}: </b> : null}
      {info.desc}
      {source ? (
        <span className="mt-1 block opacity-80">
          Set by {MODE_SOURCE_LABEL[source]}
          {source === 'policy' && policy ? ` “${policy}”` : ''}
        </span>
      ) : null}
    </span>
  )
  return (
    <Tooltip content={tip}>
      <span
        className={cn(
          'inline-flex h-5 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium',
          info.cls,
        )}
      >
        {R ? <R className="size-3 opacity-70" /> : <Icon className="size-3" />}
        {info.label}
      </span>
    </Tooltip>
  )
}

export function ModePair({
  cpu,
  memory,
  cpuSource,
  memorySource,
  policy,
}: {
  cpu: Mode
  memory: Mode
  cpuSource?: ModeSource
  memorySource?: ModeSource
  policy?: string
}) {
  if (cpu === memory && cpuSource === memorySource) {
    const info = MODE_INFO[cpu]
    const Icon = info.icon
    return (
      <Tooltip
        content={
          <span>
            <b className="font-semibold">CPU and memory: </b>
            {info.desc}
            {cpuSource ? (
              <span className="mt-1 block opacity-80">
                Set by {MODE_SOURCE_LABEL[cpuSource]}
                {cpuSource === 'policy' && policy ? ` “${policy}”` : ''}
              </span>
            ) : null}
          </span>
        }
      >
        <span
          className={cn(
            'inline-flex h-5 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium',
            info.cls,
          )}
        >
          <Icon className="size-3" />
          {info.label}
        </span>
      </Tooltip>
    )
  }
  return (
    <span className="inline-flex gap-1">
      <ModeBadge mode={cpu} resource="cpu" source={cpuSource} policy={policy} />
      <ModeBadge mode={memory} resource="memory" source={memorySource} policy={policy} />
    </span>
  )
}

// ------------------------------------------------------------------ phases

export const PHASE_INFO: Record<
  Phase,
  {
    desc: string
    variant: 'success' | 'warning' | 'destructive' | 'info' | 'muted' | 'secondary'
    icon: typeof BotIcon
  }
> = {
  Pending: {
    desc: 'No change is in progress (audit/off mode, or waiting for the next action).',
    variant: 'muted',
    icon: CircleDashedIcon,
  },
  Applying: { desc: 'A change is being rolled out or resized in place.', variant: 'info', icon: Loader2Icon },
  Applied: { desc: 'The latest recommendation is live.', variant: 'success', icon: CheckCircle2Icon },
  Blocked: {
    desc: 'A guardrail prevents the change the mode would allow.',
    variant: 'warning',
    icon: ShieldAlertIcon,
  },
  RolledBack: {
    desc: 'The health watch restored the previous values.',
    variant: 'destructive',
    icon: RotateCcwIcon,
  },
  Released: {
    desc: 'Left auto-patch; field ownership was released to Git/Argo CD.',
    variant: 'secondary',
    icon: UnlinkIcon,
  },
}

export function PhaseBadge({ phase, reason }: { phase: Phase; reason?: string }) {
  const info = PHASE_INFO[phase]
  const Icon = info.icon
  return (
    <Tooltip
      content={
        <span>
          {info.desc}
          {reason ? <span className="mt-1 block opacity-80">Reason: {reason}</span> : null}
        </span>
      }
    >
      <Badge variant={info.variant}>
        <Icon className={cn(phase === 'Applying' && 'animate-spin')} />
        {phase === 'RolledBack' ? 'Rolled back' : phase}
      </Badge>
    </Tooltip>
  )
}

// ------------------------------------------------------------------ risk flags

export const RISK_INFO: Record<
  RiskFlag,
  { label: string; desc: string; impact: string; icon: typeof BotIcon; severity: 'high' | 'medium' | 'low' }
> = {
  throttled: {
    label: 'Throttled',
    desc: 'Containers hit their CPU limit and were throttled by CFS.',
    impact: 'The CPU recommendation is never a decrease while throttling persists.',
    icon: GaugeIcon,
    severity: 'high',
  },
  'oom-killed': {
    label: 'OOM-killed',
    desc: 'A container was killed for exceeding its memory limit.',
    impact: 'The memory recommendation is never a decrease; it is raised at least 25 % over the last limit.',
    icon: FlameIcon,
    severity: 'high',
  },
  restarting: {
    label: 'Restarting',
    desc: 'Containers restarted repeatedly during the window.',
    impact: 'Usage samples are noisy, so confidence is reduced.',
    icon: RefreshCwIcon,
    severity: 'medium',
  },
  'hpa-coupled': {
    label: 'HPA-coupled',
    desc: 'A CPU/memory-utilization HorizontalPodAutoscaler targets this workload.',
    impact: 'Changing requests would change scaling behaviour, so it stays audit-only (D-011).',
    icon: ShuffleIcon,
    severity: 'medium',
  },
  sidecar: {
    label: 'Sidecar',
    desc: 'The pod runs injected sidecars (e.g. istio-proxy).',
    impact: 'Sidecar values may be overwritten by the injector; savings for them are indicative.',
    icon: LayersIcon,
    severity: 'low',
  },
  'short-lived': {
    label: 'Short-lived',
    desc: 'Pods live briefly (CronJob or batch), so samples are sparse.',
    impact: 'Percentiles are computed over run time only; confidence is reduced.',
    icon: TimerIcon,
    severity: 'low',
  },
  'low-confidence': {
    label: 'Low confidence',
    desc: 'Confidence is below the hub threshold.',
    impact: 'auto-patch is blocked by guardrails.minConfidence until more data is collected.',
    icon: TriangleAlertIcon,
    severity: 'medium',
  },
  'jvm-fixed-heap': {
    label: 'JVM fixed heap',
    desc: 'JVM with a fixed maximum heap (-Xmx); memory is never below heap + non-heap.',
    impact: 'Memory is audit-only: the agent never changes it. Change -Xmx together with the memory.',
    icon: TriangleAlertIcon,
    severity: 'medium',
  },
}

const SEVERITY_VARIANT = { high: 'destructive', medium: 'warning', low: 'muted' } as const

export function RiskFlagBadge({ flag, compact }: { flag: RiskFlag; compact?: boolean }) {
  const info = RISK_INFO[flag]
  const Icon = info.icon
  return (
    <Tooltip
      content={
        <span>
          <b className="font-semibold">{info.label}.</b> {info.desc}{' '}
          <span className="mt-1 block opacity-80">{info.impact}</span>
        </span>
      }
    >
      <Badge variant={SEVERITY_VARIANT[info.severity]} className={cn(compact && 'px-1')}>
        <Icon />
        {compact ? <span className="sr-only">{info.label}</span> : info.label}
      </Badge>
    </Tooltip>
  )
}

export function RiskFlags({
  flags,
  max = 3,
  compact,
}: {
  flags?: RiskFlag[]
  max?: number
  compact?: boolean
}) {
  if (!flags?.length) return <span className="text-xs text-muted-foreground/60">—</span>
  const order = (f: RiskFlag) => ['high', 'medium', 'low'].indexOf(RISK_INFO[f].severity)
  const sorted = [...flags].sort((a, b) => order(a) - order(b))
  const shown = sorted.slice(0, max)
  const rest = sorted.slice(max)
  return (
    <span className="inline-flex flex-wrap gap-1">
      {shown.map((f) => (
        <RiskFlagBadge key={f} flag={f} compact={compact} />
      ))}
      {rest.length ? (
        <Tooltip content={rest.map((f) => RISK_INFO[f].label).join(', ')}>
          <Badge variant="outline">+{rest.length}</Badge>
        </Tooltip>
      ) : null}
    </span>
  )
}

// ------------------------------------------------------------------ events

export const EVENT_INFO: Record<
  EventType,
  { label: string; desc: string; icon: typeof BotIcon; cls: string; dot: string }
> = {
  applied: {
    label: 'Applied',
    desc: 'A change became live.',
    icon: ZapIcon,
    cls: 'bg-success/14 text-success',
    dot: 'bg-success',
  },
  skipped: {
    label: 'Skipped',
    desc: 'The agent deliberately did not act.',
    icon: SkipForwardIcon,
    cls: 'bg-muted text-muted-foreground',
    dot: 'bg-muted-foreground/50',
  },
  blocked: {
    label: 'Blocked',
    desc: 'A guardrail prevented a change.',
    icon: BanIcon,
    cls: 'bg-warning/18 text-[color-mix(in_oklch,var(--warning)_70%,var(--foreground))]',
    dot: 'bg-warning',
  },
  fallback: {
    label: 'Fallback',
    desc: 'An in-place change switched to rollout.',
    icon: ArrowDownRightIcon,
    cls: 'bg-info/12 text-info',
    dot: 'bg-info',
  },
  rolled_back: {
    label: 'Rolled back',
    desc: 'The health watch restored previous values.',
    icon: RotateCcwIcon,
    cls: 'bg-destructive/12 text-destructive',
    dot: 'bg-destructive',
  },
  released: {
    label: 'Released',
    desc: 'Field ownership released to Git/Argo CD.',
    icon: UnlinkIcon,
    cls: 'bg-chart-6/14 text-chart-6',
    dot: 'bg-chart-6',
  },
  failed: {
    label: 'Failed',
    desc: 'A Kubernetes API call failed after retries.',
    icon: XCircleIcon,
    cls: 'bg-destructive/12 text-destructive',
    dot: 'bg-destructive',
  },
}

export function EventTypeBadge({ type }: { type: EventType }) {
  const info = EVENT_INFO[type]
  const Icon = info.icon
  return (
    <span
      className={cn(
        'inline-flex h-5 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium whitespace-nowrap',
        info.cls,
      )}
    >
      <Icon className="size-3" />
      {info.label}
    </span>
  )
}

export const RECOMMENDATION_ICON = SparklesIcon
export const ACTIVITY_ICON = ActivityIcon
export const COOLDOWN_ICON = AlarmClockIcon

// ------------------------------------------------------------------ values

export function KindBadge({ kind }: { kind: WorkloadKind }) {
  const short = { Deployment: 'deploy', StatefulSet: 'sts', DaemonSet: 'ds', CronJob: 'cron' }[kind]
  return (
    <Tooltip content={kind}>
      <span className="inline-flex h-[18px] items-center rounded border px-1 font-mono text-[10px] text-muted-foreground uppercase">
        {short}
      </span>
    </Tooltip>
  )
}

export function Savings({
  value,
  className,
  compact,
}: {
  value?: number
  className?: string
  compact?: boolean
}) {
  if (value == null) return <span className="text-muted-foreground/60">—</span>
  const neg = value < -0.5
  const zero = Math.abs(value) < 0.5
  return (
    <span
      className={cn(
        'tabular font-medium',
        neg ? 'text-destructive' : zero ? 'text-muted-foreground' : 'text-success',
        className,
      )}
    >
      {neg ? '−' : ''}
      {formatUSD(Math.abs(value), { compact })}
    </span>
  )
}

/** "500m → 120m  −76 %" */
export function Change({
  from,
  to,
  format,
  className,
}: {
  from?: number
  to?: number
  format: (v?: number) => string
  className?: string
}) {
  const d = delta(from, to)
  const tone =
    d == null || Math.abs(d) < 0.005 ? 'text-muted-foreground' : d < 0 ? 'text-success' : 'text-warning'
  const Arrow =
    d == null || Math.abs(d) < 0.005 ? ArrowRightIcon : d < 0 ? ArrowDownRightIcon : ArrowUpRightIcon
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 font-mono text-xs tabular whitespace-nowrap',
        className,
      )}
    >
      <span className="text-muted-foreground">{format(from)}</span>
      <Arrow className={cn('size-3', tone)} />
      <span className="font-medium text-foreground">{format(to)}</span>
      {d != null ? <span className={cn('text-[11px]', tone)}>{formatDelta(d)}</span> : null}
    </span>
  )
}

export function DeltaPill({ from, to }: { from?: number; to?: number }) {
  const d = delta(from, to)
  if (d == null) return null
  const flat = Math.abs(d) < 0.005
  const tone = flat
    ? 'bg-muted text-muted-foreground'
    : d < 0
      ? 'bg-success/12 text-success'
      : 'bg-warning/15 text-[color-mix(in_oklch,var(--warning)_70%,var(--foreground))]'
  const Arrow = flat ? ArrowRightIcon : d < 0 ? ArrowDownRightIcon : ArrowUpRightIcon
  return (
    <span
      className={cn(
        'inline-flex items-center gap-0.5 rounded px-1 py-0.5 font-mono text-[11px] tabular',
        tone,
      )}
    >
      <Arrow className="size-3" />
      {formatDelta(d)}
    </span>
  )
}

export function Confidence({ value, showLabel = true }: { value?: number; showLabel?: boolean }) {
  if (value == null) return <span className="text-xs text-muted-foreground/60">—</span>
  const tone = value >= 0.8 ? 'bg-success' : value >= 0.6 ? 'bg-warning' : 'bg-destructive'
  return (
    <Tooltip
      content={`Confidence ${formatPercent(value)} — data coverage, stability and risk flags combined by the hub.`}
    >
      <span className="inline-flex items-center gap-2">
        <span className="relative h-1.5 w-12 overflow-hidden rounded-full bg-muted">
          <span
            className={cn('absolute inset-y-0 left-0 rounded-full', tone)}
            style={{ width: `${Math.round(value * 100)}%` }}
          />
        </span>
        {showLabel ? (
          <span className="text-xs tabular text-muted-foreground">{formatPercent(value)}</span>
        ) : null}
      </span>
    </Tooltip>
  )
}
