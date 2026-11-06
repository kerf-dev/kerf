import type { BlockerType, CapacityObjectRef, ConsolidationBlocker } from '@/api/types'

export const BLOCKER_LABEL: Record<BlockerType, string> = {
  'pdb-zero-disruptions': 'Budget allows no disruptions',
  'do-not-disrupt-pod': 'Pod marked do-not-disrupt',
  'do-not-disrupt-node': 'Node marked do-not-disrupt',
  'safe-to-evict-false': 'Pod marked not safe to evict',
  'local-storage': 'Pod uses local storage',
  'node-pool-minimum': 'Node pool at its minimum',
  'consolidation-policy': 'Consolidation policy',
  'disruption-budget-zero': 'Node pool budget allows no disruptions',
  'scale-down-disabled': 'Scale-down disabled',
}

/** Display order of the blocker types. */
const BLOCKER_ORDER = Object.keys(BLOCKER_LABEL) as BlockerType[]

export function sortBlockers(blockers: ConsolidationBlocker[]): ConsolidationBlocker[] {
  const rank = (b: ConsolidationBlocker) => {
    const i = BLOCKER_ORDER.indexOf(b.type)
    return i < 0 ? BLOCKER_ORDER.length : i
  }
  return blockers
    .map((b, i) => ({ b, i }))
    .sort((x, y) => rank(x.b) - rank(y.b) || x.i - y.i)
    .map((x) => x.b)
}

/** Objects without an apiVersion (Cluster Autoscaler node groups) are not Kubernetes objects. */
export function kubectlGet(o: CapacityObjectRef): string | undefined {
  if (!o.apiVersion) return undefined
  return `kubectl get ${o.kind.toLowerCase()} ${o.name}${o.namespace ? ` -n ${o.namespace}` : ''} -o yaml`
}

export function objectLabel(o: CapacityObjectRef): string {
  if (!o.apiVersion && o.kind === 'NodeGroup') return `Node group ${o.name}`
  return `${o.kind} ${o.namespace ? `${o.namespace}/` : ''}${o.name}`
}
