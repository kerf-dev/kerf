import { describe, expect, it } from 'vitest'
import {
  delta,
  formatBytes,
  formatCores,
  formatDelta,
  formatQuantity,
  formatUSD,
  humanDuration,
  parseGoDuration,
  parseQuantity,
} from './format'
import { encodeWorkloadId, parseWorkloadId, workloadPath } from './workload-id'

describe('parseQuantity', () => {
  it.each([
    ['250m', 0.25],
    ['2', 2],
    ['1.5', 1.5],
    ['512Mi', 512 * 1024 ** 2],
    ['1Gi', 1024 ** 3],
    ['1G', 1e9],
    ['100k', 1e5],
    ['', undefined],
    ['abc', undefined],
    [undefined, undefined],
  ])('%s → %s', (q, want) => expect(parseQuantity(q)).toBe(want))
})

describe('formatting', () => {
  it.each([
    [0.25, '250m'],
    [0.0045, '4.5m'],
    [1, '1'],
    [1.256, '1.26'],
    [12.34, '12.3'],
  ])('formatCores(%s) = %s', (v, want) => expect(formatCores(v)).toBe(want))
  it.each([
    [512 * 1024 ** 2, '512 MiB'],
    [1.5 * 1024 ** 3, '1.5 GiB'],
    [900, '900 B'],
    [-2 * 1024 ** 3, '-2 GiB'],
  ])('formatBytes(%s) = %s', (v, want) => expect(formatBytes(v)).toBe(want))
  it('formats quantities by resource', () => {
    expect(formatQuantity('memory', '1536Mi')).toBe('1.5 GiB')
    expect(formatQuantity('cpu', '1500m')).toBe('1.5')
    expect(formatQuantity('cpu', undefined)).toBe('—')
  })
  it('formats money', () => {
    expect(formatUSD(1234.5)).toBe('$1,235')
    expect(formatUSD(48213, { compact: true })).toBe('$48.2K')
    expect(formatUSD(3.456)).toBe('$3.46')
  })
  it('formats deltas', () => {
    expect(formatDelta(delta(1, 0.5))).toBe('−50%')
    expect(formatDelta(delta(1, 1.25))).toBe('+25%')
    expect(formatDelta(delta(0, 1))).toBe('')
  })
  it('parses go durations', () => {
    expect(parseGoDuration('336h0m0s')).toBe(336 * 3600)
    expect(parseGoDuration('1h30m')).toBe(5400)
    expect(humanDuration(336 * 3600)).toBe('14d')
    expect(humanDuration(30 * 3600)).toBe('1d 6h')
  })
})

describe('workload ids', () => {
  it('encodes as one path segment', () => {
    expect(encodeWorkloadId('ro-spoke-1/payments/Deployment/checkout')).toBe(
      'ro-spoke-1%2Fpayments%2FDeployment%2Fcheckout',
    )
  })
  it('parses and rejects', () => {
    expect(parseWorkloadId('c/ns/StatefulSet/db')).toEqual({
      cluster: 'c',
      namespace: 'ns',
      kind: 'StatefulSet',
      name: 'db',
    })
    expect(parseWorkloadId('c/ns/Pod/db')).toBeUndefined()
    expect(parseWorkloadId('c/ns/Deployment')).toBeUndefined()
  })
  it('builds readable UI routes', () => {
    expect(workloadPath('c/ns/Deployment/web')).toBe('/workloads/c/ns/Deployment/web')
  })
})
