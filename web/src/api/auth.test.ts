import { describe, expect, it } from 'vitest'
import { loginPath, meRefetchInterval, safeNext } from './auth'

const origin = 'https://kerf.example'

describe('safeNext', () => {
  it.each([
    ['/opportunities', '/opportunities'],
    ['/admin/policies?tab=git#top', '/admin/policies?tab=git#top'],
    ['/workloads/c1/ns/Deployment/a%2Fb', '/workloads/c1/ns/Deployment/a%2Fb'],
    ['/./clusters', '/clusters'],
  ])('keeps the same-site path %s', (next, want) => expect(safeNext(next, origin)).toBe(want))

  it.each([
    null,
    undefined,
    '',
    'opportunities',
    'https://evil.example/',
    'javascript:alert(1)',
    '//evil.example/',
    '/\\evil.example/',
    '/\t/evil.example/',
    '/\n/evil.example/',
    '\t//evil.example/',
    '/login',
    '/login?next=/admin',
    '/login/',
  ])('sends %j to the overview', (next) => expect(safeNext(next, origin)).toBe('/'))
})

describe('loginPath', () => {
  it('keeps the page to return to', () => {
    expect(loginPath({ pathname: '/opportunities', search: '?kind=StatefulSet' })).toBe(
      '/login?next=%2Fopportunities%3Fkind%3DStatefulSet',
    )
    expect(loginPath({ pathname: '/clusters', search: '', hash: '#x' })).toBe('/login?next=%2Fclusters%23x')
    expect(loginPath({ pathname: '/', search: '' })).toBe('/login')
  })
})

describe('meRefetchInterval', () => {
  const now = Date.parse('2026-11-06T10:00:00Z')
  it('checks right after the session ends, at most every 5 minutes', () => {
    expect(meRefetchInterval(undefined, now)).toBe(300_000)
    const at = (iso: string) =>
      meRefetchInterval({ authMethod: 'session', role: 'viewer', sessionExpiresAt: iso }, now)
    expect(at('2026-11-06T22:00:00Z')).toBe(300_000)
    expect(at('2026-11-06T10:01:00Z')).toBe(61_000)
    expect(at('2026-11-06T09:00:00Z')).toBe(5_000)
  })
})
