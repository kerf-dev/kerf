/**
 * MSW handlers for the admin area (docs/contracts/admin.md, readiness.md): auth and sessions,
 * settings, policies, preview, effective policy, overrides, approvals, clusters and automation,
 * tokens, users, config audit, datasources, readiness and install snippets. Every response is
 * validated against api/openapi/hub.yaml in admin.test.ts.
 */
import { http, HttpResponse } from 'msw'
import type { components } from '@/api/schema'
import type { ClusterInfo, WorkloadDetail, WorkloadHistory } from '@/api/types'
import { iso, type MockDb, type WorkloadModel } from './data'
import {
  audit,
  currentSet,
  defaultBinding,
  evaluate,
  obj,
  policyTarget,
  recompute,
  recordVersion,
  stripApproval,
  type AdminPolicy,
  type AdminState,
  type ApprovalRec,
  type ClusterMeta,
  type DatasourceRec,
  type Mode,
  type PolicySet,
  type Spec,
  type UserRec,
  type UserRole,
} from './admin-state'
import {
  badRequest,
  body,
  bump,
  conflict,
  dec,
  enc,
  forbidden,
  gate,
  ifMatch,
  json,
  loginFailures,
  newSession,
  notFound,
  problem,
  reasonError,
  readSession,
  roleAtLeast,
  unauthorized,
  writeSession,
  type Ctx,
  installUnconfigured,
} from './core'
import { versionCompat } from '@/lib/version'
import { mulberry32, uuid } from './random'
import {
  buildReadiness,
  computeState,
  installSnippet,
  metricsCheckResults,
  readinessSummary,
  toChecks,
  toClusterReadiness,
  type InstallFormat,
} from './readiness'
import { overridePolicyName } from './sha256'

type S = components['schemas']

const now = () => iso(Date.now())
const rnd = () => mulberry32((Date.now() ^ Math.floor(Math.random() * 2 ** 31)) >>> 0)
const newId = () => uuid(rnd())
const isLive = (p: { deletedAt?: string }) => !p.deletedAt

// ---------------------------------------------------------------------------------------------
// Projections
// ---------------------------------------------------------------------------------------------

function liveCluster(st: AdminState, name: string | readonly string[] | undefined) {
  const c = st.clusters.get(String(name))
  return c && !c.deletedAt ? c : undefined
}

export function readinessState(st: AdminState, cluster: string) {
  const m = st.readiness.get(cluster)
  return m ? computeState(toChecks(m)) : 'Registered'
}

const effPaused = (st: AdminState, c: ClusterMeta) => c.automation.paused || st.fleet.paused

function configSync(c: ClusterMeta, info?: ClusterInfo): S['AgentConfigSync'] {
  const applied = c.appliedConfigVersion
  return {
    ...(c.agentVersion ? { agentVersion: c.agentVersion } : {}),
    ...(info?.lastHeartbeatAt ? { lastHeartbeatAt: info.lastHeartbeatAt } : {}),
    ...(c.agentConnected ? { lastConfigPullAt: iso(Date.now() - 14_000) } : {}),
    configVersion: `c-${c.configVersion}`,
    ...(applied != null ? { appliedConfigVersion: `c-${applied}` } : {}),
    inSync: applied === c.configVersion,
  }
}

function inventory(st: AdminState, db: MockDb, cluster: string): S['ClusterPolicyInventory'] {
  const c = st.clusters.get(cluster)
  if (!c || !c.agentConnected) return { cluster, items: [] }
  const ws = db.workloads.filter((w) => w.cluster === cluster)
  const evals = ws.map((w) => evaluate(st, w))
  const items: S['PolicyInventoryItem'][] = []
  if (c.policySource !== 'git') {
    for (const p of st.policies.filter(isLive)) {
      if (p.clusters.length && !p.clusters.includes(cluster)) continue
      const matched =
        p.type === 'policy'
          ? evals.filter((e) => e.policy === p.name).length
          : p.type === 'workload-override'
            ? evals.filter((e) => e.override === p.name).length
            : ws.length
      items.push({
        name: p.name,
        source: 'hub',
        priority: p.priority,
        generation: p.version,
        valid: true,
        hubPolicyId: p.id,
        hubVersion: p.version,
        hubPolicyType: p.type,
        matchedWorkloads: matched,
        spec: p.spec,
      })
    }
  }
  for (const g of st.gitPolicies.filter((g) => g.clusters.includes(cluster)))
    items.push({
      name: g.name,
      source: 'git',
      priority: g.priority,
      generation: g.generation,
      valid: true,
      matchedWorkloads: evals.filter((e) => e.policy === g.name).length,
      spec: g.spec,
    })
  items.sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name))
  return {
    cluster,
    reportedAt: iso(Date.now() - 14_000),
    ...(c.appliedConfigVersion != null ? { configVersion: `c-${c.appliedConfigVersion}` } : {}),
    items,
  }
}

function clusterAdmin(st: AdminState, db: MockDb, c: ClusterMeta): S['ClusterAdmin'] {
  const info = db.clusters.find((x) => x.name === c.name)
  const m = st.readiness.get(c.name)
  return {
    name: c.name,
    description: c.description,
    environment: c.environment,
    owner: c.owner,
    policySource: c.policySource,
    createdAt: c.createdAt,
    ...(c.updatedAt ? { updatedAt: c.updatedAt } : {}),
    ...(c.updatedBy ? { updatedBy: c.updatedBy } : {}),
    ...(c.deletedAt ? { deletedAt: c.deletedAt } : {}),
    version: c.version,
    automation: c.automation,
    paused: effPaused(st, c),
    agent: configSync(c, info),
    policyInventory: inventory(st, db, c.name),
    ...(c.provider ? { provider: c.provider } : {}),
    ...(c.region ? { region: c.region } : {}),
    metrics: c.metrics,
    readiness: m ? readinessSummary(m) : { state: 'Registered', blocking: 0, warnings: 0 },
  }
}

function datasourceOut(st: AdminState, d: DatasourceRec): S['Datasource'] {
  const out: S['Datasource'] & { knownLabelValues?: string[]; deletedAt?: string } = { ...d }
  delete out.knownLabelValues
  delete out.deletedAt
  out.usedBy = [...st.clusters.values()]
    .filter((c) => !c.deletedAt && c.metrics.datasourceId === d.id)
    .map((c) => c.name)
    .sort()
  return out
}

export function userOut(u: UserRec): S['User'] {
  const out: S['User'] & Partial<Pick<UserRec, 'password' | 'failedLogins' | 'lockedUntilMs'>> = { ...u }
  delete out.password
  delete out.failedLogins
  delete out.lockedUntilMs
  if (u.lockedUntilMs && u.lockedUntilMs > Date.now()) out.lockedUntil = iso(u.lockedUntilMs)
  else delete out.lockedUntil
  return out
}

/** Additive fields of `GET /v1/clusters` (admin area, readiness). Counts follow the current modes. */
export function decorateClusters(st: AdminState, db: MockDb, items: ClusterInfo[]): ClusterInfo[] {
  return items.map((c) => {
    const meta = st.clusters.get(c.name)
    if (!meta) return c
    const ws = db.workloads.filter((w) => w.cluster === c.name)
    const m = st.readiness.get(c.name)
    return {
      ...c,
      environment: meta.environment,
      ...(meta.owner ? { owner: meta.owner } : {}),
      policySource: meta.policySource,
      paused: effPaused(st, meta),
      automation: meta.automation,
      configSync: configSync(meta, c),
      ...(m ? { readiness: readinessSummary(m) } : {}),
      counts: {
        workloads: ws.length,
        withRecommendation: ws.filter((w) => w.hasRecommendation).length,
        autoPatch: ws.filter((w) => w.modes.cpu === 'auto-patch' || w.modes.memory === 'auto-patch').length,
        blocked: ws.filter((w) => w.phase === 'Blocked').length,
      },
    }
  })
}

const liveOverride = (st: AdminState, workloadId: string) =>
  st.policies.find((p) => p.type === 'workload-override' && p.workloadId === workloadId && isLive(p))

function latestApproval(st: AdminState, workloadId: string) {
  return st.approvals
    .filter((a) => a.workloadId === workloadId)
    .sort((a, b) => b.approvedAt.localeCompare(a.approvedAt))[0]
}

export function decorateDetail(st: AdminState, d: WorkloadDetail): WorkloadDetail {
  const ov = liveOverride(st, d.id)
  const ap = latestApproval(st, d.id)
  return {
    ...d,
    ...(ap ? { approval: stripApproval(ap) } : {}),
    ...(ov ? { override: ov } : {}),
    ...(d.status && ov ? { status: { ...d.status, override: ov.name } } : {}),
  }
}

export function decorateHistory(st: AdminState, h: WorkloadHistory): WorkloadHistory {
  return {
    ...h,
    configChanges: st.audit.filter((c) => c.target.workloadId === h.workloadId).reverse(),
  }
}

// ---------------------------------------------------------------------------------------------
// Validation (a subset of api/v1alpha1/validate, enough for useful UI messages)
// ---------------------------------------------------------------------------------------------

const DURATION = /^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$/
const RATIO01 = /^(0(\.[0-9]{1,3})?|1(\.0{1,3})?)$/
const QUANTITY = /^[0-9]+(\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$/
const DNS1123 = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
const POLICY_NAME = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/
const LABEL_KEY = /^([a-z0-9]([-a-z0-9.]*[a-z0-9])?\/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
const MODES: Mode[] = ['off', 'audit', 'auto-patch']
const KINDS = ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob']

function validateWindows(ws: S['TimeWindow'][] | undefined, path: string, errs: string[]) {
  ws?.forEach((w, i) => {
    const p = `${path}[${i}]`
    const recurring = w.schedule != null || w.duration != null
    const oneOff = w.start != null || w.end != null
    if (recurring && oneOff) errs.push(`${p}: Invalid value: either schedule + duration or start + end`)
    else if (recurring) {
      if (!w.schedule || w.schedule.trim().split(/\s+/).length !== 5)
        errs.push(
          `${p}.schedule: Invalid value: ${JSON.stringify(w.schedule ?? '')}: expected a 5-field cron expression`,
        )
      if (!w.duration || !DURATION.test(w.duration))
        errs.push(`${p}.duration: Required value: a duration such as 2h`)
    } else if (oneOff) {
      if (!w.start || !w.end) errs.push(`${p}: Required value: start and end`)
      else if (Date.parse(w.end) <= Date.parse(w.start))
        errs.push(`${p}.end: Invalid value: must be after start`)
    } else errs.push(`${p}: Required value: either schedule + duration or start + end`)
  })
}

export function validateSpec(spec: Spec | undefined, errs: string[] = [], path = 'spec'): string[] {
  if (!spec || typeof spec !== 'object') {
    errs.push(`${path}: Required value`)
    return errs
  }
  const sel = spec.selector
  if (sel) {
    for (const k of ['namespaces', 'excludeNamespaces'] as const)
      sel[k]?.forEach((g, i) => {
        if (!g || !/^[a-z0-9*?]([-a-z0-9*?]*)?$/.test(g))
          errs.push(
            `${path}.selector.${k}[${i}]: Invalid value: ${JSON.stringify(g)}: not a namespace or glob`,
          )
      })
    for (const [k, v] of Object.entries(sel.matchLabels ?? {}))
      if (!LABEL_KEY.test(k) || typeof v !== 'string' || v.length > 63)
        errs.push(`${path}.selector.matchLabels: Invalid value: ${JSON.stringify(k)}`)
    sel.kinds?.forEach((k, i) => {
      if (!KINDS.includes(k))
        errs.push(`${path}.selector.kinds[${i}]: Unsupported value: ${JSON.stringify(k)}`)
    })
    sel.workloads?.forEach((w, i) => {
      if (!KINDS.includes(w.kind) || !w.namespace || !w.name)
        errs.push(`${path}.selector.workloads[${i}]: Invalid value: kind, namespace and name are required`)
    })
  }
  if (spec.profile && !['dev', 'production', 'high-availability'].includes(spec.profile))
    errs.push(`${path}.profile: Unsupported value: ${JSON.stringify(spec.profile)}`)
  if (spec.cpu) {
    if (spec.cpu.mode && !MODES.includes(spec.cpu.mode))
      errs.push(`${path}.cpu.mode: Unsupported value: ${JSON.stringify(spec.cpu.mode)}`)
    if (spec.cpu.limits && !['keep', 'remove', 'ratio'].includes(spec.cpu.limits))
      errs.push(`${path}.cpu.limits: Unsupported value: ${JSON.stringify(spec.cpu.limits)}`)
    if (spec.cpu.limits === 'ratio' && !spec.cpu.limitRatio)
      errs.push(`${path}.cpu.limitRatio: Required value: required when limits is ratio`)
    if (spec.cpu.limits !== 'ratio' && spec.cpu.limitRatio)
      errs.push(`${path}.cpu.limitRatio: Forbidden: only allowed when limits is ratio`)
    if (spec.cpu.limitRatio && !/^([1-9][0-9]*)(\.[0-9]{1,3})?$/.test(spec.cpu.limitRatio))
      errs.push(
        `${path}.cpu.limitRatio: Invalid value: ${JSON.stringify(spec.cpu.limitRatio)}: a number ≥ 1 such as 1.5`,
      )
  }
  if (spec.memory?.mode && !MODES.includes(spec.memory.mode))
    errs.push(`${path}.memory.mode: Unsupported value: ${JSON.stringify(spec.memory.mode)}`)
  const a = spec.apply
  if (a) {
    if (a.strategy && !['in-place', 'rollout', 'deferred'].includes(a.strategy))
      errs.push(`${path}.apply.strategy: Unsupported value: ${JSON.stringify(a.strategy)}`)
    if (a.fallbackAfter && !DURATION.test(a.fallbackAfter))
      errs.push(
        `${path}.apply.fallbackAfter: Invalid value: ${JSON.stringify(a.fallbackAfter)}: a duration such as 10m`,
      )
    validateWindows(a.maintenanceWindows, `${path}.apply.maintenanceWindows`, errs)
    validateWindows(a.freezeWindows, `${path}.apply.freezeWindows`, errs)
    if (a.strategy === 'deferred' && !a.maintenanceWindows?.length)
      errs.push(`${path}.apply.maintenanceWindows: Required value: required when strategy is deferred`)
  }
  const g = spec.guardrails
  if (g) {
    for (const k of ['learningPeriod', 'cooldown'] as const)
      if (g[k] && !DURATION.test(g[k]))
        errs.push(`${path}.guardrails.${k}: Invalid value: ${JSON.stringify(g[k])}: a duration such as 24h`)
    if (g.minConfidence && !RATIO01.test(g.minConfidence))
      errs.push(
        `${path}.guardrails.minConfidence: Invalid value: ${JSON.stringify(g.minConfidence)}: between 0 and 1, at most 3 decimals`,
      )
    if (
      g.maxChangePercent != null &&
      !(Number.isInteger(g.maxChangePercent) && g.maxChangePercent >= 1 && g.maxChangePercent <= 100)
    )
      errs.push(`${path}.guardrails.maxChangePercent: Invalid value: ${g.maxChangePercent}: an integer 1–100`)
    for (const k of ['minCPU', 'minMemory'] as const)
      if (g[k] && !QUANTITY.test(g[k]))
        errs.push(
          `${path}.guardrails.${k}: Invalid value: ${JSON.stringify(g[k])}: a quantity such as 10m or 32Mi`,
        )
    const rb = g.rollback
    if (rb) {
      if (rb.watchDuration && !DURATION.test(rb.watchDuration))
        errs.push(
          `${path}.guardrails.rollback.watchDuration: Invalid value: ${JSON.stringify(rb.watchDuration)}`,
        )
      for (const k of ['oomKills', 'restarts'] as const)
        if (rb[k] != null && !(Number.isInteger(rb[k]) && rb[k] >= 1))
          errs.push(`${path}.guardrails.rollback.${k}: Invalid value: ${rb[k]}: an integer ≥ 1`)
      if (rb.throttlingRatio && !RATIO01.test(rb.throttlingRatio))
        errs.push(
          `${path}.guardrails.rollback.throttlingRatio: Invalid value: ${JSON.stringify(rb.throttlingRatio)}: between 0 and 1`,
        )
    }
  }
  const b = spec.hpa?.targetBounds
  if (b) {
    for (const k of ['minUtilization', 'maxUtilization'] as const)
      if (b[k] != null && !(Number.isInteger(b[k]) && b[k] >= 1 && b[k] <= 100))
        errs.push(`${path}.hpa.targetBounds.${k}: Invalid value: ${b[k]}: an integer 1–100`)
    if (b.minUtilization != null && b.maxUtilization != null && b.minUtilization > b.maxUtilization)
      errs.push(`${path}.hpa.targetBounds: Invalid value: minUtilization must not exceed maxUtilization`)
  }
  return errs
}

function validatePolicyInput(st: AdminState, p: S['PolicyInput'] | S['PolicyDraft'], errs: string[] = []) {
  if (!['global-default', 'cluster-default', 'policy', 'workload-override'].includes(p.type))
    errs.push(`type: Unsupported value: ${JSON.stringify(p.type)}`)
  if (p.type === 'policy') {
    if (!p.name) errs.push('name: Required value')
    else if (!POLICY_NAME.test(p.name) || p.name.length > 253)
      errs.push(`name: Invalid value: ${JSON.stringify(p.name)}: lower-case letters, digits, '-' and '.'`)
    else if (p.name.startsWith('ro-'))
      errs.push(`name: Invalid value: ${JSON.stringify(p.name)}: the prefix ro- is reserved`)
    const pr = p.spec?.priority
    if (pr == null) errs.push('spec.priority: Required value')
    else if (!(Number.isInteger(pr) && pr >= 1 && pr <= 899999))
      errs.push(`spec.priority: Invalid value: ${pr}: policies use 1–899999`)
  }
  if (p.type === 'global-default' && p.clusters?.length)
    errs.push('clusters: Forbidden: global defaults apply to every cluster')
  if (p.type === 'cluster-default' && p.clusters?.length !== 1)
    errs.push('clusters: Invalid value: exactly one cluster')
  if (
    (p.type === 'global-default' || p.type === 'cluster-default') &&
    p.spec?.selector &&
    Object.keys(p.spec.selector).length
  )
    errs.push('spec.selector: Forbidden: defaults have no selector')
  for (const c of p.clusters ?? [])
    if (!liveCluster(st, c)) errs.push(`clusters: Not found: ${JSON.stringify(c)}`)
  validateSpec(p.spec, errs)
  return errs
}

const invalid = (code: string, errs: string[]) => badRequest(code, errs.join('; '))

const setsAutoPatch = (spec: Spec | undefined) =>
  spec?.cpu?.mode === 'auto-patch' || spec?.memory?.mode === 'auto-patch'

function targetClusters(st: AdminState, clusters: string[] | undefined) {
  return clusters?.length
    ? clusters
    : [...st.clusters.values()].filter((c) => !c.deletedAt).map((c) => c.name)
}

/**
 * D-025 gating: auto-patch only on AutomationReady clusters, unless an admin sends
 * overrideReadiness. An operator sending the flag gets 403 admin_required.
 */
function readinessGate(ctx: Ctx, clusters: string[], spec: Spec | undefined, overrideReadiness?: boolean) {
  if (overrideReadiness && ctx.who.role !== 'admin')
    return forbidden('admin_required', 'only an admin can override readiness')
  if (!setsAutoPatch(spec) || overrideReadiness) return undefined
  const notReady = clusters
    .map((c) => ({ c, s: readinessState(ctx.st, c) }))
    .filter((x) => x.s !== 'AutomationReady')
  if (!notReady.length) return undefined
  return conflict(
    'cluster_not_automation_ready',
    `auto-patch needs AutomationReady clusters: ${notReady.map((x) => `${x.c} (${x.s})`).join(', ')}`,
  )
}

// ---------------------------------------------------------------------------------------------
// Workloads
// ---------------------------------------------------------------------------------------------

function decodeId(raw: string | readonly string[] | undefined): string {
  const s = Array.isArray(raw) ? raw.join('/') : String(raw ?? '')
  return s.includes('%') ? decodeURIComponent(s) : s
}
function findWorkload(db: MockDb, raw: string | readonly string[] | undefined): WorkloadModel | Response {
  const id = decodeId(raw)
  if (
    !/^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\/[^/]+\/(Deployment|StatefulSet|DaemonSet|CronJob)\/[^/]+$/.test(id)
  )
    return badRequest('invalid_workload_id', `invalid workload id ${JSON.stringify(id)}`)
  return db.byId.get(id) ?? notFound('workload_not_found', `workload ${id} not found`)
}

const overrideSelector = (w: WorkloadModel): S['PolicySelector'] => ({
  namespaces: [w.namespace],
  kinds: [w.kind],
  workloads: [{ kind: w.kind, namespace: w.namespace, name: w.name }],
})

// ---------------------------------------------------------------------------------------------
// Preview
// ---------------------------------------------------------------------------------------------

function draftToPolicy(st: AdminState, db: MockDb, d: S['PolicyDraft'], id: string): AdminPolicy | string {
  const base = {
    id,
    description: d.description,
    version: 1,
    createdAt: now(),
    createdBy: { type: 'system' as const, name: 'preview' },
    updatedAt: now(),
    updatedBy: { type: 'system' as const, name: 'preview' },
  }
  if (d.type === 'workload-override') {
    if (!d.workloadId) return 'policy.workloadId: Required value for a workload-override draft'
    const w = db.byId.get(d.workloadId)
    if (!w) return `policy.workloadId: Not found: ${d.workloadId}`
    return {
      ...base,
      type: d.type,
      name: overridePolicyName(w.namespace, w.kind, w.name),
      priority: 900000,
      clusters: [w.cluster],
      workloadId: w.id,
      spec: { ...d.spec, priority: 900000, selector: overrideSelector(w) },
    }
  }
  const priority =
    d.type === 'global-default' ? 0 : d.type === 'cluster-default' ? 10 : (d.spec.priority ?? 0)
  const name =
    d.type === 'global-default'
      ? 'ro-global-defaults'
      : d.type === 'cluster-default'
        ? 'ro-cluster-defaults'
        : (d.name ?? 'draft')
  void st
  return { ...base, type: d.type, name, priority, clusters: d.clusters ?? [], spec: { ...d.spec, priority } }
}

function runPreview(ctx: Ctx, req: S['PreviewRequest']): S['ImpactPreview'] | Response {
  const { st, db } = ctx
  const warnings: string[] = []
  let hub = st.policies.filter(isLive).map((p) => p)
  for (const [i, ch] of (req.changes ?? []).entries()) {
    if (ch.op === 'delete') {
      if (!ch.policyId)
        return badRequest('invalid_preview', `changes[${i}].policyId: Required value for delete`)
      hub = hub.filter((p) => p.id !== ch.policyId)
      continue
    }
    if (!ch.policy) return badRequest('invalid_preview', `changes[${i}].policy: Required value for upsert`)
    for (const e of validatePolicyInput(st, ch.policy)) warnings.push(`changes[${i}]: ${e}`)
    const existing = ch.policyId ? hub.find((p) => p.id === ch.policyId) : undefined
    if (ch.policyId && !existing)
      warnings.push(`changes[${i}]: policy ${ch.policyId} not found; previewed as new`)
    const p = draftToPolicy(st, db, ch.policy, existing?.id ?? `draft-${i}`)
    if (typeof p === 'string') return badRequest('invalid_preview', `changes[${i}].${p}`)
    hub = hub.filter((x) => {
      if (existing && x.id === existing.id) return false
      if (p.type === 'global-default' && x.type === 'global-default') return false
      if (p.type === 'cluster-default' && x.type === 'cluster-default' && x.clusters[0] === p.clusters[0])
        return false
      if (p.type === 'workload-override' && x.type === 'workload-override' && x.workloadId === p.workloadId)
        return false
      return true
    })
    hub.push(p)
  }
  const after: PolicySet = { hub, git: st.gitPolicies }
  const before = currentSet(st)
  const into = { cpu: 0, memory: 0, total: 0 }
  const out = { cpu: 0, memory: 0, total: 0 }
  const items: S['ImpactItem'][] = []
  let labels = false
  for (const w of db.workloads) {
    const b = evaluate(st, w, before)
    const a = evaluate(st, w, after)
    labels ||= a.labelsSkipped
    if (a.modes.cpu === b.modes.cpu && a.modes.memory === b.modes.memory) continue
    const s = w.hasRecommendation ? w.savings : { cpu: 0, memory: 0, total: 0 }
    for (const r of ['cpu', 'memory'] as const) {
      if (a.modes[r] === 'auto-patch' && b.modes[r] !== 'auto-patch') into[r] += s[r]
      if (b.modes[r] === 'auto-patch' && a.modes[r] !== 'auto-patch') out[r] += s[r]
    }
    items.push({
      workloadId: w.id,
      cluster: w.cluster,
      namespace: w.namespace,
      kind: w.kind,
      name: w.name,
      before: b.modes,
      after: a.modes,
      ...(b.policy ? { policyBefore: b.policy } : {}),
      ...(a.policy || a.override ? { policyAfter: a.override ?? a.policy } : {}),
      ...(w.hasRecommendation ? { monthlySavingsUSD: w.savings } : {}),
    })
  }
  if (labels) warnings.push('workload labels unknown; matchLabels selectors assumed not to match')
  const r2 = (n: number) => Math.round(n * 100) / 100
  const fin = (x: typeof into) => ({ cpu: r2(x.cpu), memory: r2(x.memory), total: r2(x.cpu + x.memory) })
  items.sort(
    (a, b) =>
      (b.monthlySavingsUSD?.total ?? 0) - (a.monthlySavingsUSD?.total ?? 0) ||
      a.workloadId.localeCompare(b.workloadId),
  )
  const limit = req.limit ?? 50
  if (!(limit >= 1 && limit <= 1000)) return badRequest('invalid_limit', 'limit must be 1..1000')
  return {
    workloadsEvaluated: db.workloads.length,
    workloadsChanged: items.length,
    savingsIntoAutoPatchUSD: fin(into),
    savingsOutOfAutoPatchUSD: fin(out),
    items: items.slice(0, limit),
    truncated: items.length > limit,
    ...(warnings.length ? { warnings } : {}),
  }
}

// ---------------------------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------------------------

const PROFILES = ['dev', 'production', 'high-availability']
const sortPolicies = (a: AdminPolicy, b: AdminPolicy) => {
  const order = ['global-default', 'cluster-default', 'policy', 'workload-override']
  return (
    order.indexOf(a.type) - order.indexOf(b.type) || b.priority - a.priority || a.name.localeCompare(b.name)
  )
}

function page<T>(
  items: T[],
  sp: URLSearchParams,
  def = 100,
  max = 1000,
): { items: T[]; continue?: string } | Response {
  const limit = Number(sp.get('limit') ?? def)
  if (!(Number.isInteger(limit) && limit >= 1 && limit <= max))
    return badRequest('invalid_limit', `limit must be 1..${max}`)
  const offset = dec(sp.get('continue'))
  if (offset < 0) return badRequest('invalid_continue', 'invalid continue token')
  return {
    items: items.slice(offset, offset + limit),
    ...(offset + limit < items.length ? { continue: enc(offset + limit) } : {}),
  }
}

function meOut(u: UserRec, s: { csrfToken: string; expiresAt: string }): S['Me'] {
  return {
    authMethod: 'session',
    role: u.role,
    user: userOut(u),
    csrfToken: s.csrfToken,
    sessionExpiresAt: s.expiresAt,
  }
}

function enabledAdmins(st: AdminState) {
  return [...st.users.values()].filter((u) => u.role === 'admin' && !u.disabled && !u.deletedAt)
}

function tokenValue() {
  const r = rnd()
  let s = 'ro-'
  const abc = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
  for (let i = 0; i < 40; i++) s += abc[Math.floor(r() * abc.length)]
  return s
}

function createToken(
  st: AdminState,
  who: Ctx['who'],
  t: Omit<S['TokenCreate'], 'reason'>,
  reason: string,
): S['TokenCreated'] {
  const token: S['TokenInfo'] = {
    id: String(st.nextTokenId++),
    role: t.role,
    ...(t.cluster ? { cluster: t.cluster } : {}),
    description: t.description,
    createdAt: now(),
    createdBy: who.actor.name,
    ...(t.expiresAt ? { expiresAt: t.expiresAt } : {}),
  }
  st.tokens.push(token)
  audit(st, {
    time: now(),
    actor: who.actor,
    action: 'TokenCreated',
    target: {
      type: 'token',
      id: token.id,
      name: token.description,
      ...(token.cluster ? { cluster: token.cluster } : {}),
    },
    reason,
    after: token,
  })
  return { token, value: tokenValue() }
}

function validateBinding(st: AdminState, b: S['MetricsBinding'] | undefined, errs: string[]) {
  if (!b) return
  if (!st.datasources.some((d) => d.id === b.datasourceId && !d.deletedAt))
    errs.push(`metrics.datasourceId: Not found: ${JSON.stringify(b.datasourceId)}`)
  if (b.clusterLabel == null || !/^([a-zA-Z_][a-zA-Z0-9_]*)?$/.test(b.clusterLabel))
    errs.push(
      `metrics.clusterLabel: Invalid value: ${JSON.stringify(b.clusterLabel)}: a Prometheus label name or ""`,
    )
  b.extraMatchers?.forEach((m, i) => {
    if (!/^[a-zA-Z_][a-zA-Z0-9_]*(=|!=|=~|!~)"([^"\\]|\\.)*"$/.test(m))
      errs.push(`metrics.extraMatchers[${i}]: Invalid value: ${JSON.stringify(m)}: e.g. env="prod"`)
  })
}

/** Binding errors as the hub answers them: 400 unknown_datasource or 400 invalid_metrics. */
function bindingError(st: AdminState, b: S['MetricsBinding'] | undefined) {
  if (!b) return undefined
  if (!st.datasources.some((d) => d.id === b.datasourceId && !d.deletedAt))
    return badRequest('unknown_datasource', 'Unknown datasource in metrics.')
  const errs: string[] = []
  validateBinding(st, b, errs)
  return errs.length ? invalid('invalid_metrics', errs) : undefined
}

function labelMatches(st: AdminState, b: S['MetricsBinding'], cluster: string) {
  if (b.clusterLabel === '') return true
  const ds = st.datasources.find((d) => d.id === b.datasourceId)
  return !!ds?.knownLabelValues.includes(b.clusterLabelValue || cluster)
}

function validateDatasource(st: AdminState, d: S['DatasourceInput'], existing?: DatasourceRec) {
  const errs: string[] = []
  if (!d.name || !DNS1123.test(d.name))
    errs.push(`name: Invalid value: ${JSON.stringify(d.name ?? '')}: lower-case DNS label`)
  if (!['prometheus', 'victoriametrics', 'thanos', 'mimir', 'amp'].includes(d.type))
    errs.push(`type: Unsupported value: ${JSON.stringify(d.type)}`)
  try {
    const u = new URL(d.url)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error()
  } catch {
    errs.push(`url: Invalid value: ${JSON.stringify(d.url ?? '')}: an http(s) URL`)
  }
  const a = d.auth
  if (a) {
    const has = (k: 'token' | 'password') =>
      !!a[k] || (!!existing?.auth.secretConfigured && !d.clearSecrets && existing.auth.type === a.type)
    if (a.type === 'bearer' && !has('token')) errs.push('auth.token: Required value for bearer auth')
    if (a.type === 'basic' && (!a.username || !has('password')))
      errs.push('auth.username, auth.password: Required value for basic auth')
    if (a.type === 'sigv4' && !a.sigv4?.region) errs.push('auth.sigv4.region: Required value for SigV4')
    if (a.type === 'mtls' && (!a.tls?.clientCert || (!a.tls.clientKey && !existing?.auth.secretConfigured)))
      errs.push('auth.tls.clientCert, auth.tls.clientKey: Required value for mTLS')
  }
  if (d.type === 'amp' && a?.type !== 'sigv4')
    errs.push('auth.type: Invalid value: Amazon Managed Prometheus needs sigv4')
  if (d.timeout && !DURATION.test(d.timeout))
    errs.push(`timeout: Invalid value: ${JSON.stringify(d.timeout)}`)
  if (d.maxPoints != null && !(Number.isInteger(d.maxPoints) && d.maxPoints >= 100))
    errs.push(`maxPoints: Invalid value: ${d.maxPoints}: at least 100`)
  if (st.datasources.some((x) => x.name === d.name && !x.deletedAt && x.id !== existing?.id))
    return conflict('already_exists', `datasource ${d.name} exists`)
  return errs.length ? invalid('invalid_datasource', errs) : undefined
}

function datasourceAuthInfo(
  a: S['DatasourceAuthInput'] | undefined,
  prev?: S['DatasourceAuthInfo'],
  clear?: boolean,
): S['DatasourceAuthInfo'] {
  const type = a?.type ?? prev?.type ?? 'none'
  const kept = !clear && prev?.secretConfigured && prev.type === type
  return {
    type,
    ...(a?.username ? { username: a.username } : {}),
    ...(a?.sigv4 ? { sigv4: { ...a.sigv4, service: a.sigv4.service ?? 'aps' } } : {}),
    secretConfigured: !!(a?.token || a?.password || a?.tls?.clientKey) || !!kept,
    ...(a?.tls?.caCert ? { caConfigured: true } : {}),
    ...(a?.tls?.clientCert ? { clientCertConfigured: true } : {}),
    ...(a?.tls?.insecureSkipVerify ? { insecureSkipVerify: true } : {}),
  }
}

function policyById(st: AdminState, raw: unknown) {
  return st.policies.find((p) => p.id === String(raw))
}

function settingsSection(st: AdminState, section: string) {
  return section === 'recommender' ? st.recommender : section === 'pricing' ? st.pricing : undefined
}

function validateRecommender(v: S['RecommenderSettings'] | undefined) {
  const errs: string[] = []
  if (!v) return ['value: Required value']
  if (!v.interval || !DURATION.test(v.interval))
    errs.push(`value.interval: Invalid value: ${JSON.stringify(v.interval ?? '')}`)
  if (!v.retention || !DURATION.test(v.retention))
    errs.push(`value.retention: Invalid value: ${JSON.stringify(v.retention ?? '')}`)
  const names: string[] = (v.profiles ?? []).map((p) => p.name)
  if (names.length !== 3 || PROFILES.some((n) => !names.includes(n)))
    errs.push(
      'value.profiles: Invalid value: exactly one entry per built-in profile (dev, production, high-availability)',
    )
  v.profiles?.forEach((p, i) => {
    const pp = `value.profiles[${i}]`
    if (!DURATION.test(p.window ?? ''))
      errs.push(`${pp}.window: Invalid value: ${JSON.stringify(p.window ?? '')}`)
    if (p.memoryPeakWindow && !DURATION.test(p.memoryPeakWindow))
      errs.push(`${pp}.memoryPeakWindow: Invalid value`)
    for (const k of ['cpuPercentile', 'memoryPercentile'] as const)
      if (!(typeof p[k] === 'number' && p[k] > 0 && p[k] <= 1))
        errs.push(`${pp}.${k}: Invalid value: ${p[k]}: in (0, 1]`)
    for (const k of ['cpuHeadroom', 'memoryBuffer'] as const)
      if (!(typeof p[k] === 'number' && p[k] >= 0 && p[k] <= 10))
        errs.push(`${pp}.${k}: Invalid value: ${p[k]}: 0–10`)
  })
  return errs
}

function validatePricing(st: AdminState, v: S['PricingSettings'] | undefined) {
  const errs: string[] = []
  if (!v) return ['value: Required value']
  const d = v.defaultDiscountPercent
  if (d != null && !(d >= 0 && d <= 100))
    errs.push(`value.defaultDiscountPercent: Invalid value: ${d}: 0–100`)
  const seen = new Set<string>()
  ;(v.clusters ?? []).forEach((c, i) => {
    const p = `value.clusters[${i}]`
    if (seen.has(c.cluster)) errs.push(`${p}.cluster: Duplicate value: ${JSON.stringify(c.cluster)}`)
    seen.add(c.cluster)
    if (!liveCluster(st, c.cluster)) errs.push(`${p}.cluster: Not found: ${JSON.stringify(c.cluster)}`)
    if (c.discountPercent != null && !(c.discountPercent >= 0 && c.discountPercent <= 100))
      errs.push(`${p}.discountPercent: Invalid value: ${c.discountPercent}: 0–100`)
    for (const k of ['cpuCoreHourUSD', 'memoryGiBHourUSD'] as const)
      if (c[k] != null && !(c[k] >= 0)) errs.push(`${p}.${k}: Invalid value: ${c[k]}: ≥ 0`)
  })
  return errs
}

function policyClusters(st: AdminState, p: { clusters: string[] }) {
  return targetClusters(st, p.clusters)
}

function afterPolicyWrite(ctx: Ctx, clusters: string[]) {
  bump(ctx.st, clusters)
  recompute(ctx.st, ctx.db)
}

async function automationUpdate(request: Request): Promise<S['AutomationUpdate'] | Response> {
  const b = await body<S['AutomationUpdate']>(request)
  if (!b || typeof b.paused !== 'boolean') return badRequest('invalid_automation', 'paused: Required value')
  const r = reasonError(b.reason)
  if (r) return r
  if (b.resumeAt != null) {
    if (!b.paused) return badRequest('invalid_automation', 'resumeAt: Forbidden: only when pausing')
    if (!(Date.parse(b.resumeAt) > Date.now()))
      return badRequest('invalid_automation', 'resumeAt: Invalid value: must be in the future')
  }
  return b
}

// ---------------------------------------------------------------------------------------------
// Maintenance (WO-33): the hub's view of component versions
// ---------------------------------------------------------------------------------------------

function compatMessage(c: S['ComponentVersion'], hub: string): string | undefined {
  const where = c.cluster ? `in ${c.cluster}` : 'on the hub cluster'
  if (!c.version)
    return c.component === 'agent' ? 'The agent has not reported a version yet.' : 'No version reported.'
  if (c.compat === 'older') return `Older than the hub (${hub}): upgrade the Kerf chart ${where}.`
  if (c.compat === 'newer') return `Newer than the hub (${hub}): upgrade the hub first.`
  if (c.compat === 'unknown') return 'Not a release version: cannot compare with the hub.'
  return undefined
}

export function maintenanceOut(st: AdminState, db: MockDb): S['Maintenance'] {
  const m = st.maintenance
  const hub = m.hubVersion
  const t = Date.now()
  const out: S['ComponentVersion'][] = []
  const push = (c: Omit<S['ComponentVersion'], 'compat'>) => {
    const v: S['ComponentVersion'] = { ...c, compat: c.version ? versionCompat(c.version, hub) : 'unknown' }
    const msg = compatMessage(v, hub)
    out.push(msg ? { ...v, message: msg } : v)
  }
  m.hubReplicas.forEach((r, i) =>
    push({
      component: 'hub',
      instance: r.instance,
      version: hub,
      startedAt: r.startedAt,
      lastSeenAt: iso(t - (8 + i * 11) * 1000),
      leader: i === 0,
    }),
  )
  for (const p of m.pricing)
    push({ component: 'pricing', instance: p.instance, version: p.version, lastSeenAt: iso(t - 3 * 60_000) })
  const clusters = [...st.clusters.values()]
    .filter((c) => !c.deletedAt)
    .sort((a, b) => a.name.localeCompare(b.name))
  for (const c of clusters) {
    const info = db.clusters.find((x) => x.name === c.name)
    push({
      component: 'agent',
      cluster: c.name,
      version: c.agentVersion ?? '',
      ...(info?.lastHeartbeatAt ? { lastSeenAt: info.lastHeartbeatAt } : {}),
      agentStatus: info?.agentStatus ?? (c.agentConnected ? 'connected' : 'never'),
    })
    for (const w of m.webhooks.get(c.name) ?? [])
      push({
        component: 'webhook',
        cluster: c.name,
        instance: w.instance,
        version: w.version,
        lastSeenAt: iso(t - 4 * 60_000),
      })
  }
  const u = m.update
  return {
    hubVersion: hub,
    chartVersion: m.chartVersion,
    components: out,
    update: u,
    ...(u.latestVersion
      ? {
          upgrade: {
            version: u.latestVersion,
            chart: 'oci://ghcr.io/kerf-dev/kerf/charts/kerf',
            hubCommand: `helm --kube-context <hub-cluster> upgrade kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf --version ${u.latestVersion} -n kerf -f values-hub.yaml`,
            spokeCommand: `helm --kube-context <workload-cluster> upgrade kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf --version ${u.latestVersion} -n kerf -f values-spoke-<cluster>.yaml --wait`,
            docsURL: 'https://github.com/kerf-dev/kerf/blob/main/docs/install/eks.md#8-upgrade',
          },
        }
      : {}),
  }
}

/** "Check now": reads the (simulated) public releases API unless the last check is under 30 s old. */
function runUpdateCheck(st: AdminState): S['UpdateStatus'] {
  const m = st.maintenance
  const last = m.update.checkedAt ? Date.parse(m.update.checkedAt) : 0
  if (Date.now() - last < 30_000) return m.update
  const at = now()
  const r = m.release
  const cmp = versionCompat(r.version, m.hubVersion)
  m.update = {
    enabled: true,
    state:
      versionCompat(m.hubVersion, m.hubVersion) === 'unknown'
        ? 'dev_build'
        : cmp === 'newer'
          ? 'update_available'
          : 'up_to_date',
    currentVersion: m.hubVersion,
    latestVersion: r.version,
    releaseName: r.name,
    releaseURL: r.url,
    publishedAt: r.publishedAt,
    checkedAt: at,
    lastSuccessAt: at,
    interval: m.update.interval,
    source: m.update.source,
  }
  return m.update
}

export const adminHandlers = [
  // Auth -------------------------------------------------------------------------------------
  http.post('*/v1/auth/login', async ({ request }) => {
    const g = await gate(request, null)
    if (g instanceof Response) return g
    const b = await body<S['LoginRequest']>(request)
    if (!b?.username || !b.password)
      return badRequest('invalid_request', 'username and password are required')
    const u = g.st.users.get(b.username)
    const key = b.username
    if (u?.lockedUntilMs && u.lockedUntilMs > Date.now()) {
      const s = Math.ceil((u.lockedUntilMs - Date.now()) / 1000)
      return problem(
        429,
        'account_locked',
        'Too Many Requests',
        `too many failed sign-ins; try again in ${Math.ceil(s / 60)} min`,
        { 'Retry-After': String(s) },
      )
    }
    if (!u || u.deletedAt || u.disabled || u.password !== b.password) {
      const n = (loginFailures.get(key) ?? 0) + 1
      loginFailures.set(key, n)
      if (u) u.failedLogins = n
      if (n >= 5) {
        loginFailures.set(key, 0)
        if (u) {
          u.lockedUntilMs = Date.now() + 5 * 60_000
          u.failedLogins = 0
        }
        return problem(
          429,
          'account_locked',
          'Too Many Requests',
          'too many failed sign-ins; try again in 5 min',
          { 'Retry-After': '300' },
        )
      }
      return problem(401, 'invalid_credentials', 'Unauthorized', 'wrong username or password')
    }
    loginFailures.delete(key)
    u.failedLogins = 0
    u.lockedUntilMs = undefined
    u.lastLoginAt = now()
    const s = newSession(u.username)
    writeSession(s)
    return HttpResponse.json(meOut(u, s), {
      headers: {
        'Set-Cookie': `kerf_session=mock-${s.csrfToken.slice(0, 8)}; HttpOnly; SameSite=Strict; Path=/`,
      },
    })
  }),

  http.post('*/v1/auth/logout', async ({ request }) => {
    const g = await gate(request, 'viewer')
    if (g instanceof Response) return g
    if (!g.who.session) return unauthorized()
    writeSession(null)
    return new HttpResponse(null, { status: 204 })
  }),

  http.get('*/v1/me', async ({ request }) => {
    const g = await gate(request, 'viewer')
    if (g instanceof Response) return g
    const { who } = g
    if (who.session && who.user) return json(meOut(who.user, who.session))
    const me: S['Me'] = { authMethod: 'token', role: 'viewer', tokenId: '2' }
    return json(me)
  }),

  http.post('*/v1/me/password', async ({ request }) => {
    const g = await gate(request, 'viewer')
    if (g instanceof Response) return g
    if (!g.who.session || !g.who.user) return unauthorized()
    const b = await body<S['PasswordChange']>(request)
    if (!b || b.currentPassword !== g.who.user.password)
      return badRequest('invalid_credentials', 'the current password is wrong')
    if (!b.newPassword || b.newPassword.length < 8 || b.newPassword.length > 72)
      return badRequest('invalid_password', 'newPassword: Invalid value: 8–72 characters')
    if (b.newPassword === b.currentPassword)
      return badRequest(
        'invalid_password',
        'newPassword: Invalid value: must differ from the current password',
      )
    g.who.user.password = b.newPassword
    g.who.user.mustChangePassword = false
    g.who.user.updatedAt = now()
    return new HttpResponse(null, { status: 204 })
  }),

  // Settings ---------------------------------------------------------------------------------
  http.get('*/v1/admin/settings', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const body: S['Settings'] = {
      recommender: g.st.recommender,
      pricing: { ...g.st.pricing, rates: g.st.pricingRates },
    }
    return json(body)
  }),
  http.get('*/v1/admin/settings/:section/versions', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const section = String(params.section)
    if (!settingsSection(g.st, section))
      return badRequest('invalid_section', 'section must be recommender or pricing')
    const p = page(
      g.st.settingsVersions.filter((v) => v.section === section),
      new URL(request.url).searchParams,
      20,
    )
    if (p instanceof Response) return p
    return json<S['SettingsVersionList']>(p)
  }),
  ...(['recommender', 'pricing'] as const).flatMap((section) => [
    http.get(`*/v1/admin/settings/${section}`, async ({ request }) => {
      const g = await gate(request)
      if (g instanceof Response) return g
      const doc = settingsSection(g.st, section)!
      return json(section === 'pricing' ? { ...doc, rates: g.st.pricingRates } : doc, {
        version: doc.version,
      })
    }),
    http.put(`*/v1/admin/settings/${section}`, async ({ request }) => {
      const g = await gate(request, 'admin')
      if (g instanceof Response) return g
      const doc = settingsSection(g.st, section)!
      const m = ifMatch(request, doc.version)
      if (m) return m
      const b = await body<S['RecommenderSettingsUpdate'] | S['PricingSettingsUpdate']>(request)
      const r = reasonError(b?.reason)
      if (r) return r
      const errs =
        section === 'recommender'
          ? validateRecommender(b!.value as S['RecommenderSettings'])
          : validatePricing(g.st, b!.value as S['PricingSettings'])
      if (errs.length) return invalid('invalid_settings', errs)
      const before = structuredClone(doc.value)
      const next = {
        version: doc.version + 1,
        updatedAt: now(),
        updatedBy: g.who.actor,
        reason: b!.reason,
        value: b!.value,
      }
      if (section === 'recommender') g.st.recommender = next as S['RecommenderSettingsDoc']
      else g.st.pricing = next as S['PricingSettingsDoc']
      g.st.settingsVersions.unshift({
        section,
        version: next.version,
        value: obj(next.value),
        createdAt: next.updatedAt,
        actor: g.who.actor,
        reason: b!.reason,
      })
      audit(g.st, {
        time: now(),
        actor: g.who.actor,
        action: 'SettingsUpdated',
        target: { type: 'settings', id: section },
        reason: b!.reason,
        version: next.version,
        before,
        after: next.value,
      })
      return json(next, { version: next.version })
    }),
  ]),

  // Policies ---------------------------------------------------------------------------------
  http.get('*/v1/admin/policy-inventory', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const cl = new URL(request.url).searchParams.get('cluster')
    if (cl && !liveCluster(g.st, cl)) return notFound('not_found', `cluster ${cl} not found`)
    const names = cl ? [cl] : [...g.st.clusters.values()].filter((c) => !c.deletedAt).map((c) => c.name)
    return json<S['PolicyInventoryList']>({ items: names.map((n) => inventory(g.st, g.db, n)) })
  }),

  http.post('*/v1/admin/preview', async ({ request }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const b = await body<S['PreviewRequest']>(request)
    if (!b || !Array.isArray(b.changes)) return badRequest('invalid_preview', 'changes: Required value')
    const res = runPreview(g, b)
    return res instanceof Response ? res : json(res)
  }),

  http.get('*/v1/admin/policies', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const sp = new URL(request.url).searchParams
    const cl = sp.get('cluster')
    const type = sp.get('type')
    if (type && !['global-default', 'cluster-default', 'policy', 'workload-override'].includes(type))
      return badRequest('invalid_type', `invalid type ${type}`)
    const items = g.st.policies
      .filter(
        (p) =>
          (sp.get('includeDeleted') === 'true' || isLive(p)) &&
          (!type || p.type === type) &&
          (!cl || !p.clusters.length || p.clusters.includes(cl)),
      )
      .sort(sortPolicies)
    return json<S['AdminPolicyList']>({ items })
  }),

  http.post('*/v1/admin/policies', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const b = await body<S['PolicyInput']>(request)
    if (!b) return badRequest('invalid_policy', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    if (b.type === 'workload-override')
      return badRequest(
        'invalid_policy',
        'type: Invalid value: create overrides with PUT /v1/workloads/{id}/override',
      )
    const errs = validatePolicyInput(g.st, b)
    if (errs.length) return invalid('invalid_policy', errs)
    const live = g.st.policies.filter(isLive)
    if (b.type === 'global-default' && live.some((p) => p.type === 'global-default'))
      return conflict('already_exists', 'the global defaults exist; update them instead')
    if (
      b.type === 'cluster-default' &&
      live.some((p) => p.type === 'cluster-default' && p.clusters[0] === b.clusters![0])
    )
      return conflict('already_exists', `cluster defaults for ${b.clusters![0]} exist; update them instead`)
    if (b.type === 'policy' && live.some((p) => p.name === b.name))
      return conflict('already_exists', `policy ${b.name} exists`)
    const clusters = b.type === 'global-default' ? [] : (b.clusters ?? [])
    const gateRes = readinessGate(g, targetClusters(g.st, clusters), b.spec, b.overrideReadiness)
    if (gateRes) return gateRes
    const priority = b.type === 'global-default' ? 0 : b.type === 'cluster-default' ? 10 : b.spec.priority!
    const name =
      b.type === 'global-default'
        ? 'ro-global-defaults'
        : b.type === 'cluster-default'
          ? 'ro-cluster-defaults'
          : b.name!
    const t = now()
    const p: AdminPolicy = {
      id: newId(),
      type: b.type,
      name,
      ...(b.description ? { description: b.description } : {}),
      priority,
      clusters,
      spec: { ...b.spec, priority },
      version: 1,
      createdAt: t,
      createdBy: g.who.actor,
      updatedAt: t,
      updatedBy: g.who.actor,
    }
    g.st.policies.push(p)
    recordVersion(g.st, p, 'create', b.reason, g.who.actor, t)
    audit(g.st, {
      time: t,
      actor: g.who.actor,
      action: 'PolicyCreated',
      target: policyTarget(p),
      reason: b.reason,
      version: 1,
      after: { ...p, ...(b.overrideReadiness ? { overrideReadiness: true } : {}) },
    })
    afterPolicyWrite(g, policyClusters(g.st, p))
    return json(p, { status: 201, version: 1 })
  }),

  http.get('*/v1/admin/policies/:policyId/versions', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const p = policyById(g.st, params.policyId)
    if (!p) return notFound('not_found', `policy ${String(params.policyId)} not found`)
    const pg = page(g.st.versions.get(p.id) ?? [], new URL(request.url).searchParams)
    if (pg instanceof Response) return pg
    return json<S['PolicyVersionList']>(pg)
  }),

  http.post('*/v1/admin/policies/:policyId/revert', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const p = policyById(g.st, params.policyId)
    if (!p) return notFound('not_found', `policy ${String(params.policyId)} not found`)
    const m = ifMatch(request, p.version)
    if (m) return m
    const b = await body<S['PolicyRevert'] & { overrideReadiness?: boolean }>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const v = (g.st.versions.get(p.id) ?? []).find((x) => x.version === b!.version)
    if (!v) return badRequest('invalid_version', `version ${b!.version} does not exist`)
    if (v.deleted)
      return badRequest('invalid_version', `version ${v.version} is a deletion; pick an earlier version`)
    if (
      g.st.policies.some(
        (x) => x.id !== p.id && isLive(x) && x.type === p.type && x.name === v.name && p.type === 'policy',
      )
    )
      return conflict('already_exists', `another live policy is named ${v.name}`)
    const gateRes = readinessGate(g, targetClusters(g.st, v.clusters), v.spec, b!.overrideReadiness)
    if (gateRes) return gateRes
    const before = structuredClone(p)
    const oldClusters = policyClusters(g.st, p)
    Object.assign(p, {
      name: v.name,
      description: v.description,
      priority: v.priority,
      clusters: [...v.clusters],
      spec: structuredClone(v.spec),
      version: p.version + 1,
      updatedAt: now(),
      updatedBy: g.who.actor,
    })
    if (!v.description) delete p.description
    delete p.deletedAt
    recordVersion(g.st, p, 'revert', b!.reason, g.who.actor, p.updatedAt, v.version)
    audit(g.st, {
      time: p.updatedAt,
      actor: g.who.actor,
      action: 'PolicyReverted',
      target: policyTarget(p),
      reason: b!.reason,
      version: p.version,
      before,
      after: structuredClone(p),
    })
    afterPolicyWrite(g, [...oldClusters, ...policyClusters(g.st, p)])
    return json(p, { version: p.version })
  }),

  http.get('*/v1/admin/policies/:policyId', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const p = policyById(g.st, params.policyId)
    if (!p) return notFound('not_found', `policy ${String(params.policyId)} not found`)
    return json(p, { version: p.version })
  }),

  http.put('*/v1/admin/policies/:policyId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const p = policyById(g.st, params.policyId)
    if (!p || p.deletedAt) return notFound('not_found', `policy ${String(params.policyId)} not found`)
    const m = ifMatch(request, p.version)
    if (m) return m
    const b = await body<S['PolicyInput']>(request)
    if (!b) return badRequest('invalid_policy', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    if (b.type !== p.type)
      return badRequest('type_immutable', `type: Forbidden: cannot change ${p.type} to ${b.type}`)
    if (p.type === 'workload-override')
      return badRequest(
        'invalid_policy',
        'type: Invalid value: change overrides with PUT /v1/workloads/{id}/override',
      )
    const input = p.type === 'policy' ? b : { ...b, clusters: p.clusters }
    const errs = validatePolicyInput(g.st, input)
    if (errs.length) return invalid('invalid_policy', errs)
    if (p.type === 'policy' && g.st.policies.some((x) => x.id !== p.id && isLive(x) && x.name === b.name))
      return conflict('already_exists', `policy ${b.name} exists`)
    const clusters = p.type === 'policy' ? (b.clusters ?? []) : p.clusters
    const gateRes = readinessGate(g, targetClusters(g.st, clusters), b.spec, b.overrideReadiness)
    if (gateRes) return gateRes
    const before = structuredClone(p)
    const oldClusters = policyClusters(g.st, p)
    const priority = p.type === 'policy' ? b.spec.priority! : p.priority
    p.name = p.type === 'policy' ? b.name! : p.name
    if (b.description) p.description = b.description
    else delete p.description
    p.priority = priority
    p.clusters = clusters
    p.spec = { ...b.spec, priority }
    p.version++
    p.updatedAt = now()
    p.updatedBy = g.who.actor
    recordVersion(g.st, p, 'update', b.reason, g.who.actor, p.updatedAt)
    audit(g.st, {
      time: p.updatedAt,
      actor: g.who.actor,
      action: 'PolicyUpdated',
      target: policyTarget(p),
      reason: b.reason,
      version: p.version,
      before,
      after: { ...structuredClone(p), ...(b.overrideReadiness ? { overrideReadiness: true } : {}) },
    })
    afterPolicyWrite(g, [...oldClusters, ...policyClusters(g.st, p)])
    return json(p, { version: p.version })
  }),

  http.delete('*/v1/admin/policies/:policyId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const p = policyById(g.st, params.policyId)
    if (!p || p.deletedAt) return notFound('not_found', `policy ${String(params.policyId)} not found`)
    const m = ifMatch(request, p.version)
    if (m) return m
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const before = structuredClone(p)
    p.version++
    p.deletedAt = now()
    p.updatedAt = p.deletedAt
    p.updatedBy = g.who.actor
    recordVersion(g.st, p, 'delete', b!.reason, g.who.actor, p.deletedAt)
    audit(g.st, {
      time: p.deletedAt,
      actor: g.who.actor,
      action: p.type === 'workload-override' ? 'OverrideCleared' : 'PolicyDeleted',
      target: policyTarget(p),
      reason: b!.reason,
      version: p.version,
      before,
      after: structuredClone(p),
    })
    afterPolicyWrite(g, policyClusters(g.st, p))
    return json(p)
  }),

  // Workloads --------------------------------------------------------------------------------
  http.get('*/v1/workloads/:id/effective-policy', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const e = evaluate(g.st, w)
    const meta = g.st.clusters.get(w.cluster)
    const warnings: string[] = []
    if (e.labelsSkipped) warnings.push('workload labels unknown; matchLabels selectors assumed not to match')
    if (meta && meta.appliedConfigVersion !== meta.configVersion)
      warnings.push(`agent has not applied config c-${meta.configVersion} yet`)
    if (meta?.policySource === 'git')
      warnings.push('the cluster uses policySource git: hub policies are not sent to it')
    const body: S['EffectivePolicy'] = {
      workloadId: w.id,
      layers: e.layers,
      fields: e.fields,
      modes: e.modes,
      agentModes: w.modes,
      agentPolicy: w.policy ?? '',
      ...(warnings.length ? { warnings } : {}),
    }
    return json(body)
  }),

  http.get('*/v1/workloads/:id/override', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const ov = liveOverride(g.st, w.id)
    if (!ov) return notFound('not_found', `workload ${w.id} has no override`)
    return json(ov, { version: ov.version })
  }),

  http.put('*/v1/workloads/:id/override', async ({ request, params }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const existing = liveOverride(g.st, w.id)
    if (existing) {
      const m = ifMatch(request, existing.version)
      if (m) return m
    }
    const b = await body<S['WorkloadOverrideInput']>(request)
    if (!b) return badRequest('invalid_override', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    if (b.spec?.selector || b.spec?.priority != null)
      return badRequest(
        'invalid_override',
        'spec.selector, spec.priority: Forbidden: set by the hub for overrides',
      )
    const errs = validateSpec(b.spec)
    if (errs.length) return invalid('invalid_override', errs)
    const meta = g.st.clusters.get(w.cluster)
    if (setsAutoPatch(b.spec) && meta?.environment === 'prod' && g.who.role !== 'admin')
      return forbidden('admin_required', `auto-patch on the prod cluster ${w.cluster} needs an admin`)
    const gateRes = readinessGate(g, [w.cluster], b.spec, b.overrideReadiness)
    if (gateRes) return gateRes
    const t = now()
    const spec: Spec = { ...b.spec, priority: 900000, selector: overrideSelector(w) }
    let p: AdminPolicy
    if (existing) {
      const before = structuredClone(existing)
      existing.spec = spec
      if (b.description) existing.description = b.description
      existing.version++
      existing.updatedAt = t
      existing.updatedBy = g.who.actor
      p = existing
      recordVersion(g.st, p, 'update', b.reason, g.who.actor, t)
      audit(g.st, {
        time: t,
        actor: g.who.actor,
        action: 'OverrideSet',
        target: policyTarget(p),
        reason: b.reason,
        version: p.version,
        before,
        after: structuredClone(p),
      })
    } else {
      p = {
        id: newId(),
        type: 'workload-override',
        name: overridePolicyName(w.namespace, w.kind, w.name),
        ...(b.description ? { description: b.description } : {}),
        priority: 900000,
        clusters: [w.cluster],
        workloadId: w.id,
        spec,
        version: 1,
        createdAt: t,
        createdBy: g.who.actor,
        updatedAt: t,
        updatedBy: g.who.actor,
      }
      g.st.policies.push(p)
      recordVersion(g.st, p, 'create', b.reason, g.who.actor, t)
      audit(g.st, {
        time: t,
        actor: g.who.actor,
        action: 'OverrideSet',
        target: policyTarget(p),
        reason: b.reason,
        version: 1,
        after: { ...structuredClone(p), ...(b.overrideReadiness ? { overrideReadiness: true } : {}) },
      })
    }
    afterPolicyWrite(g, [w.cluster])
    return json(p, { version: p.version })
  }),

  http.delete('*/v1/workloads/:id/override', async ({ request, params }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const ov = liveOverride(g.st, w.id)
    if (!ov) return notFound('not_found', `workload ${w.id} has no override`)
    const m = ifMatch(request, ov.version)
    if (m) return m
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const before = structuredClone(ov)
    ov.version++
    ov.deletedAt = now()
    ov.updatedAt = ov.deletedAt
    ov.updatedBy = g.who.actor
    recordVersion(g.st, ov, 'delete', b!.reason, g.who.actor, ov.deletedAt)
    audit(g.st, {
      time: ov.deletedAt,
      actor: g.who.actor,
      action: 'OverrideCleared',
      target: policyTarget(ov),
      reason: b!.reason,
      version: ov.version,
      before,
      after: null,
    })
    afterPolicyWrite(g, [w.cluster])
    return new HttpResponse(null, { status: 204 })
  }),

  http.post('*/v1/workloads/:id/approve', async ({ request, params }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const b = await body<S['ApprovalRequest']>(request)
    if (!b?.recommendationID) return badRequest('invalid_approval', 'recommendationID: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    if (!w.recommendation || w.recommendation.id !== b.recommendationID)
      return conflict(
        'recommendation_stale',
        `recommendation ${b.recommendationID} is not the latest for ${w.id}`,
      )
    if (w.modes.cpu !== 'audit' && w.modes.memory !== 'audit')
      return conflict('not_in_audit', `no resource of ${w.id} is in audit mode`)
    if (b.overrideReadiness && g.who.role !== 'admin')
      return forbidden('admin_required', 'only an admin can override readiness')
    const state = readinessState(g.st, w.cluster)
    if (state !== 'AutomationReady' && !b.overrideReadiness)
      return conflict(
        'cluster_not_automation_ready',
        `approve-once needs an AutomationReady cluster: ${w.cluster} (${state})`,
      )
    const t = Date.now()
    let expires = t + 24 * 3600_000
    if (b.expiresAt) {
      expires = Date.parse(b.expiresAt)
      if (!(expires > t))
        return badRequest('invalid_approval', 'expiresAt: Invalid value: must be in the future')
      if (expires > t + 7 * 86400_000)
        return badRequest('invalid_approval', 'expiresAt: Invalid value: at most 7 days')
    }
    for (const a of g.st.approvals) if (a.workloadId === w.id && a.state === 'pending') a.state = 'superseded'
    const a: ApprovalRec = {
      id: newId(),
      workloadId: w.id,
      recommendationID: b.recommendationID,
      approvedBy: g.who.actor,
      approvedAt: iso(t),
      expiresAt: iso(expires),
      reason: b.reason,
      state: 'pending',
      autoConsume: true,
    }
    g.st.approvals.push(a)
    audit(g.st, {
      time: iso(t),
      actor: g.who.actor,
      action: 'WorkloadApproved',
      target: { type: 'approval', id: a.id, name: w.name, cluster: w.cluster, workloadId: w.id },
      reason: b.reason,
      after: { ...stripApproval(a), ...(b.overrideReadiness ? { overrideReadiness: true } : {}) },
    })
    bump(g.st, [w.cluster])
    return json(stripApproval(a), { status: 201 })
  }),

  // Clusters and automation --------------------------------------------------------------------
  http.get('*/v1/admin/automation', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const body: S['FleetAutomation'] = {
      fleet: g.st.fleet,
      clusters: [...g.st.clusters.values()]
        .filter((c) => !c.deletedAt)
        .map((c) => ({ cluster: c.name, automation: c.automation, paused: effPaused(g.st, c) })),
    }
    return json(body, { version: g.st.fleet.version })
  }),

  http.put('*/v1/admin/automation', async ({ request }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const m = ifMatch(request, g.st.fleet.version, true)
    if (m) return m
    const b = await automationUpdate(request)
    if (b instanceof Response) return b
    if (!b.paused && g.who.role !== 'admin')
      return forbidden('admin_required', 'resuming the fleet needs an admin')
    const before = g.st.fleet
    g.st.fleet = {
      paused: b.paused,
      reason: b.reason,
      by: g.who.actor,
      since: now(),
      ...(b.paused && b.resumeAt ? { resumeAt: b.resumeAt } : {}),
      version: before.version + 1,
    }
    audit(g.st, {
      time: now(),
      actor: g.who.actor,
      action: b.paused ? 'AutomationPaused' : 'AutomationResumed',
      target: { type: 'automation', id: '*' },
      reason: b.reason,
      version: g.st.fleet.version,
      before,
      after: g.st.fleet,
    })
    bump(g.st, [...g.st.clusters.keys()])
    return json(g.st.fleet, { version: g.st.fleet.version })
  }),

  http.put('*/v1/clusters/:cluster/automation', async ({ request, params }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    if (!c) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    const m = ifMatch(request, c.automation.version, true)
    if (m) return m
    const b = await automationUpdate(request)
    if (b instanceof Response) return b
    if (!b.paused && c.environment === 'prod' && g.who.role !== 'admin')
      return forbidden('admin_required', `resuming the prod cluster ${c.name} needs an admin`)
    const before = c.automation
    c.automation = {
      paused: b.paused,
      reason: b.reason,
      by: g.who.actor,
      since: now(),
      ...(b.paused && b.resumeAt ? { resumeAt: b.resumeAt } : {}),
      version: before.version + 1,
    }
    audit(g.st, {
      time: now(),
      actor: g.who.actor,
      action: b.paused ? 'AutomationPaused' : 'AutomationResumed',
      target: { type: 'automation', id: c.name, cluster: c.name },
      reason: b.reason,
      version: c.automation.version,
      before,
      after: c.automation,
    })
    bump(g.st, [c.name])
    return json(c.automation, { version: c.automation.version })
  }),

  http.post('*/v1/admin/clusters', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const b = await body<S['ClusterCreate']>(request)
    if (!b) return badRequest('invalid_cluster', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const errs: string[] = []
    if (!b.name || !DNS1123.test(b.name))
      errs.push(`name: Invalid value: ${JSON.stringify(b.name ?? '')}: lower-case DNS label`)
    if (b.environment !== 'prod' && b.environment !== 'non-prod')
      errs.push('environment: Required value: prod or non-prod')
    if (b.policySource && !['hub', 'git', 'both'].includes(b.policySource))
      errs.push(`policySource: Unsupported value: ${JSON.stringify(b.policySource)}`)
    if (errs.length) return invalid('invalid_cluster', errs)
    const metricsErr = bindingError(g.st, b.metrics)
    if (metricsErr) return metricsErr
    if (liveCluster(g.st, b.name)) return conflict('already_exists', `cluster ${b.name} exists`)
    const t = Date.now()
    const metrics: S['MetricsBinding'] = b.metrics
      ? { extraMatchers: [], ...b.metrics, clusterLabelValue: b.metrics.clusterLabelValue || b.name }
      : defaultBinding(b.name)
    const meta: ClusterMeta = {
      name: b.name,
      description: b.description ?? '',
      environment: b.environment,
      owner: b.owner ?? '',
      policySource: b.policySource ?? 'both',
      ...(b.provider ? { provider: b.provider } : {}),
      ...(b.region ? { region: b.region } : {}),
      metrics,
      createdAt: iso(t),
      updatedAt: iso(t),
      updatedBy: g.who.actor,
      version: 1,
      automation: { paused: false, version: 0 },
      configVersion: 1,
      configChangedAt: t,
      agentConnected: false,
      connectPendingSince: t,
    }
    g.st.clusters.set(b.name, meta)
    const matches = labelMatches(g.st, metrics, b.name)
    g.st.readiness.set(
      b.name,
      buildReadiness(b.name, 'new', { now: t, createdAt: t, workloads: 0, labelMatches: matches }),
    )
    g.st.readiness.get(b.name)!.checkedAt = iso(t)
    const existingInfo = g.db.clusters.findIndex((c) => c.name === b.name)
    const info: ClusterInfo = {
      name: b.name,
      ...(b.description ? { description: b.description } : {}),
      createdAt: iso(t),
      agentStatus: 'never',
      counts: { workloads: 0, withRecommendation: 0, autoPatch: 0, blocked: 0 },
    }
    if (existingInfo >= 0) g.db.clusters.splice(existingInfo, 1, info)
    else g.db.clusters.push(info)
    audit(g.st, {
      time: iso(t),
      actor: g.who.actor,
      action: 'ClusterCreated',
      target: { type: 'cluster', id: b.name, cluster: b.name },
      reason: b.reason,
      version: 1,
      after: clusterAdmin(g.st, g.db, meta),
    })
    const agentToken = b.createAgentToken
      ? createToken(g.st, g.who, { role: 'agent', cluster: b.name, description: `agent ${b.name}` }, b.reason)
      : undefined
    const out: S['ClusterCreated'] = {
      cluster: clusterAdmin(g.st, g.db, meta),
      ...(agentToken ? { agentToken } : {}),
    }
    return json(out, { status: 201, version: 1 })
  }),

  http.get('*/v1/admin/clusters/:cluster/install', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    if (!c) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    const sp = new URL(request.url).searchParams
    const format = sp.get('format')
    if (format !== 'helm' && format !== 'values' && format !== 'argocd')
      return badRequest('invalid_format', 'format must be helm, values or argocd')
    if (installUnconfigured())
      return conflict(
        'install_not_configured',
        'Install snippets are not configured on the hub: hub URL must be an absolute http(s) URL\nchart reference is not configured',
      )
    return json(
      installSnippet(
        c.name,
        format as InstallFormat,
        sp.get('webhook') === 'true',
        sp.get('certManager') === 'true',
      ),
    )
  }),

  http.get('*/v1/admin/clusters/:cluster', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    if (!c) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    return json(clusterAdmin(g.st, g.db, c), { version: c.version })
  }),

  http.patch('*/v1/admin/clusters/:cluster', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    if (!c) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    const m = ifMatch(request, c.version)
    if (m) return m
    const b = await body<S['ClusterUpdate']>(request)
    if (!b) return badRequest('invalid_cluster', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const errs: string[] = []
    if (b.environment && b.environment !== 'prod' && b.environment !== 'non-prod')
      errs.push(`environment: Unsupported value: ${JSON.stringify(b.environment)}`)
    if (b.policySource && !['hub', 'git', 'both'].includes(b.policySource))
      errs.push(`policySource: Unsupported value: ${JSON.stringify(b.policySource)}`)
    if (errs.length) return invalid('invalid_cluster', errs)
    const metricsErr = bindingError(g.st, b.metrics)
    if (metricsErr) return metricsErr
    const before = clusterAdmin(g.st, g.db, c)
    for (const k of ['description', 'environment', 'owner', 'policySource', 'provider', 'region'] as const)
      if (b[k] !== undefined) (c as unknown as Record<string, unknown>)[k] = b[k]
    if (b.metrics) {
      c.metrics = {
        extraMatchers: [],
        ...b.metrics,
        clusterLabelValue: b.metrics.clusterLabelValue || c.name,
      }
      const rm = g.st.readiness.get(c.name)
      if (rm) {
        const res = metricsCheckResults(
          c.name,
          c.metrics,
          g.st.datasources.find((d) => d.id === c.metrics.datasourceId)?.knownLabelValues ?? [],
          Date.now(),
        )
        for (const ch of res.checks)
          rm.results[ch.id] = {
            status: ch.status,
            message: ch.message,
            ...(ch.details ? { details: ch.details } : {}),
          }
        rm.checkedAt = now()
      }
    }
    c.version++
    c.updatedAt = now()
    c.updatedBy = g.who.actor
    if (b.policySource && b.policySource !== before.policySource) {
      bump(g.st, [c.name])
      recompute(g.st, g.db)
    }
    const after = clusterAdmin(g.st, g.db, c)
    audit(g.st, {
      time: c.updatedAt,
      actor: g.who.actor,
      action: 'ClusterUpdated',
      target: { type: 'cluster', id: c.name, cluster: c.name },
      reason: b.reason,
      version: c.version,
      before,
      after,
    })
    return json(after, { version: c.version })
  }),

  http.delete('*/v1/admin/clusters/:cluster', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    if (!c) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    const m = ifMatch(request, c.version)
    if (m) return m
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const before = clusterAdmin(g.st, g.db, c)
    c.deletedAt = now()
    c.version++
    for (const t of g.st.tokens) if (t.cluster === c.name && !t.revokedAt) t.revokedAt = c.deletedAt
    const i = g.db.clusters.findIndex((x) => x.name === c.name)
    if (i >= 0) g.db.clusters.splice(i, 1)
    audit(g.st, {
      time: c.deletedAt,
      actor: g.who.actor,
      action: 'ClusterDeleted',
      target: { type: 'cluster', id: c.name, cluster: c.name },
      reason: b!.reason,
      version: c.version,
      before,
      after: null,
    })
    return new HttpResponse(null, { status: 204 })
  }),

  // Readiness --------------------------------------------------------------------------------
  http.get('*/v1/clusters/:cluster/readiness/history', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    const m = c && g.st.readiness.get(c.name)
    if (!m) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    const sp = new URL(request.url).searchParams
    const from = sp.get('from') ? Date.parse(sp.get('from')!) : -Infinity
    const to = sp.get('to') ? Date.parse(sp.get('to')!) : Infinity
    const pg = page(
      m.history.filter((h) => Date.parse(h.time) >= from && Date.parse(h.time) < to),
      sp,
    )
    if (pg instanceof Response) return pg
    return json<S['ReadinessHistory']>(pg)
  }),

  http.post('*/v1/clusters/:cluster/readiness/recheck', async ({ request, params }) => {
    const g = await gate(request, 'operator')
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    const m = c && g.st.readiness.get(c.name)
    if (!c || !m) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    m.recheckRequestedAt = now()
    const body: S['RecheckAccepted'] = {
      requestedAt: m.recheckRequestedAt,
      hubChecks: true,
      agentRecheck: c.agentConnected,
    }
    return json(body, { status: 202 })
  }),

  http.get('*/v1/clusters/:cluster/readiness', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const c = liveCluster(g.st, params.cluster)
    const m = c && g.st.readiness.get(c.name)
    if (!m) return notFound('not_found', `cluster ${String(params.cluster)} not found`)
    return json(toClusterReadiness(m))
  }),

  // Tokens -----------------------------------------------------------------------------------
  http.get('*/v1/admin/tokens', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const sp = new URL(request.url).searchParams
    const cl = sp.get('cluster')
    const items = g.st.tokens
      .filter((t) => (sp.get('includeRevoked') === 'true' || !t.revokedAt) && (!cl || t.cluster === cl))
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    return json<S['TokenList']>({ items })
  }),

  http.post('*/v1/admin/tokens', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const b = await body<S['TokenCreate']>(request)
    if (!b) return badRequest('invalid_token', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const errs: string[] = []
    if (!['agent', 'ui', 'admin', 'pricing'].includes(b.role))
      errs.push(`role: Unsupported value: ${JSON.stringify(b.role)}`)
    if (!b.description?.trim() || b.description.length > 256)
      errs.push('description: Required value: 1–256 characters')
    if (b.role === 'agent' && !b.cluster) errs.push('cluster: Required value for an agent token')
    if (b.role !== 'agent' && b.cluster)
      errs.push('cluster: Forbidden: only agent tokens belong to a cluster')
    if (b.expiresAt && !(Date.parse(b.expiresAt) > Date.now()))
      errs.push('expiresAt: Invalid value: must be in the future')
    if (errs.length) return invalid('invalid_token', errs)
    if (b.cluster && !liveCluster(g.st, b.cluster))
      return notFound('not_found', `cluster ${b.cluster} not found`)
    return json(createToken(g.st, g.who, b, b.reason), { status: 201 })
  }),

  http.delete('*/v1/admin/tokens/:tokenId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const t = g.st.tokens.find((x) => x.id === String(params.tokenId))
    if (!t) return notFound('not_found', `token ${String(params.tokenId)} not found`)
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    if (!t.revokedAt) {
      t.revokedAt = now()
      audit(g.st, {
        time: t.revokedAt,
        actor: g.who.actor,
        action: 'TokenRevoked',
        target: {
          type: 'token',
          id: t.id,
          name: t.description,
          ...(t.cluster ? { cluster: t.cluster } : {}),
        },
        reason: b!.reason,
        after: t,
      })
    }
    return json(t)
  }),

  // Users ------------------------------------------------------------------------------------
  http.get('*/v1/admin/users', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const all = new URL(request.url).searchParams.get('includeDeleted') === 'true'
    const items = [...g.st.users.values()].filter((u) => all || !u.deletedAt).map(userOut)
    items.sort((a, b) => a.username.localeCompare(b.username))
    return json<S['UserList']>({ items })
  }),

  http.post('*/v1/admin/users', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const b = await body<S['UserCreate']>(request)
    if (!b) return badRequest('invalid_user', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const errs: string[] = []
    if (!b.username || !/^[a-z0-9][a-z0-9._-]{1,62}$/.test(b.username))
      errs.push(
        `username: Invalid value: ${JSON.stringify(b.username ?? '')}: 2–63 lower-case letters, digits, '.', '_' or '-'`,
      )
    if (!['viewer', 'operator', 'admin'].includes(b.role))
      errs.push(`role: Unsupported value: ${JSON.stringify(b.role)}`)
    if (!b.password || b.password.length < 8 || b.password.length > 72)
      errs.push('password: Invalid value: 8–72 characters')
    if (b.displayName && b.displayName.length > 128)
      errs.push('displayName: Too long: at most 128 characters')
    if (errs.length) return invalid('invalid_user', errs)
    const ex = g.st.users.get(b.username)
    if (ex && !ex.deletedAt) return conflict('already_exists', `user ${b.username} exists`)
    const u: UserRec = {
      username: b.username,
      displayName: b.displayName ?? b.username,
      role: b.role,
      disabled: false,
      mustChangePassword: b.mustChangePassword ?? true,
      createdAt: now(),
      createdBy: g.who.actor.name,
      version: 1,
      password: b.password,
      failedLogins: 0,
    }
    g.st.users.set(u.username, u)
    audit(g.st, {
      time: u.createdAt,
      actor: g.who.actor,
      action: 'UserCreated',
      target: { type: 'user', id: u.username },
      reason: b.reason,
      version: 1,
      after: userOut(u),
    })
    return json(userOut(u), { status: 201, version: 1 })
  }),

  http.post('*/v1/admin/users/:username/reset-password', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const u = g.st.users.get(String(params.username))
    if (!u || u.deletedAt) return notFound('not_found', `user ${String(params.username)} not found`)
    const b = await body<S['PasswordReset']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    if (b!.password != null && (b!.password.length < 8 || b!.password.length > 72))
      return badRequest('invalid_password', 'password: Invalid value: 8–72 characters')
    const temp = b!.password ? undefined : `tmp-${tokenValue().slice(3, 15)}`
    u.password = b!.password ?? temp!
    u.mustChangePassword = true
    u.lockedUntilMs = undefined
    u.failedLogins = 0
    u.updatedAt = now()
    audit(g.st, {
      time: u.updatedAt,
      actor: g.who.actor,
      action: 'PasswordReset',
      target: { type: 'user', id: u.username },
      reason: b!.reason,
    })
    const out: S['PasswordResetResult'] = {
      ...(temp ? { temporaryPassword: temp } : {}),
      mustChangePassword: true,
    }
    return json(out)
  }),

  http.get('*/v1/admin/users/:username', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const u = g.st.users.get(String(params.username))
    if (!u) return notFound('not_found', `user ${String(params.username)} not found`)
    return json(userOut(u), { version: u.version })
  }),

  http.patch('*/v1/admin/users/:username', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const u = g.st.users.get(String(params.username))
    if (!u || u.deletedAt) return notFound('not_found', `user ${String(params.username)} not found`)
    const m = ifMatch(request, u.version)
    if (m) return m
    const b = await body<S['UserUpdate']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    if (b!.role && !['viewer', 'operator', 'admin'].includes(b!.role))
      return badRequest('invalid_user', `role: Unsupported value: ${JSON.stringify(b!.role)}`)
    const losesAdmin =
      u.role === 'admin' && !u.disabled && ((b!.role && b!.role !== 'admin') || b!.disabled === true)
    if (losesAdmin && enabledAdmins(g.st).length <= 1)
      return conflict('last_admin', `${u.username} is the last enabled admin`)
    const before = userOut(u)
    if (b!.displayName !== undefined) u.displayName = b!.displayName
    if (b!.role) u.role = b!.role as UserRole
    if (b!.disabled !== undefined) u.disabled = b!.disabled
    u.version++
    u.updatedAt = now()
    audit(g.st, {
      time: u.updatedAt,
      actor: g.who.actor,
      action: 'UserUpdated',
      target: { type: 'user', id: u.username },
      reason: b!.reason,
      version: u.version,
      before,
      after: userOut(u),
    })
    return json(userOut(u), { version: u.version })
  }),

  http.delete('*/v1/admin/users/:username', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const u = g.st.users.get(String(params.username))
    if (!u || u.deletedAt) return notFound('not_found', `user ${String(params.username)} not found`)
    const m = ifMatch(request, u.version)
    if (m) return m
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    if (u.role === 'admin' && !u.disabled && enabledAdmins(g.st).length <= 1)
      return conflict('last_admin', `${u.username} is the last enabled admin`)
    const before = userOut(u)
    u.deletedAt = now()
    u.version++
    audit(g.st, {
      time: u.deletedAt,
      actor: g.who.actor,
      action: 'UserDeleted',
      target: { type: 'user', id: u.username },
      reason: b!.reason,
      version: u.version,
      before,
      after: null,
    })
    return new HttpResponse(null, { status: 204 })
  }),

  // Config audit -----------------------------------------------------------------------------
  http.get('*/v1/audit/config', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const sp = new URL(request.url).searchParams
    const order = sp.get('order') ?? 'asc'
    if (order !== 'asc' && order !== 'desc') return badRequest('invalid_order', 'order must be asc or desc')
    const from = sp.get('from') ? Date.parse(sp.get('from')!) : -Infinity
    const to = sp.get('to') ? Date.parse(sp.get('to')!) : Infinity
    const f = (k: string) => sp.get(k)
    let items = g.st.audit.filter(
      (c) =>
        (!f('cluster') ||
          c.target.cluster === f('cluster') ||
          (c.target.type === 'cluster' && c.target.id === f('cluster'))) &&
        (!f('targetType') || c.target.type === f('targetType')) &&
        (!f('targetId') || c.target.id === f('targetId')) &&
        (!f('workloadId') || c.target.workloadId === f('workloadId')) &&
        (!f('actor') || c.actor.name === f('actor')) &&
        (!sp.getAll('action').length || sp.getAll('action').includes(c.action)) &&
        Date.parse(c.time) >= from &&
        Date.parse(c.time) < to,
    )
    if (order === 'desc') items = [...items].reverse()
    const pg = page(items, sp)
    if (pg instanceof Response) return pg
    return json<S['ConfigChangeList']>(pg)
  }),

  // Maintenance (WO-33) ------------------------------------------------------------------------
  http.get('*/v1/admin/maintenance', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    return json<S['Maintenance']>(maintenanceOut(g.st, g.db))
  }),

  http.post('*/v1/admin/maintenance/update-check', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    if (!g.st.maintenance.update.enabled)
      return conflict(
        'update_check_disabled',
        'the update check is off (chart value hub.updateCheck.enabled)',
      )
    return json<S['UpdateStatus']>(runUpdateCheck(g.st))
  }),

  // Datasources ------------------------------------------------------------------------------
  http.get('*/v1/admin/datasources', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    return json<S['DatasourceList']>({
      items: g.st.datasources.filter((d) => !d.deletedAt).map((d) => datasourceOut(g.st, d)),
    })
  }),

  http.post('*/v1/admin/datasources', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const b = await body<S['DatasourceInput']>(request)
    if (!b) return badRequest('invalid_datasource', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateDatasource(g.st, b)
    if (v) return v
    const t = now()
    const d: DatasourceRec = {
      id: newId(),
      name: b.name,
      type: b.type,
      url: b.url,
      builtIn: false,
      auth: datasourceAuthInfo(b.auth),
      headers: b.headers ?? {},
      ...(b.timeout ? { timeout: b.timeout } : {}),
      ...(b.maxPoints ? { maxPoints: b.maxPoints } : {}),
      usedBy: [],
      version: 1,
      createdAt: t,
      createdBy: g.who.actor,
      updatedAt: t,
      updatedBy: g.who.actor,
      knownLabelValues: ['staging-eu-west-1', 'dev-eu-west-1', `${b.name}-cluster`],
    }
    g.st.datasources.push(d)
    const out = datasourceOut(g.st, d)
    audit(g.st, {
      time: t,
      actor: g.who.actor,
      action: 'DatasourceCreated',
      target: { type: 'datasource', id: d.id, name: d.name },
      reason: b.reason,
      version: 1,
      after: out,
    })
    return json(out, { status: 201, version: 1 })
  }),

  http.post('*/v1/admin/datasources/:datasourceId/test', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const d = g.st.datasources.find((x) => x.id === String(params.datasourceId) && !x.deletedAt)
    if (!d) return notFound('not_found', `datasource ${String(params.datasourceId)} not found`)
    const b = await body<S['DatasourceTestRequest']>(request)
    if (b?.binding) {
      const errs: string[] = []
      validateBinding(g.st, { ...b.binding, datasourceId: d.id }, errs)
      if (errs.length) return invalid('invalid_datasource', errs)
    }
    const cluster = b?.binding?.clusterLabelValue || 'new-cluster'
    const res = metricsCheckResults(cluster, b?.binding, d.knownLabelValues, Date.now())
    const out: S['DatasourceTestResult'] = {
      ok: res.ok,
      latencyMs: 38 + Math.floor(Math.random() * 60),
      ...(res.found ? { found: res.found } : {}),
      labelValues: d.knownLabelValues,
      checks: res.checks,
    }
    return json(out)
  }),

  http.get('*/v1/admin/datasources/:datasourceId', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const d = g.st.datasources.find((x) => x.id === String(params.datasourceId) && !x.deletedAt)
    if (!d) return notFound('not_found', `datasource ${String(params.datasourceId)} not found`)
    return json(datasourceOut(g.st, d), { version: d.version })
  }),

  http.put('*/v1/admin/datasources/:datasourceId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const d = g.st.datasources.find((x) => x.id === String(params.datasourceId) && !x.deletedAt)
    if (!d) return notFound('not_found', `datasource ${String(params.datasourceId)} not found`)
    // Like the hub (WO-25): the built-in datasource is managed by the hub configuration.
    if (d.builtIn)
      return conflict(
        'builtin',
        'the built-in datasource local is configured by the hub and cannot be changed',
      )
    const m = ifMatch(request, d.version)
    if (m) return m
    const b = await body<S['DatasourceInput']>(request)
    if (!b) return badRequest('invalid_datasource', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateDatasource(g.st, b, d)
    if (v) return v
    const before = datasourceOut(g.st, d)
    d.name = b.name
    d.type = b.type
    d.url = b.url
    d.auth = datasourceAuthInfo(b.auth ?? { type: d.auth.type }, d.auth, b.clearSecrets)
    d.headers = b.headers ?? {}
    if (b.timeout) d.timeout = b.timeout
    if (b.maxPoints) d.maxPoints = b.maxPoints
    d.version++
    d.updatedAt = now()
    d.updatedBy = g.who.actor
    const out = datasourceOut(g.st, d)
    audit(g.st, {
      time: d.updatedAt,
      actor: g.who.actor,
      action: 'DatasourceUpdated',
      target: { type: 'datasource', id: d.id, name: d.name },
      reason: b.reason,
      version: d.version,
      before,
      after: out,
    })
    return json(out, { version: d.version })
  }),

  http.delete('*/v1/admin/datasources/:datasourceId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const d = g.st.datasources.find((x) => x.id === String(params.datasourceId) && !x.deletedAt)
    if (!d) return notFound('not_found', `datasource ${String(params.datasourceId)} not found`)
    const m = ifMatch(request, d.version)
    if (m) return m
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    if (d.builtIn) return conflict('builtin', 'the built-in datasource local cannot be deleted')
    const used = datasourceOut(g.st, d).usedBy
    if (used.length) return conflict('datasource_in_use', `bound to ${used.join(', ')}`)
    const before = datasourceOut(g.st, d)
    d.deletedAt = now()
    d.version++
    audit(g.st, {
      time: d.deletedAt,
      actor: g.who.actor,
      action: 'DatasourceDeleted',
      target: { type: 'datasource', id: d.id, name: d.name },
      reason: b!.reason,
      version: d.version,
      before,
      after: null,
    })
    return new HttpResponse(null, { status: 204 })
  }),
]

/** For tests: the seeded session helpers. */
export { readSession, roleAtLeast }
