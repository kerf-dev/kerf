import { describe, expect, it } from 'vitest'
import { compareVersions, displayVersion, versionCompat } from './version'

describe('versionCompat', () => {
  it.each([
    ['0.4.2', '0.4.2', 'same'],
    ['v0.4.2', '0.4.2', 'same'],
    ['v0.4.1', '0.4.2', 'older'],
    ['0.5.0', '0.4.2', 'newer'],
    ['1.0.0', '0.9.9', 'newer'],
    ['0.5.0-rc.1', '0.5.0', 'older'],
    ['0.5.0-rc.2', '0.5.0-rc.10', 'older'],
    ['0.5.0-alpha', '0.5.0-1', 'newer'],
    ['dev', '0.4.2', 'unknown'],
    ['dev-3f2a9c1', '0.4.2', 'unknown'],
    ['0.4.2', 'dev', 'unknown'],
    ['', '0.4.2', 'unknown'],
    ['sha256:0123456789ab', '0.4.2', 'unknown'],
  ])('%s vs hub %s → %s', (v, hub, want) => {
    expect(versionCompat(v, hub)).toBe(want)
  })

  it('compares by semver precedence, not as strings', () => {
    expect(compareVersions('0.10.0', '0.9.0')).toBe(1)
    expect(compareVersions('0.4.2+build.7', '0.4.2')).toBe(0)
    expect(compareVersions('x', '0.4.2')).toBeUndefined()
  })

  it('drops a leading v for display only', () => {
    expect(displayVersion('v0.4.2')).toBe('0.4.2')
    expect(displayVersion('dev')).toBe('dev')
    expect(displayVersion(undefined)).toBe('')
  })
})
