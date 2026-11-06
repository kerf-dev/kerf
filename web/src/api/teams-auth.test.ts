import { describe, expect, it } from 'vitest'
import { visibleAdminNav } from '@/components/layout/app-shell'
import { canOperateOnTeam, roleOnTeam, type Auth } from './auth'
import type { Me, UserRole } from './types'

const me = (m: Partial<Me> & { fleetRole?: UserRole }): Me => ({
  authMethod: 'session',
  role: m.role ?? 'viewer',
  scope: 'fleet',
  teamRoles: [],
  user: {
    username: 'u',
    displayName: 'U',
    role: m.fleetRole ?? m.role ?? 'viewer',
    disabled: false,
    mustChangePassword: false,
    createdAt: '2026-01-01T00:00:00Z',
    version: 1,
  },
  ...m,
})

describe('roles on a team', () => {
  it('a fleet user has its role everywhere, raised by a team role', () => {
    const v = me({
      role: 'operator',
      fleetRole: 'viewer',
      teamRoles: [{ team: 'payments', role: 'operator' }],
    })
    expect(roleOnTeam(v, 'payments')).toBe('operator')
    expect(roleOnTeam(v, 'cards')).toBe('viewer')
    expect(roleOnTeam(v, 'unassigned')).toBe('viewer')
    expect(canOperateOnTeam(v, 'payments')).toBe(true)
    expect(canOperateOnTeam(v, 'cards')).toBe(false)
    expect(canOperateOnTeam(v, undefined)).toBe(false)
  })

  it('a team binding never lowers a fleet role', () => {
    const o = me({ role: 'operator', teamRoles: [{ team: 'cards', role: 'viewer' }] })
    expect(roleOnTeam(o, 'cards')).toBe('operator')
    expect(canOperateOnTeam(o, 'cards')).toBe(true)
    expect(canOperateOnTeam(o, 'anything')).toBe(true)
  })

  it('a team-scoped user has roles on its teams only', () => {
    const t = me({
      role: 'operator',
      scope: 'teams',
      teamRoles: [
        { team: 'payments', role: 'operator' },
        { team: 'cards', role: 'viewer' },
      ],
    })
    expect(canOperateOnTeam(t, 'payments')).toBe(true)
    expect(canOperateOnTeam(t, 'cards')).toBe(false)
    expect(canOperateOnTeam(t, 'risk')).toBe(false)
    expect(canOperateOnTeam(t, 'unassigned')).toBe(false)
    expect(canOperateOnTeam(t, undefined)).toBe(false)
  })

  it('without a session or a user there is nothing', () => {
    expect(roleOnTeam(undefined, 'payments')).toBeUndefined()
    expect(canOperateOnTeam(undefined, 'payments')).toBe(false)
  })
})

describe('admin navigation', () => {
  const auth = (role: UserRole, teamScoped: boolean) =>
    ({
      can: (min: UserRole) =>
        ['viewer', 'operator', 'admin'].indexOf(role) >= ['viewer', 'operator', 'admin'].indexOf(min),
      teamScoped,
    }) as Auth

  it('everyone sees Teams, admins also Access', () => {
    expect(visibleAdminNav(auth('viewer', false)).map((n) => n.label)).toContain('Teams')
    const admin = visibleAdminNav(auth('admin', false)).map((n) => n.label)
    expect(admin).toEqual(expect.arrayContaining(['Teams', 'Access', 'Policies']))
    expect(admin.indexOf('Teams')).toBe(admin.indexOf('Access') + 1)
  })

  it('a team-scoped user sees only Teams, however high its role', () => {
    expect(visibleAdminNav(auth('operator', true)).map((n) => n.label)).toEqual(['Teams'])
  })
})
