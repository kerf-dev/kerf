import type { Team } from '@/api/types'

/** The filter value and row value for workloads that belong to no team. */
export const UNASSIGNED = 'unassigned'

/** A team id is a DNS label (RFC 1123); `unassigned` is reserved. */
const TEAM_ID = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/

export function teamIdProblem(id: string): string | undefined {
  if (!id) return 'Required.'
  if (id === UNASSIGNED) return '"unassigned" is reserved.'
  if (!TEAM_ID.test(id))
    return 'Lowercase letters, digits and "-"; start and end with a letter or digit; at most 63 characters.'
  return undefined
}

/** Display name of a team value: the team's name, "Unassigned", or the id itself when unknown. */
export function teamLabel(value: string, teams?: Pick<Team, 'id' | 'displayName'>[]): string {
  if (value === UNASSIGNED) return 'Unassigned'
  return teams?.find((t) => t.id === value)?.displayName || value
}

/** One line about how a team claims namespaces: "payments-*, 2 labels · clusters prod-*". */
export function selectorSummary(t: Pick<Team, 'namespaces' | 'namespaceLabels' | 'clusters'>): string {
  const parts: string[] = []
  if (t.namespaces.length) parts.push(`namespaces ${t.namespaces.join(', ')}`)
  const labels = Object.entries(t.namespaceLabels)
  if (labels.length) parts.push(`labels ${labels.map(([k, v]) => `${k}=${v}`).join(', ')}`)
  if (!parts.length) return 'Owner label only'
  if (t.clusters.length) parts.push(`in clusters ${t.clusters.join(', ')}`)
  return parts.join(' · ')
}

/** Largest savings first, `unassigned` always last. */
export function byTeamSavings<T extends { team: string; monthlySavingsUSD: { total: number } }>(
  xs: T[],
): T[] {
  return [...xs].sort(
    (a, b) =>
      Number(a.team === UNASSIGNED) - Number(b.team === UNASSIGNED) ||
      b.monthlySavingsUSD.total - a.monthlySavingsUSD.total ||
      a.team.localeCompare(b.team),
  )
}

/** Splits a list typed with commas, spaces or new lines; empty and repeated entries are dropped. */
export const parseList = (s: string) => [...new Set(s.split(/[\s,]+/).filter(Boolean))]

/** `key=value` per line; the first line that is not one is reported. */
export function parseLabels(s: string): { labels: Record<string, string>; bad?: string } {
  const labels: Record<string, string> = {}
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (!t) continue
    const i = t.indexOf('=')
    if (i < 1) return { labels, bad: t }
    labels[t.slice(0, i).trim()] = t.slice(i + 1).trim()
  }
  return { labels }
}
