import { parse, parseAllDocuments } from 'yaml'
import { describe, expect, it } from 'vitest'
import type { WorkloadKind } from '@/api/types'
import { containerPatches, formatPatches, hasPatch, strategicMergePatch, type PatchWorkload } from './patch'

const KINDS: WorkloadKind[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob']

const app = {
  name: 'app',
  recommended: { requests: { cpu: '250m', memory: '512Mi' }, limits: { memory: '768Mi' } },
}
const proxy = { name: 'proxy', recommended: { requests: { cpu: '50m' } }, removeLimits: ['cpu' as const] }

function wl(kind: WorkloadKind, containers: PatchWorkload['containers'] = [app]): PatchWorkload {
  return { kind, namespace: 'payments', name: 'api', cluster: 'prod-1', containers }
}

type Doc = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any
type Named = { name: string }

const podSpec = (doc: Doc, kind: WorkloadKind): Doc =>
  kind === 'CronJob' ? doc.spec.jobTemplate.spec.template.spec : doc.spec.template.spec

describe('containerPatches', () => {
  it('keeps only resources that have a recommendation', () => {
    expect(containerPatches(wl('Deployment', [app, proxy]))).toEqual([
      {
        name: 'app',
        init: false,
        resources: { requests: { cpu: '250m', memory: '512Mi' }, limits: { memory: '768Mi' } },
      },
      { name: 'proxy', init: false, resources: { requests: { cpu: '50m' }, limits: { cpu: null } } },
    ])
  })
  it('skips containers without any value', () => {
    const w = wl('Deployment', [
      { name: 'idle', recommended: {} },
      { name: 'x', recommended: { limits: {} } },
    ])
    expect(containerPatches(w)).toEqual([])
    expect(hasPatch(w)).toBe(false)
    expect(hasPatch(wl('Deployment'))).toBe(true)
    expect(hasPatch({ kind: 'Deployment', namespace: 'a', name: 'b' })).toBe(false)
  })
})

describe('strategic merge patch', () => {
  it.each(KINDS)('%s with two containers', (kind) => {
    const doc = parse(formatPatches('patch', [wl(kind, [app, proxy])])) as Doc
    expect(doc.kind).toBe(kind)
    expect(doc.apiVersion).toBe(kind === 'CronJob' ? 'batch/v1' : 'apps/v1')
    expect(doc.metadata).toEqual({ name: 'api', namespace: 'payments' })
    expect(podSpec(doc, kind).containers).toEqual([
      { name: 'app', resources: { requests: { cpu: '250m', memory: '512Mi' }, limits: { memory: '768Mi' } } },
      { name: 'proxy', resources: { requests: { cpu: '50m' }, limits: { cpu: null } } },
    ])
  })
  it('puts init and sidecar containers under initContainers', () => {
    const doc = parse(formatPatches('patch', [wl('Deployment', [app, { ...proxy, init: true }])])) as Doc
    expect(doc.spec.template.spec.containers.map((c: Named) => c.name)).toEqual(['app'])
    expect(doc.spec.template.spec.initContainers.map((c: Named) => c.name)).toEqual(['proxy'])
  })
  it('keeps quantities as strings', () => {
    const w = wl('Deployment', [{ name: 'a', recommended: { requests: { cpu: '1', memory: '1Gi' } } }])
    const doc = parse(formatPatches('patch', [w])) as Doc
    expect(doc.spec.template.spec.containers[0].resources.requests).toEqual({ cpu: '1', memory: '1Gi' })
  })
  it('matches the object form', () => {
    const w = wl('StatefulSet')
    expect(parse(formatPatches('patch', [w]))).toEqual(strategicMergePatch(w))
  })
  it('joins a bulk selection with --- into separate documents', () => {
    const out = formatPatches('patch', [wl('Deployment'), { ...wl('DaemonSet'), name: 'agent' }])
    const docs = parseAllDocuments(out).map((d) => d.toJS() as { kind: string })
    expect(docs.map((d) => d.kind)).toEqual(['Deployment', 'DaemonSet'])
    expect(out.match(/^---$/gm)).toHaveLength(1)
  })
  it('skips workloads with nothing to change', () => {
    expect(formatPatches('patch', [wl('Deployment', [])])).toBe('')
    expect(parseAllDocuments(formatPatches('patch', [wl('Deployment', []), wl('DaemonSet')]))).toHaveLength(1)
  })
})

describe('kubectl patch', () => {
  it.each([
    ['Deployment', 'deployment'],
    ['StatefulSet', 'statefulset'],
    ['DaemonSet', 'daemonset'],
    ['CronJob', 'cronjob'],
  ] as [WorkloadKind, string][])('%s', (kind, resource) => {
    const out = formatPatches('kubectl', [wl(kind, [app, proxy])])
    const line = out.split('\n').find((l) => l.startsWith('kubectl'))!
    expect(line).toMatch(new RegExp(`^kubectl patch ${resource} 'api' -n 'payments' --type strategic -p '`))
    const json = JSON.parse(line.slice(line.indexOf("-p '") + 4, -1)) as Doc
    expect(podSpec(json, kind).containers).toHaveLength(2)
    expect(podSpec(json, kind).containers[1].resources.limits).toEqual({ cpu: null })
  })
  it('escapes single quotes for the shell', () => {
    expect(formatPatches('kubectl', [{ ...wl('Deployment'), name: "it's" }])).toContain(`'it'\\''s'`)
  })
  it('puts one command per workload', () => {
    const out = formatPatches('kubectl', [wl('Deployment'), wl('DaemonSet')])
    expect(out.split('\n').filter((l) => l.startsWith('kubectl'))).toHaveLength(2)
  })
})

describe('Kustomize patch', () => {
  it.each([
    ['Deployment', 'apps', 'v1'],
    ['StatefulSet', 'apps', 'v1'],
    ['DaemonSet', 'apps', 'v1'],
    ['CronJob', 'batch', 'v1'],
  ] as [WorkloadKind, string, string][])('%s has a target and an embedded patch', (kind, group, version) => {
    const doc = parse(formatPatches('kustomize', [wl(kind, [app, proxy])])) as { patches: Doc[] }
    expect(doc.patches).toHaveLength(1)
    const [entry] = doc.patches as [Doc]
    expect(entry.target).toEqual({ group, version, kind, name: 'api' })
    const patch = parse(entry.patch) as Doc
    expect(patch.kind).toBe(kind)
    expect(patch.metadata).toEqual({ name: 'api' })
    expect(podSpec(patch, kind).containers.map((c: Named) => c.name)).toEqual(['app', 'proxy'])
  })
  it('merges a bulk selection into one patches list', () => {
    const out = formatPatches('kustomize', [wl('Deployment'), { ...wl('DaemonSet'), name: 'agent' }])
    const doc = parse(out) as { patches: { target: { name: string } }[] }
    expect(doc.patches.map((p) => p.target.name)).toEqual(['api', 'agent'])
    expect(out).not.toContain('---')
    expect(out.endsWith('\n') && !out.endsWith('\n\n')).toBe(true)
  })
  it('notes the namespace in a comment', () => {
    expect(formatPatches('kustomize', [wl('Deployment')])).toContain('# Deployment payments/api (prod-1)')
  })
})

describe('Helm values', () => {
  it('has a resources block per container, keyed by container name', () => {
    expect(parse(formatPatches('helm', [wl('Deployment', [app, proxy])]))).toEqual({
      app: { resources: { requests: { cpu: '250m', memory: '512Mi' }, limits: { memory: '768Mi' } } },
      proxy: { resources: { requests: { cpu: '50m' }, limits: { cpu: null } } },
    })
  })
  it.each(KINDS)('%s', (kind) => {
    expect(Object.keys(parse(formatPatches('helm', [wl(kind)])) as Doc)).toEqual(['app'])
  })
  it('joins a bulk selection with ---', () => {
    const out = formatPatches('helm', [wl('Deployment'), { ...wl('StatefulSet'), name: 'db' }])
    expect(parseAllDocuments(out)).toHaveLength(2)
    expect(out).toContain('# StatefulSet payments/db (prod-1)')
  })
})
