import { EllipsisIcon, KeyRoundIcon, PlusIcon, UserPlusIcon, UsersIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTokens, useUsers, useWrite, writes } from '@/api/admin'
import { useAuth } from '@/api/auth'
import { useClusters } from '@/api/queries'
import type {
  PasswordResetResult,
  TeamRole,
  TokenCreated,
  TokenInfo,
  TokenRole,
  User,
  UserRole,
  UserScope,
} from '@/api/types'
import { RoleBadge, SecretOnce } from '@/components/admin/common'
import { AccessFields, accessBody, accessProblem, type AccessValue } from '@/components/admin/team-roles'
import { TokenCreatedDialog } from '@/components/admin/install'
import { WriteDialog } from '@/components/admin/write-dialog'
import { passwordProblem } from '@/components/layout/change-password'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Checkbox, Field, NativeSelect } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'

/** Users the identity provider (OpenID Connect or SAML) manages: no password, access from its groups. */
const isIdP = (u: User) => u.source === 'oidc' || u.source === 'saml'

const ROLE_DESC: Record<UserRole, string> = {
  viewer: 'Everything the read-only UI shows',
  operator: 'Plus workload actions (mode, exclude, approve once) and pausing',
  admin: 'Plus defaults, policies, recommender, pricing, clusters, tokens and users',
}

function UserDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [f, setF] = useState({
    username: '',
    displayName: '',
    role: 'viewer' as UserRole,
    scope: 'fleet' as UserScope,
    teamRoles: [] as TeamRole[],
    password: '',
    mustChangePassword: true,
  })
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (open) {
      setF({
        username: '',
        displayName: '',
        role: 'viewer',
        scope: 'fleet',
        teamRoles: [],
        password: '',
        mustChangePassword: true,
      })
      setTouched(false)
    }
  }, [open])
  const userErr = !/^[a-z0-9][a-z0-9._-]{1,62}$/.test(f.username)
    ? 'Lowercase letters, digits, ".", "_" and "-"; 2–63 characters.'
    : undefined
  const pwErr = passwordProblem(f.password)
  const accessErr = accessProblem(f)
  const write = useWrite(({ reason }: { reason: string }) =>
    writes.createUser({
      ...f,
      ...accessBody(f),
      displayName: f.displayName || undefined,
      reason,
    }),
  )
  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Add a user"
      description="A local user. OIDC sign-in comes later with the same roles."
      confirmLabel="Add user"
      onConfirm={(o) => {
        setTouched(true)
        if (userErr || pwErr || accessErr)
          return Promise.reject(new Error('Fix the highlighted fields first.'))
        return write.mutateAsync(o)
      }}
      successMessage={`Added ${f.username}`}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Username" error={touched ? userErr : undefined}>
          <Input
            value={f.username}
            onChange={(e) => setF({ ...f, username: e.target.value.trim() })}
            className="font-mono"
            autoComplete="off"
            data-testid="new-username"
          />
        </Field>
        <Field label="Display name" optional>
          <Input value={f.displayName} onChange={(e) => setF({ ...f, displayName: e.target.value })} />
        </Field>
        <AccessFields
          value={f}
          onChange={(a) => setF({ ...f, ...a })}
          error={touched ? accessErr : undefined}
        />
        <Field
          label="Initial password"
          className="sm:col-span-2"
          error={touched ? pwErr : undefined}
          hint="Share it securely."
        >
          <Input
            type="password"
            value={f.password}
            onChange={(e) => setF({ ...f, password: e.target.value })}
            autoComplete="new-password"
            data-testid="new-password"
          />
        </Field>
        <label className="flex items-center gap-2 text-[13px] sm:col-span-2">
          <Checkbox
            checked={f.mustChangePassword}
            onCheckedChange={(v) => setF({ ...f, mustChangePassword: v === true })}
          />{' '}
          Must change it at first sign-in
        </label>
      </div>
    </WriteDialog>
  )
}

/** Role, scope and team roles of a local user. */
function AccessDialog({ user, onClose }: { user?: User; onClose: () => void }) {
  const [v, setV] = useState<AccessValue>({ role: 'viewer', scope: 'fleet', teamRoles: [] })
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (user) {
      setV({ role: user.role, scope: user.scope ?? 'fleet', teamRoles: user.teamRoles ?? [] })
      setTouched(false)
    }
  }, [user])
  const err = accessProblem(v)
  const write = useWrite(({ reason }: { reason: string }) =>
    writes.updateUser({
      username: user!.username,
      version: user!.version,
      body: { ...accessBody(v), reason },
    }),
  )
  return (
    <WriteDialog
      open={!!user}
      onOpenChange={(o) => !o && onClose()}
      title={`Edit access of ${user?.username}`}
      description="Role, scope and team roles take effect at their next request."
      confirmLabel="Save access"
      reasonAutoFocus={false}
      testId="access-dialog"
      onSubmitAttempt={() => setTouched(true)}
      onConfirm={(o) => {
        if (err) return Promise.reject(new Error('Fix the highlighted fields first.'))
        return write.mutateAsync(o)
      }}
      successMessage="Access updated"
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <AccessFields value={v} onChange={setV} error={touched ? err : undefined} />
      </div>
    </WriteDialog>
  )
}

/** Scope and team roles of a user, in a table cell. */
function AccessCell({ u }: { u: User }) {
  const teams = u.scope === 'teams'
  const roles = u.teamRoles ?? []
  return (
    <div className="space-y-1" data-testid="user-access">
      {teams ? (
        <Tooltip content="Sees and acts only on the workloads of its teams">
          <Badge variant="info">Some teams</Badge>
        </Tooltip>
      ) : (
        <span className="text-muted-foreground">Whole fleet</span>
      )}
      {roles.length ? (
        <div className="flex flex-wrap gap-1">
          {roles.map((r) => (
            <Badge key={r.team} variant="muted" data-testid="team-role-badge">
              <span className="font-mono">{r.team}</span> · {r.role}
            </Badge>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function UsersTab() {
  const auth = useAuth()
  const users = useUsers()
  const [add, setAdd] = useState(false)
  const [act, setAct] = useState<{
    kind: 'role' | 'disable' | 'enable' | 'delete' | 'reset'
    user: User
    role?: UserRole
  } | null>(null)
  const [editAccess, setEditAccess] = useState<User>()
  const [reset, setReset] = useState<{ user: string; r: PasswordResetResult }>()
  const write = useWrite(async ({ reason }: { reason: string }): Promise<unknown> => {
    const u = act!.user
    switch (act!.kind) {
      case 'role':
        return writes.updateUser({
          username: u.username,
          version: u.version,
          // An admin is always fleet-wide, without team roles.
          body: {
            role: act!.role!,
            ...(act!.role === 'admin' ? { scope: 'fleet' as const, teamRoles: [] } : {}),
            reason,
          },
        })
      case 'disable':
      case 'enable':
        return writes.updateUser({
          username: u.username,
          version: u.version,
          body: { disabled: act!.kind === 'disable', reason },
        })
      case 'delete':
        return writes.deleteUser({ username: u.username, version: u.version, reason })
      case 'reset':
        return writes.resetPassword({ username: u.username, reason })
    }
  })
  const me = auth.me?.user?.username
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-[13px] text-muted-foreground">
          Local users, their roles and the teams they can see. Disabling or deleting signs them out.
        </p>
        <Button size="sm" onClick={() => setAdd(true)} data-testid="add-user">
          <UserPlusIcon /> Add user
        </Button>
      </div>
      <Card className="overflow-hidden">
        {users.isError ? (
          <ErrorState error={users.error} onRetry={() => users.refetch()} />
        ) : !users.data ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : !users.data.items.length ? (
          <EmptyState icon={UsersIcon} title="No users" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]" data-testid="users-table">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">User</th>
                  <th className="px-3 py-2 text-left font-medium">Role</th>
                  <th className="px-3 py-2 text-left font-medium">Access</th>
                  <th className="px-3 py-2 text-left font-medium">Status</th>
                  <th className="hidden px-3 py-2 text-left font-medium md:table-cell">Last sign-in</th>
                  <th className="w-12" />
                </tr>
              </thead>
              <tbody>
                {users.data.items
                  .filter((u) => !u.deletedAt)
                  .map((u) => (
                    <tr
                      key={u.username}
                      className={cn('border-b last:border-0', u.disabled && 'opacity-60')}
                      data-testid={`user-${u.username}`}
                    >
                      <td className="px-4 py-2.5">
                        <div className="font-medium">
                          {u.displayName || u.username}
                          {u.username === me ? (
                            <span className="ml-1.5 text-xs text-muted-foreground">(you)</span>
                          ) : null}
                        </div>
                        <div className="font-mono text-xs text-muted-foreground">{u.username}</div>
                      </td>
                      <td className="px-3 py-2.5">
                        <RoleBadge role={u.role} />
                      </td>
                      <td className="px-3 py-2.5">
                        <AccessCell u={u} />
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap gap-1">
                          {u.disabled ? (
                            <Badge variant="muted">disabled</Badge>
                          ) : (
                            <Badge variant="success">active</Badge>
                          )}
                          {u.lockedUntil && Date.parse(u.lockedUntil) > Date.now() ? (
                            <Tooltip
                              content={`Too many failed sign-ins; locked until ${formatDateTime(u.lockedUntil)}`}
                            >
                              <Badge variant="warning">locked</Badge>
                            </Tooltip>
                          ) : null}
                          {u.mustChangePassword ? <Badge variant="info">must change password</Badge> : null}
                          {isIdP(u) ? (
                            <Tooltip
                              content={`Signs in with the identity provider (${u.source === 'saml' ? 'SAML' : 'OpenID Connect'}); the role, scope and team roles come from its groups`}
                            >
                              <Badge variant="info" data-testid="idp-user">
                                identity provider · {u.source === 'saml' ? 'SAML' : 'OIDC'}
                              </Badge>
                            </Tooltip>
                          ) : null}
                        </div>
                      </td>
                      <td className="hidden px-3 py-2.5 text-muted-foreground md:table-cell">
                        {u.lastLoginAt ? relativeTime(u.lastLoginAt) : 'never'}
                      </td>
                      <td className="px-2 py-2.5 text-right">
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${u.username}`}>
                              <EllipsisIcon />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end" className="w-52">
                            {/* An IdP user's role comes from its groups and it has no password. */}
                            {isIdP(u) ? null : (
                              <DropdownMenuItem onSelect={() => setEditAccess(u)}>
                                Edit scope and teams
                              </DropdownMenuItem>
                            )}
                            {(['viewer', 'operator', 'admin'] as UserRole[])
                              .filter((r) => r !== u.role && !isIdP(u) && u.scope !== 'teams')
                              .map((r) => (
                                <DropdownMenuItem
                                  key={r}
                                  onSelect={() => setAct({ kind: 'role', user: u, role: r })}
                                >
                                  Make {r}
                                </DropdownMenuItem>
                              ))}
                            {isIdP(u) ? null : (
                              <>
                                <DropdownMenuSeparator />
                                <DropdownMenuItem onSelect={() => setAct({ kind: 'reset', user: u })}>
                                  <KeyRoundIcon /> Reset password
                                </DropdownMenuItem>
                              </>
                            )}
                            <DropdownMenuItem
                              onSelect={() => setAct({ kind: u.disabled ? 'enable' : 'disable', user: u })}
                              disabled={u.username === me}
                            >
                              {u.disabled ? 'Enable' : 'Disable'}
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              className="text-destructive"
                              onSelect={() => setAct({ kind: 'delete', user: u })}
                              disabled={u.username === me}
                            >
                              Delete
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <UserDialog open={add} onOpenChange={setAdd} />
      <AccessDialog user={editAccess} onClose={() => setEditAccess(undefined)} />
      <WriteDialog
        open={!!act}
        onOpenChange={(o) => !o && setAct(null)}
        tone={act?.kind === 'delete' || act?.kind === 'disable' ? 'destructive' : 'default'}
        title={
          act?.kind === 'role'
            ? `Make ${act.user.username} ${act.role}`
            : act?.kind === 'reset'
              ? `Reset the password of ${act.user.username}`
              : `${act?.kind === 'delete' ? 'Delete' : act?.kind === 'disable' ? 'Disable' : 'Enable'} ${act?.user.username}`
        }
        description={
          act?.kind === 'role'
            ? ROLE_DESC[act.role!]
            : act?.kind === 'reset'
              ? 'The hub generates a temporary password, shown once. The user must change it at the next sign-in.'
              : act?.kind === 'delete'
                ? 'Their sessions end; their changes stay in the audit log.'
                : undefined
        }
        confirmLabel={act?.kind === 'reset' ? 'Reset password' : 'Confirm'}
        onConfirm={async (o) => {
          const res = await write.mutateAsync(o)
          if (act?.kind === 'reset') setReset({ user: act.user.username, r: res as PasswordResetResult })
        }}
        successMessage={act?.kind === 'reset' ? undefined : 'User updated'}
      />
      <Dialog open={!!reset} onOpenChange={(o) => !o && setReset(undefined)}>
        <DialogContent className="top-[14%] w-[min(520px,calc(100vw-2rem))] p-5">
          <DialogTitle className="text-base font-semibold">Temporary password for {reset?.user}</DialogTitle>
          <DialogDescription className="mt-1 mb-4 text-[13px] text-muted-foreground">
            They must choose a new password when they sign in.
          </DialogDescription>
          {reset?.r.temporaryPassword ? (
            <SecretOnce value={reset.r.temporaryPassword} what="password" />
          ) : null}
          <div className="mt-4 flex justify-end">
            <Button size="sm" onClick={() => setReset(undefined)}>
              Done
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function TokenDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  onCreated: (t: TokenCreated) => void
}) {
  const clusters = useClusters()
  const [f, setF] = useState({ role: 'ui' as TokenRole, cluster: '', description: '', expires: '90' })
  useEffect(() => {
    if (open) setF({ role: 'ui', cluster: '', description: '', expires: '90' })
  }, [open])
  const write = useWrite(({ reason }: { reason: string }) =>
    writes.createToken({
      role: f.role,
      description: f.description,
      ...(f.role === 'agent' ? { cluster: f.cluster } : {}),
      ...(f.expires !== 'never'
        ? { expiresAt: new Date(Date.now() + Number(f.expires) * 86400_000).toISOString() }
        : {}),
      reason,
    }),
  )
  const bad = !f.description.trim() || (f.role === 'agent' && !f.cluster)
  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title="New API token"
      description="Machine tokens: agent tokens are per cluster; ui tokens are read-only; admin tokens can do everything an admin can; the pricing token only reaches the metrics bindings and the datasource query proxy."
      confirmLabel="Create token"
      confirmDisabled={bad}
      onConfirm={async (o) => onCreated((await write.mutateAsync(o)) as TokenCreated)}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Role">
          <NativeSelect value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as TokenRole })}>
            <option value="ui">ui (read-only)</option>
            <option value="agent">agent</option>
            <option value="admin">admin</option>
            <option value="pricing">pricing (pricing service)</option>
          </NativeSelect>
        </Field>
        {f.role === 'agent' ? (
          <Field label="Cluster">
            <NativeSelect value={f.cluster} onChange={(e) => setF({ ...f, cluster: e.target.value })}>
              <option value="">Choose…</option>
              {(clusters.data?.items ?? []).map((c) => (
                <option key={c.name} value={c.name}>
                  {c.name}
                </option>
              ))}
            </NativeSelect>
          </Field>
        ) : (
          <Field label="Expires">
            <NativeSelect value={f.expires} onChange={(e) => setF({ ...f, expires: e.target.value })}>
              <option value="30">in 30 days</option>
              <option value="90">in 90 days</option>
              <option value="365">in a year</option>
              <option value="never">never</option>
            </NativeSelect>
          </Field>
        )}
        <Field label="Description" className="sm:col-span-2" hint="Who or what uses it.">
          <Input
            value={f.description}
            onChange={(e) => setF({ ...f, description: e.target.value })}
            placeholder="grafana link"
            data-testid="token-description"
          />
        </Field>
      </div>
    </WriteDialog>
  )
}

function TokensTab() {
  const [revoked, setRevoked] = useState(false)
  const tokens = useTokens({ includeRevoked: revoked })
  const [create, setCreate] = useState(false)
  const [created, setCreated] = useState<TokenCreated>()
  const [rev, setRev] = useState<TokenInfo>()
  const revoke = useWrite(({ reason }: { reason: string }) => writes.revokeToken({ id: rev!.id, reason }))
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <label className="flex items-center gap-2 text-[13px] text-muted-foreground">
          <Checkbox checked={revoked} onCheckedChange={(v) => setRevoked(v === true)} /> Show revoked
        </label>
        <Button size="sm" onClick={() => setCreate(true)} data-testid="create-token">
          <PlusIcon /> New token
        </Button>
      </div>
      <Card className="overflow-hidden">
        {tokens.isError ? (
          <ErrorState error={tokens.error} onRetry={() => tokens.refetch()} />
        ) : !tokens.data ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : !tokens.data.items.length ? (
          <EmptyState icon={KeyRoundIcon} title="No tokens" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]" data-testid="tokens-table">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">Token</th>
                  <th className="px-3 py-2 text-left font-medium">Role</th>
                  <th className="hidden px-3 py-2 text-left font-medium md:table-cell">Created</th>
                  <th className="px-3 py-2 text-left font-medium">Expires</th>
                  <th className="px-3 py-2 text-left font-medium">Last used</th>
                  <th className="w-24" />
                </tr>
              </thead>
              <tbody>
                {tokens.data.items.map((t) => {
                  const expired = t.expiresAt && Date.parse(t.expiresAt) < Date.now()
                  const soon =
                    t.expiresAt && !expired && Date.parse(t.expiresAt) - Date.now() < 14 * 86400_000
                  return (
                    <tr
                      key={t.id}
                      className={cn('border-b last:border-0', (t.revokedAt || expired) && 'opacity-60')}
                    >
                      <td className="px-4 py-2.5">
                        <div className="font-medium">{t.description}</div>
                        <div className="font-mono text-xs text-muted-foreground">
                          #{t.id}
                          {t.cluster ? ` · ${t.cluster}` : ''}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <Badge
                          variant={t.role === 'admin' ? 'default' : t.role === 'agent' ? 'info' : 'muted'}
                          className="font-mono"
                        >
                          {t.role}
                        </Badge>
                      </td>
                      <td className="hidden px-3 py-2.5 text-muted-foreground md:table-cell">
                        {formatDateTime(t.createdAt, true)}
                        {t.createdBy ? ` · ${t.createdBy}` : ''}
                      </td>
                      <td className="px-3 py-2.5">
                        {t.revokedAt ? (
                          <Badge variant="muted">revoked</Badge>
                        ) : expired ? (
                          <Badge variant="muted">expired</Badge>
                        ) : t.expiresAt ? (
                          <span
                            className={cn(
                              soon &&
                                'font-medium text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]',
                            )}
                          >
                            {relativeTime(t.expiresAt)}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">never</span>
                        )}
                      </td>
                      <td className="px-3 py-2.5 text-muted-foreground">
                        {t.lastUsedAt ? relativeTime(t.lastUsedAt) : 'never'}
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        {!t.revokedAt ? (
                          <Button
                            size="xs"
                            variant="ghost"
                            className="text-destructive hover:text-destructive"
                            onClick={() => setRev(t)}
                          >
                            Revoke
                          </Button>
                        ) : null}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <TokenDialog open={create} onOpenChange={setCreate} onCreated={setCreated} />
      <TokenCreatedDialog
        created={created}
        onClose={() => setCreated(undefined)}
        cluster={created?.token.cluster}
      />
      <WriteDialog
        open={!!rev}
        onOpenChange={(o) => !o && setRev(undefined)}
        tone="destructive"
        title={`Revoke token #${rev?.id}`}
        description={`${rev?.description} (${rev?.role}${rev?.cluster ? `, ${rev.cluster}` : ''}) stops working immediately.`}
        confirmLabel="Revoke token"
        onConfirm={(o) => revoke.mutateAsync(o)}
        successMessage="Token revoked"
      />
    </div>
  )
}

export default function AccessPage() {
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'tokens' ? 'tokens' : 'users'
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Access"
        description="Who can sign in, with which role, and the machine tokens agents and integrations use."
      />
      <Tabs value={tab} onValueChange={(v) => setParams(v === 'tokens' ? { tab: v } : {}, { replace: true })}>
        <TabsList>
          <TabsTrigger value="users">
            <UsersIcon /> Users
          </TabsTrigger>
          <TabsTrigger value="tokens">
            <KeyRoundIcon /> API tokens
          </TabsTrigger>
        </TabsList>
        <TabsContent value="users" className="mt-4">
          <UsersTab />
        </TabsContent>
        <TabsContent value="tokens" className="mt-4">
          <TokensTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}
