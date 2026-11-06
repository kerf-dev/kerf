import { describe, expect, it } from 'vitest'
import { getIn, mergeSpecs, prune, selectorSummary, setIn, specChips } from './spec'

describe('spec helpers', () => {
  it('setIn sets and prunes', () => {
    const a = setIn({}, 'guardrails.rollback.oomKills', 2)
    expect(a).toEqual({ guardrails: { rollback: { oomKills: 2 } } })
    expect(setIn(a, 'guardrails.rollback.oomKills', undefined)).toEqual({})
    expect(getIn(a, 'guardrails.rollback.oomKills')).toBe(2)
  })
  it('prune drops empties', () => {
    expect(prune({ a: '', b: [], c: {}, d: { e: undefined }, f: 0, g: false })).toEqual({ f: 0, g: false })
  })
  it('mergeSpecs layers field by field', () => {
    expect(
      mergeSpecs({ cpu: { mode: 'audit', limits: 'keep' } }, { cpu: { mode: 'auto-patch' } }, undefined),
    ).toEqual({ cpu: { mode: 'auto-patch', limits: 'keep' } })
  })
  it('summarises', () => {
    expect(selectorSummary({ namespaces: ['payments'], kinds: ['Deployment'] })).toBe('payments · Deployment')
    expect(selectorSummary(undefined)).toBe('All workloads')
    expect(specChips({ cpu: { mode: 'auto-patch' }, guardrails: { cooldown: '1h' } })).toEqual([
      'CPU auto-patch',
      '+1 setting',
    ])
  })
})

describe('policy helpers', () => {
  it('changedPaths and withoutSelector', async () => {
    const { changedPaths, withoutSelector } = await import('./policy')
    expect(changedPaths({ cpu: { mode: 'audit' } }, { cpu: { mode: 'off' }, profile: 'dev' })).toEqual([
      'cpu.mode',
      'profile',
    ])
    expect(
      withoutSelector({ priority: 900000, selector: { namespaces: ['a'] }, cpu: { mode: 'off' } }),
    ).toEqual({ cpu: { mode: 'off' } })
  })
})
