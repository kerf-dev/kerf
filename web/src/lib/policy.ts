import type { RightsizingPolicySpec } from '@/api/types'
import { getIn, leafPaths } from './spec'
import { toYaml } from './yaml'

/** A RightsizingPolicy manifest for Git (no hub label, so the agent treats it as a Git policy). */
export function policyManifest(name: string, spec: RightsizingPolicySpec) {
  return toYaml({
    apiVersion: 'resource-optimizer.oz.io/v1alpha1',
    kind: 'RightsizingPolicy',
    metadata: { name },
    spec,
  })
}

/** Leaf paths whose values differ between two specs (for the unsaved-changes count). */
export function changedPaths(a: RightsizingPolicySpec, b: RightsizingPolicySpec) {
  const all = new Set([...leafPaths(a), ...leafPaths(b)])
  return [...all].filter((p) => JSON.stringify(getIn(a, p)) !== JSON.stringify(getIn(b, p)))
}

/** The editable part of a policy spec: the hub sets `selector` and `priority` for overrides and defaults. */
export function withoutSelector(spec: RightsizingPolicySpec | undefined): RightsizingPolicySpec {
  const out = { ...spec }
  delete out.selector
  delete out.priority
  return out
}

/** Only same-app paths: never redirect to another origin after login. */
export function safeNext(next: string | null) {
  if (
    !next ||
    !next.startsWith('/') ||
    next.startsWith('//') ||
    next.startsWith('/\\') ||
    next.startsWith('/login')
  )
    return '/'
  return next
}
