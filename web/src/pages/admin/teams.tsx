import { EllipsisIcon, InfoIcon, PlusIcon, UsersRoundIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTeams, useWrite, writes } from '@/api/admin'
import { useAuth } from '@/api/auth'
import type { Team, TeamInput } from '@/api/types'
import { ReadOnlyNotice } from '@/components/admin/common'
import { WriteDialog } from '@/components/admin/write-dialog'
import { Savings } from '@/components/domain'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout, Field, Textarea } from '@/components/ui/form'
import { Card } from '@/components/ui/card'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { formatNumber } from '@/lib/format'
import { parseLabels, parseList, selectorSummary, teamIdProblem, UNASSIGNED } from '@/lib/teams'

interface Form {
  id: string
  displayName: string
  description: string
  namespaces: string
  namespaceLabels: string
  clusters: string
  ownerValues: string
}

const EMPTY: Form = {
  id: '',
  displayName: '',
  description: '',
  namespaces: '',
  namespaceLabels: '',
  clusters: '',
  ownerValues: '',
}

const toForm = (t: Team): Form => ({
  id: t.id,
  displayName: t.displayName === t.id ? '' : t.displayName,
  description: t.description ?? '',
  namespaces: t.namespaces.join('\n'),
  namespaceLabels: Object.entries(t.namespaceLabels)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n'),
  clusters: t.clusters.join('\n'),
  ownerValues: t.ownerValues.join('\n'),
})

function TeamDialog({
  team,
  open,
  onOpenChange,
  ownerLabel,
}: {
  /** The team being edited; none = create. */
  team?: Team
  open: boolean
  onOpenChange: (o: boolean) => void
  ownerLabel: string
}) {
  const [f, setF] = useState<Form>(EMPTY)
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (open) {
      setF(team ? toForm(team) : EMPTY)
      setTouched(false)
    }
  }, [open, team])
  const labels = parseLabels(f.namespaceLabels)
  const idErr = team ? undefined : teamIdProblem(f.id)
  const labelErr = labels.bad ? `"${labels.bad}" is not key=value.` : undefined
  const set = (patch: Partial<Form>) => setF((x) => ({ ...x, ...patch }))
  const write = useWrite(({ reason }: { reason: string }) => {
    const body: TeamInput = {
      id: f.id,
      displayName: f.displayName.trim() || undefined,
      description: f.description.trim() || undefined,
      namespaces: parseList(f.namespaces),
      namespaceLabels: labels.labels,
      clusters: parseList(f.clusters),
      ownerValues: parseList(f.ownerValues),
      reason,
    }
    return team ? writes.updateTeam({ id: team.id, version: team.version, body }) : writes.createTeam(body)
  })
  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      wide
      title={team ? `Edit team ${team.id}` : 'New team'}
      description="The workloads of the team are reassigned as soon as you save."
      confirmLabel={team ? 'Save team' : 'Create team'}
      reasonAutoFocus={false}
      testId="team-dialog"
      onSubmitAttempt={() => setTouched(true)}
      onConfirm={(o) => {
        if (idErr || labelErr) return Promise.reject(new Error('Fix the highlighted fields first.'))
        return write.mutateAsync(o)
      }}
      successMessage={team ? `Saved ${team.id}` : `Created ${f.id}`}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label="Team id"
          error={touched ? idErr : undefined}
          hint={team ? 'The id cannot be changed.' : 'A DNS label, chosen once. Used in URLs and filters.'}
        >
          <Input
            value={f.id}
            onChange={(e) => set({ id: e.target.value.trim() })}
            disabled={!!team}
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
            autoFocus={!team}
            data-testid="team-id"
          />
        </Field>
        <Field label="Display name" optional hint="Shown instead of the id.">
          <Input
            value={f.displayName}
            onChange={(e) => set({ displayName: e.target.value })}
            data-testid="team-display-name"
          />
        </Field>
        <Field label="Description" optional className="sm:col-span-2">
          <Input value={f.description} onChange={(e) => set({ description: e.target.value })} />
        </Field>
        <Field
          label="Namespaces"
          optional
          hint="Globs such as payments-*, one per line or separated by commas."
        >
          <Textarea
            rows={3}
            value={f.namespaces}
            onChange={(e) => set({ namespaces: e.target.value })}
            className="font-mono text-[13px]"
            data-testid="team-namespaces"
          />
        </Field>
        <Field
          label="Namespace labels"
          optional
          error={touched ? labelErr : undefined}
          hint="A namespace must have all of them. One key=value per line."
        >
          <Textarea
            rows={3}
            value={f.namespaceLabels}
            onChange={(e) => set({ namespaceLabels: e.target.value })}
            className="font-mono text-[13px]"
            data-testid="team-namespace-labels"
          />
        </Field>
        <Field label="Clusters" optional hint="Globs. Empty means every cluster.">
          <Textarea
            rows={2}
            value={f.clusters}
            onChange={(e) => set({ clusters: e.target.value })}
            className="font-mono text-[13px]"
          />
        </Field>
        <Field
          label="Owner values"
          optional
          hint={
            <>
              Values of the <span className="font-mono">{ownerLabel}</span> label that mean this team besides
              its id.
            </>
          }
        >
          <Textarea
            rows={2}
            value={f.ownerValues}
            onChange={(e) => set({ ownerValues: e.target.value })}
            className="font-mono text-[13px]"
            data-testid="team-owner-values"
          />
        </Field>
      </div>
    </WriteDialog>
  )
}

function Chips({ values, mono = true }: { values: string[]; mono?: boolean }) {
  if (!values.length) return <span className="text-muted-foreground/60">—</span>
  return (
    <div className="flex flex-wrap gap-1">
      {values.map((v) => (
        <Badge key={v} variant="muted" className={mono ? 'font-mono' : undefined}>
          {v}
        </Badge>
      ))}
    </div>
  )
}

export default function TeamsPage() {
  const auth = useAuth()
  const q = useTeams()
  const [edit, setEdit] = useState<Team | 'new'>()
  const [del, setDel] = useState<Team>()
  const canEdit = auth.can('admin') && !auth.teamScoped
  const remove = useWrite(({ reason }: { reason: string }) =>
    writes.deleteTeam({ id: del!.id, version: del!.version, reason }),
  )
  const ownerLabel = q.data?.ownerLabel ?? 'team'
  const items = q.data?.items ?? []
  const unassigned = q.data?.unassigned

  return (
    <div className="space-y-5">
      <PageHeader
        eyebrow="Administration"
        title="Teams"
        description="Who owns which workloads, so savings and access can be split by team."
        actions={
          canEdit ? (
            <Button size="sm" onClick={() => setEdit('new')} data-testid="new-team">
              <PlusIcon /> New team
            </Button>
          ) : null
        }
      />
      {!canEdit ? <ReadOnlyNotice need="admin" signedIn={auth.signedIn} /> : null}
      <Callout tone="info" icon={InfoIcon} title="How a workload gets its team">
        <div data-testid="ownership-help" className="space-y-1">
          <p>
            1. The <code className="font-mono">{ownerLabel}</code> label of the workload, else of its
            namespace, names the team: its id or one of its owner values.
          </p>
          <p>
            2. Otherwise the first team (by id) whose namespace selector matches: the namespace name matches
            one of its globs, has all its labels, and the cluster matches.
          </p>
          <p>3. Otherwise the workload is unassigned.</p>
        </div>
      </Callout>

      <Card className="overflow-hidden">
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : !items.length && !unassigned ? (
          <EmptyState
            icon={UsersRoundIcon}
            title="No teams yet"
            description={
              canEdit
                ? 'Create a team to see savings per team and to give people access to only their workloads.'
                : 'An admin can create teams.'
            }
            action={
              canEdit ? (
                <Button size="sm" onClick={() => setEdit('new')}>
                  <PlusIcon /> New team
                </Button>
              ) : undefined
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]" data-testid="teams-table">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">Team</th>
                  <th className="px-3 py-2 text-left font-medium">Selectors</th>
                  <th className="hidden px-3 py-2 text-left font-medium md:table-cell">Owner values</th>
                  <th className="px-3 py-2 text-right font-medium">Workloads</th>
                  <th className="px-3 py-2 text-right font-medium">Savings / mo</th>
                  {canEdit ? <th className="w-12" /> : null}
                </tr>
              </thead>
              <tbody>
                {items.map((t) => (
                  <tr key={t.id} className="border-b last:border-0" data-testid={`team-${t.id}`}>
                    <td className="px-4 py-2.5">
                      <div className="font-medium">{t.displayName}</div>
                      <div className="font-mono text-xs text-muted-foreground">{t.id}</div>
                      {t.description ? (
                        <div className="mt-0.5 max-w-64 text-xs text-muted-foreground">{t.description}</div>
                      ) : null}
                    </td>
                    <td className="max-w-80 px-3 py-2.5 text-muted-foreground" data-testid="team-selectors">
                      {selectorSummary(t)}
                    </td>
                    <td className="hidden px-3 py-2.5 md:table-cell">
                      <Chips values={t.ownerValues} />
                    </td>
                    <td className="px-3 py-2.5 text-right tabular">
                      <Link
                        to={`/opportunities?team=${encodeURIComponent(t.id)}`}
                        className="hover:underline"
                        aria-label={`${t.workloads} workloads of ${t.displayName}`}
                      >
                        {formatNumber(t.workloads)}
                      </Link>
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <Savings value={t.monthlySavingsUSD.total} />
                    </td>
                    {canEdit ? (
                      <td className="px-2 py-2.5 text-right">
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${t.id}`}>
                              <EllipsisIcon />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end" className="w-40">
                            <DropdownMenuItem onSelect={() => setEdit(t)}>Edit</DropdownMenuItem>
                            <DropdownMenuItem className="text-destructive" onSelect={() => setDel(t)}>
                              Delete
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </td>
                    ) : null}
                  </tr>
                ))}
                {unassigned ? (
                  <tr className="bg-muted/20" data-testid="team-unassigned">
                    <td className="px-4 py-2.5">
                      <div className="font-medium">Unassigned</div>
                      <div className="font-mono text-xs text-muted-foreground">{UNASSIGNED}</div>
                    </td>
                    <td className="px-3 py-2.5 text-muted-foreground">Workloads that no team claims</td>
                    <td className="hidden px-3 py-2.5 md:table-cell" />
                    <td className="px-3 py-2.5 text-right tabular">
                      <Link
                        to={`/opportunities?team=${UNASSIGNED}`}
                        className="hover:underline"
                        aria-label={`${unassigned.workloads} unassigned workloads`}
                      >
                        {formatNumber(unassigned.workloads)}
                      </Link>
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <Savings value={unassigned.monthlySavingsUSD.total} />
                    </td>
                    {canEdit ? <td /> : null}
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <TeamDialog
        open={!!edit}
        onOpenChange={(o) => !o && setEdit(undefined)}
        team={edit === 'new' ? undefined : edit}
        ownerLabel={ownerLabel}
      />
      <WriteDialog
        open={!!del}
        onOpenChange={(o) => !o && setDel(undefined)}
        tone="destructive"
        title={`Delete team ${del?.id}`}
        description={`Its ${del?.workloads ?? 0} workloads go to another team that claims them, else to Unassigned. The roles users have on this team are removed.`}
        confirmLabel="Delete team"
        testId="delete-team-dialog"
        onConfirm={(o) => remove.mutateAsync(o)}
        successMessage="Team deleted"
      />
    </div>
  )
}
