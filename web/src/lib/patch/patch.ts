import type { ContainerRecommendation, WorkloadKind } from '@/api/types'
import { toYaml } from '@/lib/yaml'

/**
 * Turns a recommendation into something a team can commit: a strategic merge patch, a `kubectl patch`
 * command, a Kustomize `patches:` entry or a Helm values snippet. Everything here is a pure function of the
 * workload and its recommended values; nothing talks to the hub or the cluster.
 */
export type PatchFormat = 'patch' | 'kubectl' | 'kustomize' | 'helm'

export const PATCH_FORMATS: { id: PatchFormat; label: string; hint: string; language: 'yaml' | 'shell' }[] = [
  {
    id: 'patch',
    label: 'Strategic merge patch',
    hint: 'YAML for kubectl, Argo CD or Flux',
    language: 'yaml',
  },
  { id: 'kubectl', label: 'kubectl patch', hint: 'One command per workload', language: 'shell' },
  { id: 'kustomize', label: 'Kustomize patch', hint: 'A patches: entry with a target', language: 'yaml' },
  { id: 'helm', label: 'Helm values', hint: 'A resources: block per container', language: 'yaml' },
]

export interface PatchWorkload {
  kind: WorkloadKind
  namespace: string
  name: string
  /** Only used for comments, so a bulk copy across clusters stays readable. */
  cluster?: string
  containers?: Pick<ContainerRecommendation, 'name' | 'init' | 'recommended' | 'removeLimits'>[]
}

type Quantities = { cpu?: string | null; memory?: string | null }
interface ContainerPatch {
  name: string
  init: boolean
  resources: { requests?: Quantities; limits?: Quantities }
}

const RESOURCES = ['cpu', 'memory'] as const

const KINDS: Record<WorkloadKind, { apiVersion: string; resource: string; podSpecPath: string[] }> = {
  Deployment: { apiVersion: 'apps/v1', resource: 'deployment', podSpecPath: ['spec', 'template', 'spec'] },
  StatefulSet: { apiVersion: 'apps/v1', resource: 'statefulset', podSpecPath: ['spec', 'template', 'spec'] },
  DaemonSet: { apiVersion: 'apps/v1', resource: 'daemonset', podSpecPath: ['spec', 'template', 'spec'] },
  CronJob: {
    apiVersion: 'batch/v1',
    resource: 'cronjob',
    podSpecPath: ['spec', 'jobTemplate', 'spec', 'template', 'spec'],
  },
}

/**
 * The resources to change per container. A resource without a recommended value is left out (the patch
 * must not touch it); a limit the recommendation removes becomes `null`, which a merge patch reads as "delete".
 */
export function containerPatches(w: PatchWorkload): ContainerPatch[] {
  const out: ContainerPatch[] = []
  for (const c of w.containers ?? []) {
    const resources: ContainerPatch['resources'] = {}
    for (const kind of ['requests', 'limits'] as const) {
      const q: Quantities = {}
      for (const r of RESOURCES) {
        const v = c.recommended?.[kind]?.[r]
        if (v) q[r] = v
        else if (kind === 'limits' && c.removeLimits?.includes(r)) q[r] = null
      }
      if (Object.keys(q).length) resources[kind] = q
    }
    if (Object.keys(resources).length) out.push({ name: c.name, init: !!c.init, resources })
  }
  return out
}

/** True when the workload has at least one value to hand over. */
export function hasPatch(w: PatchWorkload): boolean {
  return containerPatches(w).length > 0
}

function nest(path: string[], leaf: object): Record<string, unknown> {
  return path.reduceRight<Record<string, unknown>>(
    (acc, key) => ({ [key]: acc }),
    leaf as Record<string, unknown>,
  )
}

/** The patch body: the pod spec with only the containers (and init containers) to change, matched by name. */
function podSpecPatch(w: PatchWorkload): Record<string, unknown> {
  const cs = containerPatches(w)
  const list = (init: boolean) =>
    cs.filter((c) => c.init === init).map((c) => ({ name: c.name, resources: c.resources }))
  const spec = {
    ...(list(false).length ? { containers: list(false) } : {}),
    ...(list(true).length ? { initContainers: list(true) } : {}),
  }
  return nest(KINDS[w.kind].podSpecPath, spec)
}

/** The strategic merge patch as an object, with the identity `kubectl`, Kustomize and Argo CD match on. */
export function strategicMergePatch(w: PatchWorkload): Record<string, unknown> {
  return {
    apiVersion: KINDS[w.kind].apiVersion,
    kind: w.kind,
    metadata: { name: w.name, namespace: w.namespace },
    ...podSpecPatch(w),
  }
}

const label = (w: PatchWorkload) => `${w.kind} ${w.namespace}/${w.name}${w.cluster ? ` (${w.cluster})` : ''}`

function patchYaml(w: PatchWorkload): string {
  return `# ${label(w)}\n${toYaml(strategicMergePatch(w))}`
}

const shellQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`

function kubectlCommand(w: PatchWorkload): string {
  const body = JSON.stringify(podSpecPatch(w))
  const ns = shellQuote(w.namespace)
  return `# ${label(w)}\nkubectl patch ${KINDS[w.kind].resource} ${shellQuote(w.name)} -n ${ns} --type strategic -p ${shellQuote(body)}`
}

function kustomizeEntry(w: PatchWorkload): Record<string, unknown> {
  const [group, version] = KINDS[w.kind].apiVersion.split('/') as [string, string]
  // The target matches on kind and name only: overlays usually set the namespace themselves, and a base
  // without one would not match a namespaced target. The namespace stays in the comment above.
  const body = {
    apiVersion: KINDS[w.kind].apiVersion,
    kind: w.kind,
    metadata: { name: w.name },
    ...podSpecPatch(w),
  }
  return {
    target: { group, version, kind: w.kind, name: w.name },
    patch: toYaml(body).replace(/\n$/, ''),
  }
}

function kustomizePatches(ws: PatchWorkload[]): string {
  const indent = (text: string) =>
    text
      .replace(/\n$/, '')
      .split('\n')
      .map((l) => (l ? `  ${l}` : l))
      .join('\n')
  const entries = ws.map((w) => `  # ${label(w)}\n${indent(toYaml([kustomizeEntry(w)]))}\n`)
  return `patches:\n${entries.join('')}`
}

function helmValues(w: PatchWorkload): string {
  const values = Object.fromEntries(containerPatches(w).map((c) => [c.name, { resources: c.resources }]))
  return [
    `# ${label(w)}`,
    '# Keys are container names: move each `resources:` block to where your chart reads it',
    '# (many charts use a top-level `resources:` for a single container).',
    toYaml(values),
  ].join('\n')
}

/**
 * Formats one or more workloads. Workloads with nothing to change are skipped. Bulk output joins documents
 * with `---` (commands with a newline); Kustomize merges every workload into one `patches:` list, which is
 * what a kustomization.yaml expects.
 */
export function formatPatches(format: PatchFormat, workloads: PatchWorkload[]): string {
  const ws = workloads.filter(hasPatch)
  if (!ws.length) return ''
  switch (format) {
    case 'patch':
      return ws.map(patchYaml).join('---\n')
    case 'helm':
      return ws.map(helmValues).join('---\n')
    case 'kustomize':
      return kustomizePatches(ws)
    case 'kubectl':
      return ws.map(kubectlCommand).join('\n\n') + '\n'
  }
}
