import { describe, expect, it } from 'vitest'
import { broadOff } from './selector-builder'

const off = { cpu: { mode: 'off' }, memory: { mode: 'off' } }

describe('broadOff', () => {
  it('flags mode off with only excluded namespaces', () => {
    expect(broadOff({ excludeNamespaces: ['kube-system'] }, off)).toBe(true)
  })
  it('flags mode off with an empty selector', () => {
    expect(broadOff({}, { memory: { mode: 'off' } })).toBe(true)
  })
  it('does not flag mode off scoped to namespaces, workloads or labels', () => {
    expect(broadOff({ namespaces: ['kube-system'] }, off)).toBe(false)
    expect(broadOff({ workloads: [{ kind: 'Deployment', namespace: 'a', name: 'b' }] }, off)).toBe(false)
    expect(broadOff({ matchLabels: { tier: 'infra' } }, off)).toBe(false)
  })
  it('does not flag a broad policy that does not turn anything off', () => {
    expect(broadOff({}, { cpu: { mode: 'audit' } })).toBe(false)
  })
})
