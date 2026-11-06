import type { VersionCompat } from '@/api/types'

/**
 * Semver comparison for Admin → Maintenance, the same rule as the hub's `compat`: both
 * versions must be release versions (`1.2.3` or `1.2.3-rc.1`, a leading `v` allowed); anything else
 * (`dev`, `dev-abc123`, a digest, empty) is `unknown`.
 */
const SEMVER = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/

export interface Semver {
  major: number
  minor: number
  patch: number
  pre: string[]
}

export function parseSemver(v: string | undefined): Semver | undefined {
  const m = SEMVER.exec((v ?? '').trim())
  if (!m) return undefined
  const [, major = '', minor = '', patch = '', pre] = m
  return { major: +major, minor: +minor, patch: +patch, pre: pre ? pre.split('.') : [] }
}

function comparePre(a: string[], b: string[]): number {
  if (!a.length || !b.length) return a.length ? -1 : b.length ? 1 : 0 // a release outranks its pre-releases
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i]
    const y = b[i]
    if (x === undefined) return -1
    if (y === undefined) return 1
    const [nx, ny] = [/^\d+$/.test(x), /^\d+$/.test(y)]
    if (nx && ny) {
      if (+x !== +y) return +x < +y ? -1 : 1
    } else if (nx !== ny) return nx ? -1 : 1
    else if (x !== y) return x < y ? -1 : 1
  }
  return 0
}

/** -1, 0 or 1 by semver precedence; undefined when either is not a release version. */
export function compareVersions(a: string | undefined, b: string | undefined): number | undefined {
  const x = parseSemver(a)
  const y = parseSemver(b)
  if (!x || !y) return undefined
  for (const k of ['major', 'minor', 'patch'] as const) if (x[k] !== y[k]) return x[k] < y[k] ? -1 : 1
  return comparePre(x.pre, y.pre)
}

/** `version` against the hub's `hub`. */
export function versionCompat(version: string | undefined, hub: string | undefined): VersionCompat {
  const c = compareVersions(version, hub)
  return c === undefined ? 'unknown' : c === 0 ? 'same' : c < 0 ? 'older' : 'newer'
}

/** `v0.4.2` → `0.4.2` for display next to versions without the prefix. */
export const displayVersion = (v: string | undefined) => (v ? v.replace(/^v(?=\d)/, '') : '')
