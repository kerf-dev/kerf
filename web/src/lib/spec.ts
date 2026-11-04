import type { Mode, PolicySelector, RightsizingPolicySpec } from '@/api/types'

/**
 * Helpers for editing `RightsizingPolicySpec` values. Unset fields inherit from the layer below
 * (docs/contracts/admin.md §2), so editors never write empty strings or empty objects: `prune` removes
 * them before a spec is sent.
 */

/** The agent's built-in defaults (internal/agent/policy/resolve.go): the bottom layer. */
export const BUILT_IN: Required<
  Pick<RightsizingPolicySpec, 'profile' | 'cpu' | 'memory' | 'apply' | 'guardrails' | 'hpa' | 'jvm'>
> = {
  profile: 'production',
  cpu: { mode: 'audit', limits: 'keep' },
  memory: { mode: 'audit', limitEqualsRequest: true },
  apply: { strategy: 'in-place', fallbackAfter: '10m' },
  guardrails: {
    learningPeriod: '168h',
    minConfidence: '0.7',
    maxChangePercent: 50,
    minCPU: '10m',
    minMemory: '32Mi',
    cooldown: '24h',
    rollback: { watchDuration: '30m', oomKills: 1, restarts: 3, throttlingRatio: '0.25' },
  },
  hpa: { coordinate: false, targetBounds: { minUtilization: 40, maxUtilization: 90 } },
  jvm: { detect: true },
}

type Obj = Record<string, unknown>
const isObj = (v: unknown): v is Obj => !!v && typeof v === 'object' && !Array.isArray(v)

export function getIn(o: unknown, path: string): unknown {
  return path
    .split('.')
    .reduce<unknown>((acc, k) => (isObj(acc) || Array.isArray(acc) ? (acc as Obj)[k] : undefined), o)
}

/** Immutable set; `undefined` or '' deletes the key, and empty parents are pruned. */
export function setIn<T>(o: T, path: string, value: unknown): T {
  const keys = path.split('.')
  const rec = (cur: unknown, i: number): unknown => {
    const k = keys[i]!
    const base: Obj = isObj(cur) ? { ...cur } : {}
    if (i === keys.length - 1) {
      if (value === undefined || value === '') delete base[k]
      else base[k] = value
    } else base[k] = rec(base[k], i + 1)
    return base
  }
  return prune(rec(o, 0)) as T
}

/** Removes undefined, '', empty arrays and empty objects (recursively). */
export function prune<T>(v: T): T {
  if (Array.isArray(v)) return v.map((x) => prune(x)) as T
  if (!isObj(v)) return v
  const out: Obj = {}
  for (const [k, x] of Object.entries(v)) {
    const p = prune(x)
    if (
      p === undefined ||
      p === '' ||
      (Array.isArray(p) && !p.length) ||
      (isObj(p) && !Object.keys(p).length)
    )
      continue
    out[k] = p
  }
  return out as T
}

/** Layers specs field by field, later wins (lists replace, objects merge). */
export function mergeSpecs(...specs: (RightsizingPolicySpec | undefined)[]): RightsizingPolicySpec {
  const merge = (a: unknown, b: unknown): unknown => {
    if (b === undefined) return a
    if (isObj(a) && isObj(b)) {
      const out: Obj = { ...a }
      for (const [k, v] of Object.entries(b)) out[k] = merge(a[k], v)
      return out
    }
    return b
  }
  return specs.reduce<RightsizingPolicySpec>((acc, s) => merge(acc, s) as RightsizingPolicySpec, {})
}

/** Every leaf path set in a spec (for "N settings" summaries and diff highlights). */
export function leafPaths(o: unknown, prefix = ''): string[] {
  if (!isObj(o)) return prefix ? [prefix] : []
  return Object.entries(o).flatMap(([k, v]) => {
    const p = prefix ? `${prefix}.${k}` : k
    return isObj(v) ? leafPaths(v, p) : [p]
  })
}

export const MODE_LABEL: Record<Mode, string> = { off: 'Off', audit: 'Audit', 'auto-patch': 'Auto-patch' }

/** Short human summary of a selector: "payments, team-* · Deployment · 2 labels". */
export function selectorSummary(s: PolicySelector | undefined): string {
  if (!s) return 'All workloads'
  const parts: string[] = []
  if (s.workloads?.length)
    parts.push(
      s.workloads.length === 1
        ? `${s.workloads[0]!.namespace}/${s.workloads[0]!.name}`
        : `${s.workloads.length} workloads`,
    )
  if (s.namespaces?.length)
    parts.push(
      s.namespaces.length > 3
        ? `${s.namespaces.slice(0, 3).join(', ')} +${s.namespaces.length - 3}`
        : s.namespaces.join(', '),
    )
  else if (!s.workloads?.length) parts.push('all namespaces')
  if (s.excludeNamespaces?.length) parts.push(`not ${s.excludeNamespaces.join(', ')}`)
  if (s.kinds?.length) parts.push(s.kinds.join(', '))
  const labels = Object.entries(s.matchLabels ?? {})
  if (labels.length)
    parts.push(labels.length === 1 ? `${labels[0]![0]}=${labels[0]![1]}` : `${labels.length} labels`)
  return parts.join(' · ')
}

/** What a policy sets, as short chips: "CPU auto-patch", "memory audit", "rollout". */
export function specChips(s: RightsizingPolicySpec | undefined): string[] {
  if (!s) return []
  const chips: string[] = []
  if (s.cpu?.mode) chips.push(`CPU ${MODE_LABEL[s.cpu.mode].toLowerCase()}`)
  if (s.memory?.mode) chips.push(`memory ${MODE_LABEL[s.memory.mode].toLowerCase()}`)
  if (s.apply?.strategy) chips.push(s.apply.strategy)
  if (s.profile) chips.push(s.profile)
  const rest = leafPaths(s).filter(
    (p) =>
      !['cpu.mode', 'memory.mode', 'apply.strategy', 'profile', 'priority'].includes(p) &&
      !p.startsWith('selector'),
  )
  if (rest.length) chips.push(`+${rest.length} setting${rest.length === 1 ? '' : 's'}`)
  return chips
}
