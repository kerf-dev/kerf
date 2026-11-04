import { describe, expect, it } from 'vitest'
import { stepChanges } from './series'

describe('stepChanges', () => {
  it('returns each point where the value changes', () => {
    expect(
      stepChanges([
        [0, 0.095],
        [900, 0.095],
        [1800, 0.09],
        [2700, 0.09],
        [3600, 0.095],
      ]),
    ).toEqual([
      { t: 1800, from: 0.095, to: 0.09 },
      { t: 3600, from: 0.09, to: 0.095 },
    ])
  })
  it('is empty for a constant or single-point series', () => {
    expect(stepChanges([[0, 1]])).toEqual([])
    expect(stepChanges([])).toEqual([])
  })
})
