import { PlusIcon, XIcon } from 'lucide-react'
import { Link } from 'react-router'
import { useTeams } from '@/api/admin'
import { ROLE_RANK } from '@/api/auth'
import type { TeamRole, UserRole, UserScope } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Field, NativeSelect } from '@/components/ui/form'

export interface AccessValue {
  role: UserRole
  scope: UserScope
  teamRoles: TeamRole[]
}

/** The role the hub derives for a team-scoped user: the highest of its team roles. */
export const derivedRole = (teamRoles: TeamRole[]): UserRole =>
  teamRoles.reduce<UserRole>((hi, r) => (ROLE_RANK[r.role] > ROLE_RANK[hi] ? r.role : hi), 'viewer')

/** What is sent for a local user: admins are always fleet-wide and have no team roles. */
export function accessBody(v: AccessValue): AccessValue {
  if (v.role === 'admin') return { role: 'admin', scope: 'fleet', teamRoles: [] }
  if (v.scope === 'teams') return { role: derivedRole(v.teamRoles), scope: 'teams', teamRoles: v.teamRoles }
  return v
}

/** Problem with the access choice, if any (a team-scoped user needs at least one team). */
export function accessProblem(v: AccessValue): string | undefined {
  if (v.role !== 'admin' && v.scope === 'teams') {
    if (!v.teamRoles.length) return 'Add at least one team, or choose the whole fleet.'
    if (v.teamRoles.some((r) => !r.team)) return 'Choose a team for every row.'
  }
  return undefined
}

/** One row per team (each team once): pick the team and viewer or operator. */
export function TeamRolesEditor({
  value,
  onChange,
}: {
  value: TeamRole[]
  onChange: (v: TeamRole[]) => void
}) {
  const teams = useTeams()
  const items = teams.data?.items ?? []
  const free = items.filter((t) => !value.some((r) => r.team === t.id))
  const set = (i: number, patch: Partial<TeamRole>) =>
    onChange(value.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  return (
    <div className="flex flex-col gap-2" data-testid="team-roles">
      {value.map((r, i) => (
        <div key={i} className="flex items-center gap-2" data-testid="team-role-row">
          <NativeSelect
            value={r.team}
            onChange={(e) => set(i, { team: e.target.value })}
            aria-label="Team"
            className="min-w-0 flex-1"
          >
            <option value="">Choose a team…</option>
            {items
              .filter((t) => t.id === r.team || free.includes(t))
              .map((t) => (
                <option key={t.id} value={t.id}>
                  {t.displayName === t.id ? t.id : `${t.displayName} (${t.id})`}
                </option>
              ))}
            {r.team && !items.some((t) => t.id === r.team) ? <option value={r.team}>{r.team}</option> : null}
          </NativeSelect>
          <NativeSelect
            value={r.role}
            onChange={(e) => set(i, { role: e.target.value as TeamRole['role'] })}
            aria-label="Role on the team"
            className="w-32"
          >
            <option value="viewer">viewer</option>
            <option value="operator">operator</option>
          </NativeSelect>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="Remove team role"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
          >
            <XIcon />
          </Button>
        </div>
      ))}
      <div className="flex items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="xs"
          disabled={!free.length}
          onClick={() => onChange([...value, { team: '', role: 'viewer' }])}
          data-testid="add-team-role"
        >
          <PlusIcon /> Add team
        </Button>
        {teams.data && !items.length ? (
          <span className="text-xs text-muted-foreground">
            There are no teams yet.{' '}
            <Link to="/admin/teams" className="underline">
              Create one
            </Link>
            .
          </span>
        ) : null}
      </div>
    </div>
  )
}

/** Role, scope and team roles of a local user, as the create and edit dialogs show them. */
export function AccessFields({
  value,
  onChange,
  error,
}: {
  value: AccessValue
  onChange: (v: AccessValue) => void
  error?: string
}) {
  const admin = value.role === 'admin'
  const teamsScope = !admin && value.scope === 'teams'
  const roleHint = {
    viewer: 'Everything the read-only UI shows',
    operator: 'Plus workload actions (mode, exclude, approve once) and pausing',
    admin: 'Plus defaults, policies, recommender, pricing, clusters, tokens and users',
  }
  return (
    <>
      {teamsScope ? (
        <Field
          label="Role"
          className="sm:col-span-2"
          hint="Derived: the highest role the user has on a team."
        >
          <div className="flex h-9 items-center text-[13px]" data-testid="derived-role">
            {derivedRole(value.teamRoles)}
          </div>
        </Field>
      ) : (
        <Field label="Role" className="sm:col-span-2" hint={roleHint[value.role]}>
          <NativeSelect
            value={value.role}
            onChange={(e) => {
              const role = e.target.value as UserRole
              onChange(role === 'admin' ? { role, scope: 'fleet', teamRoles: [] } : { ...value, role })
            }}
            aria-label="Role"
            data-testid="user-role"
          >
            <option value="viewer">viewer</option>
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </NativeSelect>
        </Field>
      )}
      {admin ? (
        <p className="text-xs text-muted-foreground sm:col-span-2">
          Admins always see the whole fleet; they have no team roles.
        </p>
      ) : (
        <>
          <Field
            label="Scope"
            className="sm:col-span-2"
            hint={
              teamsScope
                ? 'Sees and acts only on the workloads of the teams below; no fleet-wide pages.'
                : 'Sees every workload. Team roles below can raise the role on some teams.'
            }
          >
            <NativeSelect
              value={value.scope}
              onChange={(e) => {
                const scope = e.target.value as UserScope
                onChange({
                  ...value,
                  scope,
                  role: scope === 'teams' ? derivedRole(value.teamRoles) : value.role,
                })
              }}
              aria-label="Scope"
              data-testid="user-scope"
            >
              <option value="fleet">Whole fleet</option>
              <option value="teams">Only some teams</option>
            </NativeSelect>
          </Field>
          <Field
            label="Team roles"
            className="sm:col-span-2"
            optional={!teamsScope}
            error={error}
            hint={teamsScope ? undefined : 'For example a fleet viewer who is operator on the payments team.'}
          >
            <TeamRolesEditor
              value={value.teamRoles}
              onChange={(teamRoles) =>
                onChange({
                  ...value,
                  teamRoles,
                  role: teamsScope ? derivedRole(teamRoles) : value.role,
                })
              }
            />
          </Field>
        </>
      )}
    </>
  )
}
