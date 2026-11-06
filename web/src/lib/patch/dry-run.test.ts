// @vitest-environment node
/**
 * Applies every generated patch format to sample workloads with a server-side dry run. It needs a cluster, so
 * it only runs when KERF_DRYRUN_CONTEXT and KERF_DRYRUN_NAMESPACE are set; `npm run test:patch-dry-run`
 * (scripts/patch-dry-run.sh) creates the namespace, runs this file and deletes the namespace again.
 *
 * Nothing is scheduled: the samples have no replicas, a suspended schedule or a node selector nothing matches.
 */
import { execFileSync, execSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { parse, parseAllDocuments, stringify } from 'yaml'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import type { WorkloadKind } from '@/api/types'
import { formatPatches, type PatchWorkload } from './patch'

// Every case shells out to kubectl; a busy shared cluster answers slowly.
vi.setConfig({ testTimeout: 90_000 })

const context = process.env.KERF_DRYRUN_CONTEXT
const namespace = process.env.KERF_DRYRUN_NAMESPACE

type Obj = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any
type Container = NonNullable<PatchWorkload['containers']>[number]

const KINDS: WorkloadKind[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob']
const IMAGE = 'registry.k8s.io/pause:3.10'

const web: Container = {
  name: 'web',
  recommended: { requests: { cpu: '120m', memory: '192Mi' }, limits: { memory: '256Mi' } },
}
const worker: Container = {
  name: 'worker',
  recommended: { requests: { cpu: '1', memory: '1Gi' } },
  removeLimits: ['cpu'],
}
const setup: Container = { name: 'setup', init: true, recommended: { requests: { cpu: '10m' } } }

const SCENARIOS: [string, Container[]][] = [
  ['one container', [web]],
  ['several containers', [web, worker]],
  ['init container', [web, setup]],
]

const sampleName = (kind: WorkloadKind, i: number) => `kerf-dryrun-${kind.toLowerCase()}-${i}`

/** A workload that starts no pods but is valid for the API server. */
function sample(kind: WorkloadKind, name: string): Obj {
  const containers = [
    {
      name: 'web',
      image: IMAGE,
      resources: { requests: { cpu: '500m', memory: '512Mi' }, limits: { cpu: '1', memory: '1Gi' } },
    },
    {
      name: 'worker',
      image: IMAGE,
      resources: { requests: { cpu: '2', memory: '2Gi' }, limits: { cpu: '2', memory: '2Gi' } },
    },
  ]
  const initContainers = [{ name: 'setup', image: IMAGE, resources: { requests: { cpu: '100m' } } }]
  const labels = { app: name }
  const pod = {
    metadata: { labels },
    spec: {
      containers,
      initContainers,
      nodeSelector: { 'kerf.dev/kerf-dryrun': 'none' },
      terminationGracePeriodSeconds: 1,
    },
  }
  const base = { metadata: { name, namespace } }
  switch (kind) {
    case 'Deployment':
      return {
        apiVersion: 'apps/v1',
        kind,
        ...base,
        spec: { replicas: 0, selector: { matchLabels: labels }, template: pod },
      }
    case 'StatefulSet':
      return {
        apiVersion: 'apps/v1',
        kind,
        ...base,
        spec: { replicas: 0, serviceName: name, selector: { matchLabels: labels }, template: pod },
      }
    case 'DaemonSet':
      return {
        apiVersion: 'apps/v1',
        kind,
        ...base,
        spec: { selector: { matchLabels: labels }, template: pod },
      }
    case 'CronJob':
      return {
        apiVersion: 'batch/v1',
        kind,
        ...base,
        spec: {
          schedule: '0 0 1 1 *',
          suspend: true,
          jobTemplate: { spec: { template: { ...pod, spec: { ...pod.spec, restartPolicy: 'Never' } } } },
        },
      }
  }
}

function kubectl(args: string[], input?: string): string {
  return execFileSync('kubectl', ['--context', context!, ...args], {
    input,
    encoding: 'utf8',
    stdio: ['pipe', 'pipe', 'pipe'],
  })
}

const workload = (kind: WorkloadKind, i: number, containers: Container[]): PatchWorkload => ({
  kind,
  namespace: namespace!,
  name: sampleName(kind, i),
  containers,
})

const podSpec = (o: Obj): Obj =>
  o.kind === 'CronJob' ? o.spec.jobTemplate.spec.template.spec : o.spec.template.spec
const find = (list: Obj[], name: string): Obj => list.find((c) => c.name === name)!

/** What the dry run returned must carry the recommended values and keep everything the patch did not name. */
function expectApplied(result: Obj, containers: Container[]) {
  const spec = podSpec(result)
  for (const c of containers) {
    const got = find(c.init ? spec.initContainers : spec.containers, c.name)
    expect(got.image, `${c.name} keeps its image`).toBe(IMAGE)
    expect(got.resources.requests).toMatchObject(c.recommended.requests ?? {})
    if (c.recommended.limits) expect(got.resources.limits).toMatchObject(c.recommended.limits)
    for (const r of c.removeLimits ?? [])
      expect(got.resources.limits?.[r], `${c.name} limit ${r} removed`).toBeUndefined()
  }
  // A container the patch does not name is untouched.
  if (!containers.some((c) => c.name === 'worker')) {
    expect(find(spec.containers, 'worker').resources.requests).toEqual({ cpu: '2', memory: '2Gi' })
  }
}

describe.skipIf(!context || !namespace)('generated patches against the API server (--dry-run=server)', () => {
  let dir: string

  beforeAll(() => {
    dir = mkdtempSync(path.join(tmpdir(), 'kerf-patch-'))
    const docs = KINDS.flatMap((kind) =>
      SCENARIOS.map((_, i) => stringify(sample(kind, sampleName(kind, i)))),
    )
    kubectl(['apply', '-f', '-'], docs.join('---\n'))
  }, 60_000)

  afterAll(() => rmSync(dir, { recursive: true, force: true }))

  describe.each(KINDS)('%s', (kind) => {
    describe.each(SCENARIOS.map((s, i) => [...s, i] as [string, Container[], number]))(
      '%s',
      (_label, containers, i) => {
        const w = workload(kind, i, containers)

        // Server-side apply cannot delete a field it does not own: `limits: {cpu: null}` reads as a limit of 0.
        // Removing a limit is a patch operation (kubectl patch, Kustomize), covered by the cases below.
        const removesLimit = containers.some((c) => c.removeLimits?.length)

        it.skipIf(removesLimit)('strategic merge patch: kubectl apply --server-side --dry-run=server', () => {
          const out = kubectl(
            ['apply', '--server-side', '--force-conflicts', '--dry-run=server', '-o', 'json', '-f', '-'],
            formatPatches('patch', [w]),
          )
          expectApplied(JSON.parse(out) as Obj, containers)
        })

        it('strategic merge patch: kubectl patch --patch-file --dry-run=server', () => {
          const file = path.join(dir, `${kind}-${i}.yaml`)
          writeFileSync(file, formatPatches('patch', [w]))
          const out = kubectl([
            'patch',
            kind.toLowerCase(),
            w.name,
            '-n',
            namespace!,
            '--type',
            'strategic',
            '--patch-file',
            file,
            '--dry-run=server',
            '-o',
            'json',
          ])
          expectApplied(JSON.parse(out) as Obj, containers)
        })

        it('kubectl patch command', () => {
          const line = formatPatches('kubectl', [w])
            .split('\n')
            .find((l) => l.startsWith('kubectl'))!
          const out = execSync(
            `${line.replace(/^kubectl/, `kubectl --context ${context}`)} --dry-run=server -o json`,
            {
              encoding: 'utf8',
            },
          )
          expectApplied(JSON.parse(out) as Obj, containers)
        })

        it('Kustomize patch: kubectl kustomize, then kubectl apply --dry-run=server', () => {
          const sub = path.join(dir, `kustomize-${kind}-${i}`)
          execFileSync('mkdir', ['-p', sub])
          writeFileSync(path.join(sub, 'workload.yaml'), stringify(sample(kind, w.name)))
          writeFileSync(
            path.join(sub, 'kustomization.yaml'),
            `resources:\n  - workload.yaml\n${formatPatches('kustomize', [w])}`,
          )
          const rendered = execFileSync('kubectl', ['kustomize', sub], { encoding: 'utf8' })
          const [obj] = parseAllDocuments(rendered).map((d) => d.toJS() as Obj)
          expectApplied(obj!, containers)
          // The rendered object is a complete workload: the API server accepts it.
          kubectl(['apply', '--dry-run=server', '--server-side', '--force-conflicts', '-f', '-'], rendered)
        })

        it('Helm values: resources blocks are valid container resources', () => {
          const values = parse(formatPatches('helm', [w])) as Record<string, { resources: Obj }>
          const obj = sample(kind, w.name)
          const spec = podSpec(obj)
          for (const c of [...spec.containers, ...spec.initContainers] as Obj[]) {
            const v = values[c.name]
            if (!v) continue
            // What a chart does with the block: merge it over its default, `null` removes a default key.
            const merged = {
              requests: { ...c.resources.requests, ...v.resources.requests },
              limits: { ...c.resources.limits, ...v.resources.limits },
            }
            for (const [k, val] of Object.entries(merged.limits))
              if (val === null) delete (merged.limits as Obj)[k]
            c.resources = merged
          }
          // The cluster only validates here (it keeps limits owned by the sample's creator), so check the values locally.
          kubectl(
            ['apply', '--dry-run=server', '--server-side', '--force-conflicts', '-f', '-'],
            stringify(obj),
          )
          expectApplied(obj, containers)
        })
      },
    )
  })

  it('bulk: one --- separated stream of every kind is accepted', () => {
    const ws = KINDS.map((k) => workload(k, 2, [web, setup]))
    const out = kubectl(
      ['apply', '--server-side', '--force-conflicts', '--dry-run=server', '-o', 'json', '-f', '-'],
      formatPatches('patch', ws),
    )
    const items = (JSON.parse(out) as Obj).items as Obj[]
    expect(items.map((o) => o.kind).sort()).toEqual([...KINDS].sort())
  })
})
