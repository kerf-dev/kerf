import {
  ArrowRightIcon,
  BoxesIcon,
  CircleAlertIcon,
  CircleCheckIcon,
  CircleDashedIcon,
  CircleOffIcon,
  CrownIcon,
  ExternalLinkIcon,
  FlaskConicalIcon,
  Loader2Icon,
  RefreshCwIcon,
  ServerIcon,
  ShieldCheckIcon,
  SparklesIcon,
  TriangleAlertIcon,
} from 'lucide-react'
import { Link } from 'react-router'
import { useMaintenance, useWrite, writes } from '@/api/admin'
import { useAuth } from '@/api/auth'
import type { ComponentVersion, Maintenance, UpdateState, UpdateStatus, VersionCompat } from '@/api/types'
import { CodeBlock } from '@/components/admin/common'
import { WriteError } from '@/components/admin/write-dialog'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Callout } from '@/components/ui/form'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { displayVersion, versionCompat } from '@/lib/version'

/** The version of this UI bundle (build-time constant, WO-33). */
const WEB_VERSION: string = __KERF_VERSION__

const STATE: Record<
  UpdateState,
  {
    label: string
    variant: 'success' | 'info' | 'muted' | 'destructive' | 'warning'
    icon: typeof CircleCheckIcon
  }
> = {
  up_to_date: { label: 'Up to date', variant: 'success', icon: CircleCheckIcon },
  update_available: { label: 'Update available', variant: 'info', icon: SparklesIcon },
  disabled: { label: 'Check disabled', variant: 'muted', icon: CircleOffIcon },
  failed: { label: 'Check failed', variant: 'destructive', icon: CircleAlertIcon },
  pending: { label: 'Not checked yet', variant: 'muted', icon: CircleDashedIcon },
  dev_build: { label: 'Development build', variant: 'warning', icon: FlaskConicalIcon },
}

function UpdateStateBadge({ state }: { state: UpdateState }) {
  const s = STATE[state]
  return (
    <Badge variant={s.variant} data-testid="update-state" data-state={state}>
      <s.icon /> {s.label}
    </Badge>
  )
}

const COMPAT: Record<VersionCompat, { label: string; variant: 'success' | 'warning' | 'muted' }> = {
  same: { label: 'matches hub', variant: 'success' },
  older: { label: 'older than hub', variant: 'warning' },
  newer: { label: 'newer than hub', variant: 'warning' },
  unknown: { label: 'not compared', variant: 'muted' },
}

const mismatch = (c: VersionCompat) => c === 'older' || c === 'newer'

function CompatBadge({ compat }: { compat: VersionCompat }) {
  const c = COMPAT[compat]
  return (
    <Badge variant={c.variant} data-compat={compat}>
      {compat === 'same' ? <CircleCheckIcon /> : mismatch(compat) ? <TriangleAlertIcon /> : null}
      {c.label}
    </Badge>
  )
}

function Version({ v }: { v?: string }) {
  return v ? (
    <span className="font-mono text-[13px] font-medium">{displayVersion(v)}</span>
  ) : (
    <span className="text-muted-foreground">—</span>
  )
}

function Seen({ at }: { at?: string }) {
  if (!at) return <span className="text-muted-foreground">—</span>
  return (
    <Tooltip content={formatDateTime(at)}>
      <span className="text-muted-foreground">{relativeTime(at)}</span>
    </Tooltip>
  )
}

// ---------------------------------------------------------------- updates

function CheckNowButton({ update }: { update: UpdateStatus }) {
  const auth = useAuth()
  const check = useWrite(() => writes.checkForUpdate())
  const isAdmin = auth.can('admin')
  const why = !isAdmin
    ? 'Needs the admin role.'
    : !update.enabled
      ? 'The update check is off (chart value hub.updateCheck.enabled).'
      : null
  return (
    <div className="flex flex-col items-end gap-2">
      <Tooltip content={why}>
        <span>
          <Button
            size="sm"
            variant="outline"
            disabled={!!why || check.isPending}
            onClick={() => check.mutate(undefined)}
            data-testid="check-now"
          >
            {check.isPending ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />} Check now
          </Button>
        </span>
      </Tooltip>
      {check.error ? (
        <div className="max-w-sm">
          <WriteError error={check.error} />
        </div>
      ) : null}
    </div>
  )
}

function UpdatesCard({ m }: { m: Maintenance }) {
  const u = m.update
  const host = (() => {
    try {
      return new URL(u.source).host + new URL(u.source).pathname
    } catch {
      return u.source
    }
  })()
  const showLatest = !!u.latestVersion && u.state !== 'disabled'
  return (
    <Card data-testid="updates-card">
      <CardHeader>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>Updates</CardTitle>
            <UpdateStateBadge state={u.state} />
          </div>
          <CardDescription>
            {u.enabled ? (
              <>
                {u.checkedAt ? (
                  <Tooltip content={formatDateTime(u.checkedAt)}>
                    <span>Checked {relativeTime(u.checkedAt)}</span>
                  </Tooltip>
                ) : (
                  'Not checked yet'
                )}{' '}
                · every {u.interval} by the hub leader · <span className="font-mono text-xs">{host}</span>
              </>
            ) : (
              'The hub does not look for new releases.'
            )}
          </CardDescription>
        </div>
        <CheckNowButton update={u} />
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-end gap-x-8 gap-y-3">
          <div>
            <div className="text-xs text-muted-foreground">Running</div>
            <div className="font-mono text-2xl font-semibold tracking-tight" data-testid="current-version">
              {displayVersion(u.currentVersion) || '—'}
            </div>
          </div>
          {!showLatest && u.enabled && u.checkedAt && !u.error ? (
            <div className="mb-1 text-[13px] text-muted-foreground" data-testid="no-release">
              No stable release published yet.
            </div>
          ) : null}
          {showLatest ? (
            <>
              <ArrowRightIcon className="mb-2 size-4 text-muted-foreground" />
              <div>
                <div className="text-xs text-muted-foreground">
                  {u.state === 'failed' ? 'Latest release (last known)' : 'Latest release'}
                </div>
                <div
                  className={cn(
                    'font-mono text-2xl font-semibold tracking-tight',
                    u.state === 'update_available' && 'text-info',
                  )}
                  data-testid="latest-version"
                >
                  {displayVersion(u.latestVersion)}
                </div>
              </div>
              <div className="mb-1 text-[13px] text-muted-foreground">
                {u.releaseName ? <span className="font-medium text-foreground">{u.releaseName}</span> : null}
                {u.publishedAt ? (
                  <span>
                    {u.releaseName ? ' · ' : ''}released{' '}
                    <Tooltip content={formatDateTime(u.publishedAt)}>
                      <span>{relativeTime(u.publishedAt)}</span>
                    </Tooltip>
                  </span>
                ) : null}
                {u.releaseURL ? (
                  <>
                    {' · '}
                    <a
                      href={u.releaseURL}
                      target="_blank"
                      rel="noreferrer"
                      className="inline-flex items-center gap-1 text-primary hover:underline"
                      data-testid="release-notes"
                    >
                      Release notes <ExternalLinkIcon className="size-3" />
                    </a>
                  </>
                ) : null}
              </div>
            </>
          ) : null}
        </div>

        {u.state === 'up_to_date' ? (
          <Callout tone="success" icon={CircleCheckIcon}>
            This is the latest stable release of Kerf.
          </Callout>
        ) : null}
        {u.state === 'failed' ? (
          <Callout tone="destructive" icon={CircleAlertIcon} title="The last check failed">
            <span className="font-mono text-xs break-all" data-testid="update-error">
              {u.error || 'Unknown error.'}
            </span>
            {u.lastSuccessAt ? (
              <div className="mt-1">Last successful check {relativeTime(u.lastSuccessAt)}.</div>
            ) : null}
            <div className="mt-1">
              The hub needs HTTPS egress to {host.split('/')[0]} (set <code>HTTPS_PROXY</code> with{' '}
              <code>hub.extraEnv</code> behind a proxy), or turn the check off.
            </div>
          </Callout>
        ) : null}
        {u.state === 'disabled' ? (
          <Callout tone="muted" icon={CircleOffIcon}>
            Turned off with the chart value <code>hub.updateCheck.enabled: false</code>. Watch{' '}
            <a
              href="https://github.com/kerf-dev/kerf/releases"
              target="_blank"
              rel="noreferrer"
              className="text-primary hover:underline"
            >
              github.com/kerf-dev/kerf/releases
            </a>{' '}
            for new versions.
          </Callout>
        ) : null}
        {u.state === 'pending' ? (
          <Callout tone="info" icon={CircleDashedIcon}>
            The hub leader runs the first check within a minute of starting.
          </Callout>
        ) : null}
        {u.state === 'dev_build' ? (
          <Callout tone="warning" icon={FlaskConicalIcon}>
            This hub runs a development build ({u.currentVersion || 'no version'}), so it is not compared with
            releases.
          </Callout>
        ) : null}

        {u.state === 'update_available' && m.upgrade ? (
          <div className="space-y-3 border-t pt-4" data-testid="upgrade-commands">
            <div>
              <div className="text-sm font-semibold">Upgrade to {displayVersion(m.upgrade.version)}</div>
              <p className="text-[13px] text-muted-foreground">
                Read the release notes first. Upgrade the hub cluster, then each workload cluster. Replace the{' '}
                <code>&lt;…&gt;</code> parts with your kube contexts and values files.{' '}
                <a
                  href={m.upgrade.docsURL}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-primary hover:underline"
                >
                  Upgrade guide <ExternalLinkIcon className="size-3" />
                </a>
              </p>
            </div>
            <div className="space-y-1.5">
              <div className="text-xs font-medium text-muted-foreground">1. Hub cluster</div>
              <CodeBlock code={m.upgrade.hubCommand} testId="hub-command" />
            </div>
            <div className="space-y-1.5">
              <div className="text-xs font-medium text-muted-foreground">2. Each workload cluster</div>
              <CodeBlock code={m.upgrade.spokeCommand} testId="spoke-command" />
            </div>
          </div>
        ) : null}

        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <ShieldCheckIcon className="size-3.5 shrink-0" />
          Only reads public release data; sends nothing about this installation.
        </p>
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- hub cluster

interface HubRow {
  key: string
  component: string
  instance: React.ReactNode
  version?: string
  compat: VersionCompat
  message?: string
  lastSeenAt?: string
}

function HubTable({ m }: { m: Maintenance }) {
  const hubs = m.components.filter((c) => c.component === 'hub')
  const pricing = m.components.filter((c) => c.component === 'pricing')
  const webCompat = versionCompat(WEB_VERSION, m.hubVersion)
  const rows: HubRow[] = [
    ...hubs.map((c) => ({
      key: `hub-${c.instance}`,
      component: 'hub',
      instance: (
        <span className="inline-flex flex-wrap items-center gap-1.5">
          <span className="font-mono text-xs">{c.instance}</span>
          {c.leader ? (
            <Tooltip content="Runs the recommender, readiness checks and the update check.">
              <Badge variant="default" data-testid="leader-badge">
                <CrownIcon /> leader
              </Badge>
            </Tooltip>
          ) : null}
          {c.startedAt ? (
            <span className="text-xs text-muted-foreground">started {relativeTime(c.startedAt)}</span>
          ) : null}
        </span>
      ),
      version: c.version,
      compat: c.compat,
      message: c.message,
      lastSeenAt: c.lastSeenAt,
    })),
    {
      key: 'web',
      component: 'web',
      instance: <span className="text-muted-foreground">this UI</span>,
      version: WEB_VERSION,
      compat: webCompat,
      message:
        webCompat === 'older'
          ? `Older than the hub (${displayVersion(m.hubVersion)}): upgrade the Kerf chart on the hub cluster.`
          : webCompat === 'newer'
            ? `Newer than the hub (${displayVersion(m.hubVersion)}): the hub has not rolled out yet.`
            : undefined,
    },
    ...(pricing.length
      ? pricing.map((c) => ({
          key: `pricing-${c.instance}`,
          component: 'pricing',
          instance: <span className="font-mono text-xs">{c.instance || '—'}</span>,
          version: c.version,
          compat: c.compat,
          message: c.message,
          lastSeenAt: c.lastSeenAt,
        }))
      : [
          {
            key: 'pricing',
            component: 'pricing',
            instance: <span className="text-muted-foreground">not seen in the last hour</span>,
            compat: 'unknown' as const,
            message:
              'The pricing service reports its version when it reads from the hub (every few minutes).',
          },
        ]),
  ]
  const hubVersions = new Set(hubs.map((h) => h.version))
  return (
    <Card data-testid="hub-components">
      <CardHeader>
        <div>
          <CardTitle className="flex items-center gap-2">
            <ServerIcon className="size-4 text-muted-foreground" /> Hub cluster
          </CardTitle>
          <CardDescription>
            Hub {displayVersion(m.hubVersion) || '—'}
            {m.chartVersion ? ` · chart ${displayVersion(m.chartVersion)}` : ''} · {hubs.length} hub replica
            {hubs.length === 1 ? '' : 's'}
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        {hubVersions.size > 1 ? (
          <Callout tone="info" icon={RefreshCwIcon}>
            The hub replicas run different versions: a rolling upgrade is in progress.
          </Callout>
        ) : null}
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full min-w-[640px] text-[13px]">
            <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2 text-left font-medium">Component</th>
                <th className="px-3 py-2 text-left font-medium">Instance</th>
                <th className="px-3 py-2 text-left font-medium">Version</th>
                <th className="px-3 py-2 text-left font-medium">Status</th>
                <th className="px-3 py-2 text-right font-medium">Last seen</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr
                  key={r.key}
                  className={cn('border-b align-top last:border-0', mismatch(r.compat) && 'bg-warning/6')}
                  data-testid={`component-${r.key}`}
                  data-compat={r.compat}
                >
                  <td className="px-4 py-2.5 font-medium">{r.component}</td>
                  <td className="px-3 py-2.5">{r.instance}</td>
                  <td className="px-3 py-2.5">
                    <Version v={r.version} />
                  </td>
                  <td className="px-3 py-2.5">
                    <CompatBadge compat={r.compat} />
                    {r.message ? <Note text={r.message} warn={mismatch(r.compat)} /> : null}
                  </td>
                  <td className="px-3 py-2.5 text-right">
                    <Seen at={r.lastSeenAt} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
  )
}

function Note({ text, warn }: { text: string; warn?: boolean }) {
  return (
    <div
      className={cn(
        'mt-1 max-w-sm text-xs',
        warn ? 'text-[color-mix(in_oklch,var(--warning)_70%,var(--foreground))]' : 'text-muted-foreground',
      )}
      data-testid="compat-message"
    >
      {text}
    </div>
  )
}

// ---------------------------------------------------------------- workload clusters

const AGENT_STATUS: Record<string, 'success' | 'warning' | 'muted'> = {
  connected: 'success',
  stale: 'warning',
  never: 'muted',
}

/** The worst of several compat values: a mismatch wins, then unknown. */
function worst(cs: VersionCompat[]): VersionCompat {
  if (cs.includes('older')) return 'older'
  if (cs.includes('newer')) return 'newer'
  if (cs.includes('unknown')) return 'unknown'
  return 'same'
}

interface ClusterGroup {
  cluster: string
  agent?: ComponentVersion
  webhooks: ComponentVersion[]
}

function groupClusters(components: ComponentVersion[]): ClusterGroup[] {
  const by = new Map<string, ClusterGroup>()
  for (const c of components) {
    if (!c.cluster || (c.component !== 'agent' && c.component !== 'webhook')) continue
    const g = by.get(c.cluster) ?? { cluster: c.cluster, webhooks: [] }
    if (c.component === 'agent') g.agent = c
    else g.webhooks.push(c)
    by.set(c.cluster, g)
  }
  return [...by.values()].sort((a, b) => a.cluster.localeCompare(b.cluster))
}

function WebhookCell({ g }: { g: ClusterGroup }) {
  const reported = g.agent?.agentStatus !== 'never'
  if (!g.webhooks.length)
    return reported ? (
      <Tooltip content="The agent found no Kerf webhook in this cluster: new pods start with their template values until the agent resizes them.">
        <span className="text-muted-foreground" data-testid="webhook-missing">
          not installed
        </span>
      </Tooltip>
    ) : (
      <span className="text-muted-foreground">—</span>
    )
  const versions = [...new Set(g.webhooks.map((w) => w.version))]
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-1.5">
        {versions.map((v) => (
          <Version key={v} v={v} />
        ))}
        <Tooltip content={g.webhooks.map((w) => w.instance).join(', ')}>
          <span className="text-xs text-muted-foreground">
            ({g.webhooks.length} pod{g.webhooks.length === 1 ? '' : 's'})
          </span>
        </Tooltip>
      </div>
      <CompatBadge compat={worst(g.webhooks.map((w) => w.compat))} />
    </div>
  )
}

function ClustersTable({ m }: { m: Maintenance }) {
  const groups = groupClusters(m.components)
  const matching = groups.filter(
    (g) => g.agent?.compat === 'same' && g.webhooks.every((w) => w.compat === 'same'),
  ).length
  return (
    <Card data-testid="cluster-components">
      <CardHeader>
        <div>
          <CardTitle className="flex items-center gap-2">
            <BoxesIcon className="size-4 text-muted-foreground" /> Workload clusters
          </CardTitle>
          <CardDescription>
            Agent and webhook versions as each cluster's agent reports them.{' '}
            {groups.length ? `${matching} of ${groups.length} match the hub.` : null}
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent className="pt-4">
        {!groups.length ? (
          <EmptyState
            icon={BoxesIcon}
            title="No clusters registered"
            description="Add a cluster to see its agent and webhook versions here."
          />
        ) : (
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[720px] text-[13px]">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">Cluster</th>
                  <th className="px-3 py-2 text-left font-medium">Agent</th>
                  <th className="px-3 py-2 text-left font-medium">Webhook</th>
                  <th className="px-3 py-2 text-left font-medium">Notes</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => {
                  const compat = worst([
                    ...(g.agent ? [g.agent.compat] : []),
                    ...g.webhooks.map((w) => w.compat),
                  ])
                  const notes = [
                    ...new Set(
                      [g.agent, ...g.webhooks]
                        .filter((c): c is ComponentVersion => !!c?.message)
                        .map((c) => `${c.component === 'agent' ? 'Agent' : 'Webhook'}: ${c.message}`),
                    ),
                  ]
                  return (
                    <tr
                      key={g.cluster}
                      className={cn('border-b align-top last:border-0', mismatch(compat) && 'bg-warning/6')}
                      data-testid={`cluster-${g.cluster}`}
                      data-compat={compat}
                    >
                      <td className="px-4 py-2.5">
                        <Link
                          to={`/clusters/${encodeURIComponent(g.cluster)}`}
                          className="font-mono text-xs font-medium hover:underline"
                        >
                          {g.cluster}
                        </Link>
                        {g.agent?.lastSeenAt ? (
                          <div className="text-xs text-muted-foreground">
                            seen <Seen at={g.agent.lastSeenAt} />
                          </div>
                        ) : null}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="space-y-1">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <Version v={g.agent?.version} />
                            {g.agent?.agentStatus ? (
                              <Badge variant={AGENT_STATUS[g.agent.agentStatus] ?? 'muted'}>
                                {g.agent.agentStatus}
                              </Badge>
                            ) : null}
                          </div>
                          {g.agent?.version ? <CompatBadge compat={g.agent.compat} /> : null}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <WebhookCell g={g} />
                      </td>
                      <td className="px-3 py-2.5">
                        {notes.length ? (
                          notes.map((n) => <Note key={n} text={n} warn={mismatch(compat)} />)
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------- page

/** Admin → Maintenance (WO-33): component versions and the check for a newer Kerf release. */
export default function MaintenancePage() {
  const q = useMaintenance()
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Maintenance"
        description="The version of every Kerf component, and whether a newer release is out."
      />
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <div className="space-y-6" aria-busy="true">
          <Skeleton className="h-52 rounded-xl" />
          <Skeleton className="h-56 rounded-xl" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      ) : (
        <>
          <UpdatesCard m={q.data} />
          <HubTable m={q.data} />
          <ClustersTable m={q.data} />
        </>
      )}
    </div>
  )
}
