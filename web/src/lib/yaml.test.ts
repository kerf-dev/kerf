import { parse } from 'yaml'
import { describe, expect, it } from 'vitest'
import { diffLines, diffStats } from './diff'
import { toYaml } from './yaml'

describe('toYaml', () => {
  it.each([
    [{ a: 1, b: 'x', c: true, d: null }],
    [
      {
        spec: {
          selector: {
            namespaces: ['payments', 'team-*'],
            matchLabels: { 'app.kubernetes.io/part-of': 'shop' },
          },
        },
      },
    ],
    [
      {
        list: [
          { kind: 'Deployment', namespace: 'a', name: 'b' },
          { kind: 'StatefulSet', namespace: 'c', name: 'd' },
        ],
      },
    ],
    [{ s: ['yes', 'no', '1.0', '0.8', '', ' lead', 'a: b', 'x #y', '*', '500m', '1Gi', 'on'] }],
    [{ empty: {}, none: [], nested: { deeper: { v: 0 } } }],
    [{ text: 'line one\nline two\n', cron: '0 2 * * 1-5' }],
  ])('round-trips %#', (v) => {
    expect(parse(toYaml(v))).toEqual(v)
  })
  it('drops undefined keys', () => {
    expect(toYaml({ a: 1, b: undefined })).toBe('a: 1\n')
  })
})

describe('diffLines', () => {
  it('finds added and removed lines', () => {
    const d = diffLines('a\nb\nc', 'a\nc\nd')
    expect(d).toEqual([
      { op: 'same', text: 'a' },
      { op: 'del', text: 'b' },
      { op: 'same', text: 'c' },
      { op: 'add', text: 'd' },
    ])
    expect(diffStats(d)).toEqual({ added: 1, removed: 1 })
  })
})
