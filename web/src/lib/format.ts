/**
 * Parsing and formatting of Kubernetes quantities, money and time.
 * Quantities follow https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/
 * restricted to the suffixes the hub contract allows (m, k, M, G, T, Ki, Mi, Gi, Ti).
 */

const SUFFIX: Record<string, number> = {
  '': 1,
  m: 1e-3,
  k: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  Ki: 1024,
  Mi: 1024 ** 2,
  Gi: 1024 ** 3,
  Ti: 1024 ** 4,
}

/** Parses a quantity to base units (cores or bytes). Returns undefined for missing/invalid input. */
export function parseQuantity(q: string | undefined | null): number | undefined {
  if (q == null || q === '') return undefined
  const m = /^([0-9]+(?:\.[0-9]+)?)(m|k|M|G|T|Ki|Mi|Gi|Ti)?$/.exec(q.trim())
  if (!m) return undefined
  return Number(m[1]) * SUFFIX[m[2] ?? '']!
}

function trim(n: number, digits: number): string {
  return Number(n.toFixed(digits)).toString()
}

/** 0.25 → "250m", 1.5 → "1.5", 0.0004 → "0.4m" */
export function formatCores(cores: number | undefined, opts: { unit?: boolean } = {}): string {
  if (cores == null || Number.isNaN(cores)) return '—'
  const unit = opts.unit ?? false
  if (Math.abs(cores) < 1 && cores !== 0) {
    const milli = cores * 1000
    return `${trim(milli, Math.abs(milli) < 10 ? 1 : 0)}m`
  }
  const s = trim(cores, Math.abs(cores) < 10 ? 2 : 1)
  return unit ? `${s} ${Math.abs(cores) === 1 ? 'core' : 'cores'}` : s
}

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

/** 536870912 → "512 MiB" */
export function formatBytes(bytes: number | undefined, digits = 1): string {
  if (bytes == null || Number.isNaN(bytes)) return '—'
  const sign = bytes < 0 ? '-' : ''
  let v = Math.abs(bytes)
  let i = 0
  while (v >= 1024 && i < BYTE_UNITS.length - 1) {
    v /= 1024
    i++
  }
  const d = v >= 100 || i === 0 ? 0 : digits
  return `${sign}${trim(v, d)} ${BYTE_UNITS[i]}`
}

/** 17179869184 → "16 GiB" (always GiB, for node-sized amounts) */
export function formatGiB(bytes: number | undefined): string {
  if (bytes == null || Number.isNaN(bytes)) return '—'
  const g = bytes / 1024 ** 3
  return `${trim(g, Math.abs(g) >= 100 ? 0 : 1)} GiB`
}

export type Resource = 'cpu' | 'memory'

export function formatResource(resource: Resource, value: number | undefined): string {
  return resource === 'cpu' ? formatCores(value) : formatBytes(value)
}

/** Formats a raw quantity string for display ("1536Mi" → "1.5 GiB", "250m" → "250m"). */
export function formatQuantity(resource: Resource, q: string | undefined): string {
  const v = parseQuantity(q)
  return v == null ? '—' : formatResource(resource, v)
}

const usd0 = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 })
const usd2 = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 2 })
const usdCompact = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  notation: 'compact',
  maximumFractionDigits: 1,
})

export function formatUSD(v: number | undefined, opts: { compact?: boolean; cents?: boolean } = {}): string {
  if (v == null || Number.isNaN(v)) return '—'
  if (opts.compact && Math.abs(v) >= 10_000) return usdCompact.format(v)
  if (opts.cents || (Math.abs(v) < 10 && v !== 0)) return usd2.format(v)
  return usd0.format(v)
}

/**
 * 0.42 → "42%". A ratio strictly between 0 and 1 never rounds to "0%" or "100%" (2 of 568 workloads is
 * "<1%", 566 of 568 is ">99%"), so "0%" and "100%" always mean none and all.
 */
export function formatPercent(ratio: number | undefined, digits = 0): string {
  if (ratio == null || Number.isNaN(ratio)) return '—'
  const s = (ratio * 100).toFixed(digits)
  const step = 10 ** -digits
  if (ratio > 0 && Number(s) === 0) return `<${step}%`
  if (ratio < 1 && Number(s) === 100) return `>${trim(100 - step, digits)}%`
  return `${s}%`
}

/** Signed change from `from` to `to` as a ratio; undefined if either is missing or from is 0. */
export function delta(from: number | undefined, to: number | undefined): number | undefined {
  if (from == null || to == null || from === 0) return undefined
  return (to - from) / from
}

export function formatDelta(ratio: number | undefined): string {
  if (ratio == null) return ''
  const pct = Math.round(ratio * 100)
  if (pct === 0) return '±0%'
  return `${pct > 0 ? '+' : '−'}${Math.abs(pct)}%`
}

const nf = new Intl.NumberFormat('en-US')
export function formatNumber(n: number | undefined): string {
  return n == null ? '—' : nf.format(n)
}

/** Go duration ("336h0m0s", "10m", "1h30m") → seconds. */
export function parseGoDuration(d: string | undefined): number | undefined {
  if (!d) return undefined
  const re = /(\d+(?:\.\d+)?)(h|ms|m|s|us|µs|ns)/g
  let total = 0
  let matched = false
  for (const m of d.matchAll(re)) {
    matched = true
    const v = Number(m[1])
    switch (m[2]) {
      case 'h':
        total += v * 3600
        break
      case 'm':
        total += v * 60
        break
      case 's':
        total += v
        break
      case 'ms':
        total += v / 1e3
        break
      default:
        total += v / 1e6
    }
  }
  return matched ? total : undefined
}

/** Seconds → "14d", "8h", "1d 6h", "45m" */
export function humanDuration(seconds: number | undefined): string {
  if (seconds == null) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return h > 0 && d < 7 ? `${d}d ${h}h` : `${d}d`
  if (h > 0) return m > 0 && h < 6 ? `${h}h ${m}m` : `${h}h`
  if (m > 0) return `${m}m`
  return `${Math.round(seconds)}s`
}

export function formatGoDuration(d: string | undefined): string {
  return humanDuration(parseGoDuration(d))
}

const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })

export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return '—'
  const diff = (t - now) / 1000
  const abs = Math.abs(diff)
  if (abs < 45) return 'just now'
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day')
  return rtf.format(Math.round(diff / (86400 * 30)), 'month')
}

const dtf = new Intl.DateTimeFormat('en-GB', {
  day: '2-digit',
  month: 'short',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})
const dtfShort = new Intl.DateTimeFormat('en-GB', {
  day: '2-digit',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
})

export function formatDateTime(iso: string | undefined, short = false): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return '—'
  return (short ? dtfShort : dtf).format(t)
}
