import type { HubPolicyType, RightsizingPolicySpec, TimeWindow } from '@/api/types'

/**
 * Client-side mirror of the shared validator (api/v1alpha1/validate) so forms explain problems before
 * the hub answers `400 invalid_policy`. The hub stays authoritative; messages match its wording.
 * Errors are keyed by spec path (e.g. `cpu.limitRatio`, `apply.maintenanceWindows.0.schedule`).
 */
export type Errors = Record<string, string>

export const LIMITS = { namespaces: 64, kinds: 4, workloads: 100, windows: 16, nameLength: 253 } as const
export const PRIORITY = { policyMin: 1, policyMax: 899_999, override: 900_000, max: 1_000_000 } as const

const DURATION = /^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$/
const QUANTITY = /^[0-9]+(\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$/
const FRACTION = /^(0(\.[0-9]{1,3})?|1(\.0{1,3})?)$/
const RATIO = /^([1-9][0-9]*)(\.[0-9]{1,3})?$/
const DNS1123_SUBDOMAIN = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/
const DNS1123_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const LABEL_NAME = /^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)$/
const LABEL_VALUE = /^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$/
const GLOB_CHARS = /^[a-z0-9*?[\]^!-]+$/

export const isDuration = (s: string) => DURATION.test(s)
export const isQuantity = (s: string) => QUANTITY.test(s)
export const isFraction = (s: string) => FRACTION.test(s)

export function validateName(name: string | undefined, type: HubPolicyType): string | undefined {
  if (type !== 'policy') return undefined
  if (!name) return 'Required.'
  if (name.length > LIMITS.nameLength) return `At most ${LIMITS.nameLength} characters.`
  if (!DNS1123_SUBDOMAIN.test(name))
    return 'Lowercase letters, digits, "-" and "."; must start and end with a letter or digit.'
  if (name.startsWith('ro-')) return 'Names starting with "ro-" are reserved for the hub.'
  return undefined
}

export function validatePriority(p: number | undefined, type: HubPolicyType): string | undefined {
  if (type !== 'policy') return undefined
  if (p == null || Number.isNaN(p)) return 'Required.'
  if (!Number.isInteger(p) || p < PRIORITY.policyMin || p > PRIORITY.policyMax)
    return `Must be between ${PRIORITY.policyMin} and ${PRIORITY.policyMax.toLocaleString('en-US')} for hub policies.`
  return undefined
}

export function validateLabelKey(k: string): string | undefined {
  const [prefix, name] = k.includes('/') ? [k.slice(0, k.indexOf('/')), k.slice(k.indexOf('/') + 1)] : ['', k]
  if (!name) return 'Label key is required.'
  if (name.length > 63 || !LABEL_NAME.test(name))
    return 'Not a valid label key (name part: up to 63 alphanumerics, "-", "_", ".").'
  if (k.includes('/') && (prefix.length > 253 || !DNS1123_SUBDOMAIN.test(prefix)))
    return 'Label key prefix must be a DNS subdomain.'
  return undefined
}
export function validateLabelValue(v: string): string | undefined {
  if (v.length > 63 || !LABEL_VALUE.test(v))
    return 'Not a valid label value (up to 63 alphanumerics, "-", "_", ".").'
  return undefined
}

/** Namespace pattern: a DNS label, optionally with `*`, `?` and `[...]` glob syntax. */
export function validateGlob(g: string): string | undefined {
  if (!g) return 'Empty pattern.'
  if (g.length > 63) return 'At most 63 characters.'
  if (!GLOB_CHARS.test(g)) return 'Use lowercase letters, digits, "-" and the glob characters * ? [ ].'
  let depth = 0
  for (const ch of g) {
    if (ch === '[') depth++
    else if (ch === ']') depth--
    if (depth < 0 || depth > 1) return 'Not a valid glob: unbalanced brackets.'
  }
  if (depth !== 0) return 'Not a valid glob: unbalanced brackets.'
  if (!/[*?[]/.test(g) && !DNS1123_LABEL.test(g)) return 'Not a valid namespace name.'
  return undefined
}

/** Glob match like Go's path.Match for one segment (namespaces have no `/`). */
export function globMatch(pattern: string, s: string): boolean {
  let re = '^'
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]!
    if (c === '*') re += '.*'
    else if (c === '?') re += '.'
    else if (c === '[') {
      const end = pattern.indexOf(']', i)
      if (end < 0) return false
      let cls = pattern.slice(i + 1, end)
      if (cls.startsWith('^') || cls.startsWith('!')) cls = '^' + cls.slice(1)
      re += `[${cls.replace(/\\/g, '\\\\')}]`
      i = end
    } else re += c.replace(/[.+^${}()|\\]/g, '\\$&')
  }
  try {
    return new RegExp(re + '$').test(s)
  } catch {
    return false
  }
}

const CRON_FIELD = /^(\*|\d+(-\d+)?)(\/\d+)?(,(\*|\d+(-\d+)?)(\/\d+)?)*$/
const CRON_RANGES: [number, number][] = [
  [0, 59],
  [0, 23],
  [1, 31],
  [1, 12],
  [0, 7],
]
const NAMES = /JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC|SUN|MON|TUE|WED|THU|FRI|SAT/gi
export function validateCron(expr: string): string | undefined {
  const f = expr.trim().split(/\s+/)
  if (f.length !== 5) return `Not a 5-field cron expression: expected 5 fields, found ${f.length}.`
  for (let i = 0; i < 5; i++) {
    const field = f[i]!.replace(NAMES, '1')
    if (!CRON_FIELD.test(field)) return `Not a 5-field cron expression: invalid field "${f[i]}".`
    for (const n of field.match(/\d+/g) ?? []) {
      const v = Number(n)
      const [lo, hi] = CRON_RANGES[i]!
      if (field.includes('/' + n)) continue
      if (v < lo || v > hi)
        return `Not a 5-field cron expression: ${v} is out of range ${lo}–${hi} in "${f[i]}".`
    }
  }
  return undefined
}

let zones: Set<string> | undefined
export function isTimeZone(tz: string) {
  if (tz === 'UTC') return true
  try {
    zones ??= new Set(
      (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf('timeZone'),
    )
  } catch {
    zones = new Set()
  }
  if (zones.has(tz)) return true
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz })
    return true
  } catch {
    return false
  }
}

function durationMs(d: string) {
  let ms = 0
  for (const m of d.matchAll(/([0-9.]+)(ns|us|µs|ms|s|m|h)/g)) {
    const v = Number(m[1])
    ms += v * { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1e3, m: 6e4, h: 3.6e6 }[m[2] as 'h']
  }
  return ms
}

export function validateWindow(w: TimeWindow, path: string, errs: Errors) {
  const recurring = !!(w.schedule || w.duration)
  const oneOff = !!(w.start || w.end)
  if (recurring === oneOff) {
    errs[path] = 'Set either schedule and duration, or start and end.'
    return
  }
  if (recurring) {
    if (!w.schedule) errs[`${path}.schedule`] = 'Required with a duration.'
    else {
      const e = validateCron(w.schedule)
      if (e) errs[`${path}.schedule`] = e
    }
    if (!w.duration) errs[`${path}.duration`] = 'Required with a schedule.'
    else if (!isDuration(w.duration)) errs[`${path}.duration`] = 'A duration like 2h or 90m.'
    else if (durationMs(w.duration) <= 0) errs[`${path}.duration`] = 'Must be positive.'
  } else {
    if (!w.start) errs[`${path}.start`] = 'Required with an end.'
    if (!w.end) errs[`${path}.end`] = 'Required with a start.'
    if (w.start && w.end && Date.parse(w.end) <= Date.parse(w.start))
      errs[`${path}.end`] = 'Must be after start.'
  }
  if (w.timeZone && !isTimeZone(w.timeZone)) errs[`${path}.timeZone`] = 'Unknown IANA time zone.'
}

/** Validates a spec as the hub would for the given policy type. */
export function validateSpec(spec: RightsizingPolicySpec, type: HubPolicyType = 'policy'): Errors {
  const e: Errors = {}
  const { cpu, guardrails: g, hpa, apply, selector: sel } = spec
  if (cpu?.limits === 'ratio') {
    if (!cpu.limitRatio) e['cpu.limitRatio'] = 'Required when CPU limits use a ratio.'
    else if (!RATIO.test(cpu.limitRatio))
      e['cpu.limitRatio'] = 'A decimal ≥ 1 with at most 3 decimals, e.g. "2" or "1.5".'
  } else if (cpu?.limitRatio) e['cpu.limitRatio'] = 'Only allowed when CPU limits use a ratio.'
  for (const k of ['learningPeriod', 'cooldown'] as const)
    if (g?.[k] && !isDuration(g[k]!)) e[`guardrails.${k}`] = 'A duration like 168h or 30m.'
  if (g?.minConfidence && !isFraction(g.minConfidence))
    e['guardrails.minConfidence'] = 'A decimal from 0 to 1 with at most 3 decimals, e.g. "0.7".'
  if (
    g?.maxChangePercent != null &&
    (g.maxChangePercent < 1 || g.maxChangePercent > 100 || !Number.isInteger(g.maxChangePercent))
  )
    e['guardrails.maxChangePercent'] = 'Must be between 1 and 100.'
  for (const k of ['minCPU', 'minMemory'] as const)
    if (g?.[k] && !isQuantity(g[k]!))
      e[`guardrails.${k}`] = k === 'minCPU' ? 'A quantity like 10m or 0.5.' : 'A quantity like 32Mi or 1Gi.'
  const rb = g?.rollback
  if (rb?.watchDuration && !isDuration(rb.watchDuration))
    e['guardrails.rollback.watchDuration'] = 'A duration like 30m.'
  for (const k of ['oomKills', 'restarts'] as const)
    if (rb?.[k] != null && (rb[k]! < 1 || !Number.isInteger(rb[k])))
      e[`guardrails.rollback.${k}`] = 'Must be at least 1.'
  if (rb?.throttlingRatio && !isFraction(rb.throttlingRatio))
    e['guardrails.rollback.throttlingRatio'] = 'A decimal from 0 to 1 with at most 3 decimals.'
  const b = hpa?.targetBounds
  for (const k of ['minUtilization', 'maxUtilization'] as const)
    if (b?.[k] != null && (b[k]! < 1 || b[k]! > 100))
      e[`hpa.targetBounds.${k}`] = 'Must be between 1 and 100.'
  if (b?.minUtilization != null && b.maxUtilization != null && b.minUtilization > b.maxUtilization)
    e['hpa.targetBounds.maxUtilization'] = 'Minimum utilization must be ≤ maximum.'
  if (apply?.fallbackAfter && !isDuration(apply.fallbackAfter))
    e['apply.fallbackAfter'] = 'A duration like 48h.'
  for (const k of ['maintenanceWindows', 'freezeWindows'] as const) {
    const ws = apply?.[k] ?? []
    if (ws.length > LIMITS.windows) e[`apply.${k}`] = `At most ${LIMITS.windows} windows.`
    ws.forEach((w, i) => validateWindow(w, `apply.${k}.${i}`, e))
  }
  if (sel) {
    if (type === 'global-default' || type === 'cluster-default') {
      if (Object.values(sel).some((v) => (Array.isArray(v) ? v.length : v && Object.keys(v).length)))
        e['selector'] = 'Defaults apply to every workload; the selector must be empty.'
    }
    for (const k of ['namespaces', 'excludeNamespaces'] as const) {
      const gs = sel[k] ?? []
      if (gs.length > LIMITS.namespaces) e[`selector.${k}`] = `At most ${LIMITS.namespaces} patterns.`
      const seen = new Set<string>()
      for (const x of gs) {
        const err = seen.has(x) ? `Duplicate "${x}".` : validateGlob(x)
        seen.add(x)
        if (err) {
          e[`selector.${k}`] = `${x}: ${err}`
          break
        }
      }
    }
    for (const [k, v] of Object.entries(sel.matchLabels ?? {})) {
      const err = validateLabelKey(k) ?? validateLabelValue(v)
      if (err) {
        e['selector.matchLabels'] = `${k}: ${err}`
        break
      }
    }
    if ((sel.workloads?.length ?? 0) > LIMITS.workloads)
      e['selector.workloads'] = `At most ${LIMITS.workloads} workloads.`
  }
  return e
}
