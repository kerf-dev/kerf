import {
  CheckCircle2Icon,
  ChevronRightIcon,
  CircleDashedIcon,
  CircleDotIcon,
  ExternalLinkIcon,
  InfoIcon,
  TriangleAlertIcon,
  XCircleIcon,
} from 'lucide-react'
import { useState } from 'react'
import type {
  AgentCapability,
  ReadinessCategory,
  ReadinessCheck,
  ReadinessState,
  ReadinessStatus,
  ReadinessSummary,
} from '@/api/types'
import { READINESS_STATES } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { CodeBlock } from './common'

/** Readiness model (D-025, docs/contracts/readiness.md). */
export const STATE_INFO: Record<ReadinessState, { label: string; desc: string }> = {
  Registered: { label: 'Registered', desc: 'Known to the hub; no agent has connected yet.' },
  AgentConnected: { label: 'Agent connected', desc: 'The agent pulls its config and reports status.' },
  MetricsOK: { label: 'Metrics OK', desc: 'The datasource has the series recommendations need.' },
  ReadyAudit: { label: 'Ready (audit)', desc: 'Recommendations are produced; workloads stay in audit.' },
  AutomationReady: {
    label: 'Automation ready',
    desc: 'Every write capability is in place: auto-patch is allowed.',
  },
}

export const stateRank = (s: ReadinessState) => READINESS_STATES.indexOf(s)

/** Titles from the check catalog (api/v1alpha1/readiness). Unknown ids fall back to the id. */
export const CHECK_TITLES: Record<string, string> = {
  'agent.connected': 'Agent has pulled from the hub',
  'agent.version': 'Agent and hub versions are compatible',
  'agent.crds': 'CRDs installed and current',
  'agent.clock-skew': 'Clock skew to the hub under 30 s',
  'datasource.reachable': 'Datasource reachable and authenticated',
  'metrics.cluster-label': 'Cluster label matches series',
  'metrics.cadvisor': 'cAdvisor CPU and memory working set',
  'metrics.ksm-owners': 'kube-state-metrics owner series',
  'metrics.ksm-resources': 'kube-state-metrics requests and limits',
  'metrics.freshness': 'Data fresh (last sample under 5 min)',
  'metrics.owner-join': 'Pod to owner join coverage at least 95 %',
  'metrics.scrape-interval': 'Scrape resolution 60 s or finer',
  'metrics.history': 'History covers the profile window',
  'metrics.throttling': 'CPU throttling series',
  'metrics.restarts-oom': 'Restart and OOM series',
  'metrics.hpa': 'HPA series',
  'metrics.node-labels': 'Node labels allowlisted in kube-state-metrics',
  'metrics.node-allocatable': 'Node allocatable series',
  'metrics.dcgm': 'NVIDIA DCGM GPU series',
  'pricing.coverage': 'Nodes priced',
  'metrics.recommendations': 'Recommendations produced',
  'rbac.read': 'RBAC: read workloads, pods, HPAs, nodes',
  'rbac.resize': 'RBAC: patch pods/resize',
  'rbac.rollout': 'RBAC: patch deployments, statefulsets, daemonsets',
  'rbac.hpa': 'RBAC: patch HPAs',
  'rbac.evict': 'RBAC: create pods/eviction',
  'rbac.policies': 'RBAC: write rightsizingpolicies',
  'rbac.events': 'RBAC: create events',
  'apply.in-place-resize': 'In-place resize available',
  'webhook.healthy': 'Webhook installed and healthy',
  'webhook.cert-manager': 'cert-manager present',
  'nodes.in-place-memory': 'Nodes without swap or static managers',
  'integration.argocd': 'Argo CD ignores Kerf fields',
  'policy.namespace-coverage': 'Namespace coverage',
}
export const checkTitle = (id: string) => CHECK_TITLES[id] ?? id

export const CATEGORY_LABEL: Record<ReadinessCategory, string> = {
  agent: 'Agent',
  datasource: 'Datasource',
  metrics: 'Metrics',
  pricing: 'Pricing',
  rbac: 'Permissions (RBAC)',
  apply: 'Applying changes',
  webhook: 'Admission webhook',
  nodes: 'Nodes',
  integration: 'Integrations',
  policy: 'Policies',
}
const CATEGORY_ORDER: ReadinessCategory[] = [
  'agent',
  'datasource',
  'metrics',
  'pricing',
  'rbac',
  'apply',
  'webhook',
  'nodes',
  'integration',
  'policy',
]

/** A check that needs attention: a blocking check not passing, or a warning that warns/fails. */
export function isProblem(c: ReadinessCheck) {
  if (c.severity === 'blocking') return c.status === 'fail' || c.status === 'warn'
  if (c.severity === 'warning') return c.status === 'warn' || c.status === 'fail'
  return false
}

/**
 * A blocking check without a result yet (`unknown`, counted in `summary.unknown`, not in
 * `summary.blocking`). It still keeps the cluster from the state it gates, but it is not a failure:
 * typically the agent has not sent its self-checks yet.
 */
export function isPending(c: ReadinessCheck) {
  return c.severity === 'blocking' && c.status === 'unknown'
}

/** States the blocking checks gate (api/v1alpha1/readiness catalog). */
export const CHECK_GATES: Record<string, ReadinessState> = {
  'agent.connected': 'AgentConnected',
  'agent.version': 'AgentConnected',
  'agent.crds': 'AgentConnected',
  'rbac.read': 'AgentConnected',
  'datasource.reachable': 'MetricsOK',
  'metrics.cluster-label': 'MetricsOK',
  'metrics.cadvisor': 'MetricsOK',
  'metrics.ksm-owners': 'MetricsOK',
  'metrics.ksm-resources': 'MetricsOK',
  'metrics.freshness': 'MetricsOK',
  'metrics.recommendations': 'ReadyAudit',
  'rbac.resize': 'AutomationReady',
  'rbac.rollout': 'AutomationReady',
  'rbac.hpa': 'AutomationReady',
  'rbac.evict': 'AutomationReady',
  'rbac.policies': 'AutomationReady',
  'rbac.events': 'AutomationReady',
}

/**
 * Adds a pending row for every id in `blockedBy` that has no result in `checks` (the hub lists
 * blocking checks that have not reported yet there, but not in `checks`).
 */
export function withPending(checks: ReadinessCheck[], blockedBy?: string[]): ReadinessCheck[] {
  const have = new Set(checks.map((c) => c.id))
  const missing = (blockedBy ?? []).filter((id) => !have.has(id))
  if (!missing.length) return checks
  return [
    ...checks,
    ...missing.map((id): ReadinessCheck => ({
      id,
      category: (CATEGORY_ORDER.find((c) => id.startsWith(`${c}.`)) ?? 'agent') as ReadinessCategory,
      severity: 'blocking',
      source:
        id.startsWith('agent.') && id !== 'agent.connected'
          ? 'agent'
          : id.startsWith('rbac.')
            ? 'agent'
            : 'hub',
      gates: CHECK_GATES[id],
      status: 'unknown',
      message:
        id.startsWith('rbac.') || (id.startsWith('agent.') && id !== 'agent.connected')
          ? 'Not reported yet: the agent sends this with its self-checks.'
          : 'Not reported yet.',
      checkedAt: '',
    })),
  ]
}

/** Cluster list badge, as in the plan: ✓ Automation ready · ● Ready (audit) · ▲ N warnings · ✕ N blocking. */
export function ReadinessBadge({ summary, compact }: { summary?: ReadinessSummary; compact?: boolean }) {
  if (!summary) return null
  const info = STATE_INFO[summary.state]
  const tip = (
    <div className="space-y-0.5">
      <div className="font-medium">{info.label}</div>
      <div className="text-muted-foreground">{info.desc}</div>
      {summary.blocking ? <div>{summary.blocking} blocking check(s) not passing</div> : null}
      {summary.warnings ? <div>{summary.warnings} warning(s)</div> : null}
      {summary.checkedAt ? (
        <div className="text-muted-foreground">Checked {relativeTime(summary.checkedAt)}</div>
      ) : null}
    </div>
  )
  const state =
    summary.state === 'AutomationReady' ? (
      <Badge variant="success" className="gap-1">
        <CheckCircle2Icon /> {info.label}
      </Badge>
    ) : summary.state === 'ReadyAudit' ? (
      <Badge variant="info" className="gap-1">
        <CircleDotIcon /> {info.label}
      </Badge>
    ) : (
      <Badge variant="muted" className="gap-1">
        <CircleDashedIcon /> {info.label}
      </Badge>
    )
  return (
    <Tooltip content={tip}>
      <span className="inline-flex flex-wrap items-center gap-1" data-testid="readiness-badge">
        {state}
        {summary.blocking ? (
          <Badge variant="destructive" className="gap-1">
            <XCircleIcon /> {summary.blocking}
            {compact ? '' : ' blocking'}
          </Badge>
        ) : null}
        {summary.warnings ? (
          <Badge variant="warning" className="gap-1">
            <TriangleAlertIcon /> {summary.warnings}
            {compact ? '' : summary.warnings === 1 ? ' warning' : ' warnings'}
          </Badge>
        ) : null}
      </span>
    </Tooltip>
  )
}

/** Registered → Agent connected → Metrics OK → Ready (audit) → Automation ready. */
export function ReadinessStepper({
  state,
  nextState,
  blockedBy,
  className,
}: {
  state: ReadinessState
  nextState?: ReadinessState
  blockedBy?: string[]
  className?: string
}) {
  const cur = stateRank(state)
  return (
    <ol
      className={cn('grid grid-cols-5 gap-1.5', className)}
      aria-label="Readiness state"
      data-testid="readiness-stepper"
    >
      {READINESS_STATES.map((s, i) => {
        const done = i <= cur
        const next = s === nextState
        return (
          <li key={s} className="min-w-0" aria-current={i === cur ? 'step' : undefined}>
            <div
              className={cn(
                'h-1.5 rounded-full',
                done
                  ? s === 'AutomationReady'
                    ? 'bg-success'
                    : 'bg-primary'
                  : next
                    ? 'bg-warning/60'
                    : 'bg-muted',
              )}
            />
            <div
              className={cn(
                'mt-2 truncate text-xs font-medium',
                done ? 'text-foreground' : 'text-muted-foreground',
              )}
            >
              {STATE_INFO[s].label}
            </div>
            {next && blockedBy?.length ? (
              <div
                className="mt-0.5 truncate text-[11px] text-muted-foreground"
                title={blockedBy.map(checkTitle).join(', ')}
              >
                Blocked by {blockedBy.length === 1 ? checkTitle(blockedBy[0]!) : `${blockedBy.length} checks`}
              </div>
            ) : null}
          </li>
        )
      })}
    </ol>
  )
}

export function StatusIcon({
  status,
  severity,
}: {
  status: ReadinessStatus
  severity: ReadinessCheck['severity']
}) {
  if (status === 'pass') return <CheckCircle2Icon className="size-4 text-success" aria-label="pass" />
  if (status === 'unknown')
    return <CircleDashedIcon className="size-4 text-muted-foreground" aria-label="unknown" />
  if (status === 'fail' && severity === 'blocking')
    return <XCircleIcon className="size-4 text-destructive" aria-label="fail" />
  if (severity === 'info') return <InfoIcon className="size-4 text-info" aria-label={status} />
  return (
    <TriangleAlertIcon
      className="size-4 text-[color-mix(in_oklch,var(--warning)_80%,var(--foreground))]"
      aria-label={status}
    />
  )
}

function CheckRow({ check, capability }: { check: ReadinessCheck; capability?: AgentCapability }) {
  const problem = isProblem(check)
  const pending = isPending(check)
  const [open, setOpen] = useState(problem)
  const expandable = !!(check.remediation || check.details || capability?.missing?.length || check.docsURL)
  return (
    <li
      className={cn('border-b last:border-0', problem && 'bg-destructive/[0.025]')}
      data-testid={`check-${check.id}`}
    >
      <button
        type="button"
        className="flex w-full items-start gap-3 px-4 py-2.5 text-left outline-none focus-visible:bg-accent/50 disabled:cursor-default"
        onClick={() => expandable && setOpen((o) => !o)}
        aria-expanded={expandable ? open : undefined}
        disabled={!expandable}
      >
        <span className="mt-0.5 shrink-0">
          <StatusIcon status={check.status} severity={check.severity} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <span className="text-[13px] font-medium">{checkTitle(check.id)}</span>
            {check.severity === 'blocking' ? (
              <Badge variant={problem ? 'destructive' : 'muted'} className="text-[10px]">
                blocking{check.gates ? ` · ${STATE_INFO[check.gates].label}` : ''}
                {pending ? ' · pending' : ''}
              </Badge>
            ) : check.severity === 'info' ? (
              <Badge variant="muted" className="text-[10px]">
                info
              </Badge>
            ) : null}
          </span>
          <span className="mt-0.5 block text-[13px] text-pretty text-muted-foreground">{check.message}</span>
        </span>
        <span
          className="hidden shrink-0 text-[11px] text-muted-foreground sm:block"
          title={check.checkedAt ? formatDateTime(check.checkedAt) : undefined}
        >
          {check.source} · {check.checkedAt ? relativeTime(check.checkedAt) : 'waiting'}
        </span>
        {expandable ? (
          <ChevronRightIcon
            className={cn(
              'mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform',
              open && 'rotate-90',
            )}
          />
        ) : (
          <span className="size-4 shrink-0" />
        )}
      </button>
      {open && expandable ? (
        <div className="flex flex-col gap-2.5 pr-4 pb-3.5 pl-11">
          {capability?.missing?.length ? (
            <div className="text-xs">
              <div className="mb-1 font-medium">ClusterRole rules to add</div>
              <CodeBlock code={capability.missing.map(prettyRule).join('\n')} maxHeight={160} />
            </div>
          ) : null}
          {check.remediation ? (
            <div className="text-xs">
              <div className="mb-1 font-medium">
                {check.remediationType === 'command'
                  ? 'Run'
                  : check.remediationType === 'yaml'
                    ? 'Add to your values'
                    : 'Fix'}
              </div>
              {check.remediationType === 'text' ? (
                <p className="text-[13px] text-muted-foreground">{check.remediation}</p>
              ) : (
                <CodeBlock code={check.remediation} maxHeight={220} />
              )}
            </div>
          ) : null}
          {check.details && Object.keys(check.details).length ? (
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
              {Object.entries(check.details).map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="font-mono break-all">{v}</dd>
                </div>
              ))}
            </dl>
          ) : null}
          {check.docsURL ? (
            <a
              href={check.docsURL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex w-fit items-center gap-1 text-xs font-medium text-primary hover:underline"
            >
              Documentation <ExternalLinkIcon className="size-3" />
            </a>
          ) : null}
        </div>
      ) : null}
    </li>
  )
}

function prettyRule(json: string) {
  try {
    const r = JSON.parse(json) as { apiGroups?: string[]; resources?: string[]; verbs?: string[] }
    return `- apiGroups: [${(r.apiGroups ?? []).map((g) => JSON.stringify(g)).join(', ')}]\n  resources: [${(r.resources ?? []).join(', ')}]\n  verbs: [${(r.verbs ?? []).join(', ')}]`
  } catch {
    return json
  }
}

/** The checklist grouped by category; failed checks start expanded with their remediation. */
export function ReadinessChecklist({
  checks,
  capabilities,
  onlyProblems,
  blockedBy,
}: {
  checks: ReadinessCheck[]
  capabilities?: AgentCapability[]
  onlyProblems?: boolean
  /** `ClusterReadiness.blockedBy`: blocking checks without a result get a pending row. */
  blockedBy?: string[]
}) {
  const all = withPending(checks, blockedBy)
  const list = onlyProblems ? all.filter(isProblem) : all
  const groups = CATEGORY_ORDER.map((cat) => ({ cat, items: list.filter((c) => c.category === cat) }))
    .filter((g) => g.items.length)
    // Categories that need attention first, the rest in catalog order.
    .sort((a, b) => Number(b.items.some(isProblem)) - Number(a.items.some(isProblem)))
  const capOf = (id: string) => capabilities?.find((c) => `rbac.${c.name}` === id)
  return (
    <div className="flex flex-col gap-4">
      {groups.map((g) => {
        const bad = g.items.filter(isProblem).length
        const waiting = g.items.filter(isPending).length
        return (
          <section key={g.cat} className="overflow-hidden rounded-xl border bg-card">
            <header className="flex items-center justify-between border-b bg-muted/30 px-4 py-2">
              <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
                {CATEGORY_LABEL[g.cat]}
              </h3>
              <span className="text-xs text-muted-foreground">
                {bad ? `${bad} need attention · ` : ''}
                {waiting ? `${waiting} not reported yet · ` : ''}
                {g.items.filter((c) => c.status === 'pass').length}/{g.items.length} passing
              </span>
            </header>
            <ul>
              {g.items.map((c) => (
                <CheckRow key={c.id} check={c} capability={capOf(c.id)} />
              ))}
            </ul>
          </section>
        )
      })}
    </div>
  )
}
