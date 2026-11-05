import { KeyRoundIcon, Loader2Icon, SettingsIcon, PlusIcon, RotateCwIcon, Trash2Icon } from 'lucide-react'
import { useState } from 'react'
import { useInstallSnippet, useTokens, useWrite, writes } from '@/api/admin'
import type { InstallFormat, TokenCreated, TokenInfo } from '@/api/types'
import { ApiError } from '@/api/client'
import { ErrorState } from '@/components/states'
import { Callout } from '@/components/ui/form'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, relativeTime } from '@/lib/format'
import { CodeBlock, SecretOnce } from './common'
import { SwitchRow } from './policy-form'
import { WriteDialog } from './write-dialog'

/**
 * Install instructions in three flavours (D-025): the umbrella chart resource-optimizer in mode spoke
 * (agent, webhook and CRDs; WO-18f). They reference the token Secret, never the token.
 */
export function InstallSnippets({ cluster, compact }: { cluster: string; compact?: boolean }) {
  const [format, setFormat] = useState<InstallFormat>('helm')
  // Part of mode spoke, as docs/install/eks.md installs it; off sets webhook.enabled false.
  const [webhook, setWebhook] = useState(true)
  const [certManager, setCertManager] = useState(true)
  const q = useInstallSnippet(cluster, format, { webhook, ...(webhook ? { certManager } : {}) })
  return (
    <div className="flex flex-col gap-4" data-testid="install-snippets">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs value={format} onValueChange={(v) => setFormat(v as InstallFormat)}>
          <TabsList>
            <TabsTrigger value="helm">Helm</TabsTrigger>
            <TabsTrigger value="values">values.yaml</TabsTrigger>
            <TabsTrigger value="argocd">Argo CD</TabsTrigger>
          </TabsList>
        </Tabs>
        <div className="flex flex-wrap gap-x-5 gap-y-2">
          <div className="w-56">
            <SwitchRow
              label="Admission webhook"
              description="Applies requests at pod creation"
              checked={webhook}
              onChange={setWebhook}
            />
          </div>
          {webhook ? (
            <div className="w-56">
              <SwitchRow
                label="Use cert-manager"
                description="Otherwise a self-signed cert"
                checked={certManager}
                onChange={setCertManager}
              />
            </div>
          ) : null}
        </div>
      </div>
      {q.isError ? (
        q.error instanceof ApiError && q.error.code === 'install_not_configured' ? (
          <InstallNotConfigured detail={q.error.problem?.detail} />
        ) : (
          <ErrorState error={q.error} compact onRetry={() => q.refetch()} />
        )
      ) : !q.data ? (
        <Skeleton className="h-48 rounded-lg" />
      ) : (
        <>
          <div>
            <div className="mb-1.5 flex items-center justify-between text-xs text-muted-foreground">
              <span className="font-mono">{q.data.filename}</span>
            </div>
            <CodeBlock code={q.data.content} maxHeight={compact ? 260 : 380} testId="install-content" />
          </div>
          {q.data.notes?.length ? (
            <ul className="list-disc space-y-1 pl-5 text-[13px] text-muted-foreground">
              {q.data.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          ) : null}
          <div>
            <div className="mb-1 text-[13px] font-medium">Argo CD: ignore fields the optimizer owns</div>
            <p className="mb-2 text-xs text-muted-foreground">
              Add to <code className="font-mono">argocd-cm</code> so Argo CD does not revert the resized
              requests (D-006).
            </p>
            <CodeBlock code={q.data.argocdIgnoreDifferences} maxHeight={160} />
          </div>
        </>
      )}
    </div>
  )
}

const HUB_VALUES: { key: string; what: string; match: RegExp; example: string }[] = [
  {
    key: 'hub.config.onboarding.hubURL',
    what: 'the hub URL as agents reach it',
    match: /hub URL/i,
    example: 'https://hub.example.com',
  },
  {
    key: 'hub.config.onboarding.chart',
    what: 'the resource-optimizer chart (oci://…, <repo URL>|<chart>, or a path)',
    match: /chart reference/i,
    example: 'oci://registry.example.com/resource-optimizer/charts/resource-optimizer',
  },
  {
    key: 'hub.config.onboarding.imageRegistry',
    what: 'the image registry host (only when the chart is not oci://)',
    match: /image registry/i,
    example: 'registry.example.com',
  },
  {
    key: 'hub.config.onboarding.chartVersion',
    what: 'optional: pin the chart version',
    match: /$^/,
    example: '',
  },
]

/**
 * `409 install_not_configured` (WO-25): the hub cannot render install snippets until its chart sets
 * the onboarding values. Names the missing ones (from the hub's message) and shows how to set them.
 */
function InstallNotConfigured({ detail }: { detail?: string }) {
  const missing = HUB_VALUES.filter((v) => detail && v.match.test(detail))
  const list = missing.length ? missing : HUB_VALUES.slice(0, 2)
  const cmd = `helm upgrade resource-optimizer deploy/charts/resource-optimizer -n resource-optimizer --reuse-values \\\n${list
    .filter((v) => v.example)
    .map((v) => `  --set ${v.key}=${v.example}`)
    .join(' \\\n')}`
  return (
    <div className="flex flex-col gap-3" data-testid="install-not-configured">
      <Callout tone="warning" icon={SettingsIcon} title="Install snippets are not configured on the hub">
        The hub needs these chart values before it can render install instructions:
        <ul className="mt-1.5 space-y-0.5">
          {list.map((v) => (
            <li key={v.key}>
              <code className="font-mono text-[12px] text-foreground">{v.key}</code> — {v.what}
            </li>
          ))}
        </ul>
        {detail ? <p className="mt-1.5 text-xs">Hub: {detail}</p> : null}
      </Callout>
      <CodeBlock code={cmd} maxHeight={160} />
    </div>
  )
}

/** Agent tokens of one cluster: create, rotate (create + revoke the old one) and revoke. */
export function AgentTokens({ cluster, prod }: { cluster: string; prod?: boolean }) {
  const tokens = useTokens({ cluster, includeRevoked: true })
  const [created, setCreated] = useState<TokenCreated>()
  const [dialog, setDialog] = useState<{ kind: 'create' | 'rotate' | 'revoke'; token?: TokenInfo } | null>(
    null,
  )
  const create = useWrite(async ({ reason }: { reason: string }) => {
    const res = await writes.createToken({ role: 'agent', cluster, description: `agent ${cluster}`, reason })
    if (dialog?.kind === 'rotate' && dialog.token)
      await writes.revokeToken({ id: dialog.token.id, reason: `rotated: ${reason}` })
    return res
  })
  const revoke = useWrite(({ reason }: { reason: string }) =>
    writes.revokeToken({ id: dialog!.token!.id, reason }),
  )
  const list = (tokens.data?.items ?? []).filter((t) => t.role === 'agent')
  const active = list.filter((t) => !t.revokedAt)
  return (
    <div className="flex flex-col gap-3" data-testid="agent-tokens">
      <div className="flex items-center justify-between gap-2">
        <p className="text-[13px] text-muted-foreground">
          The agent authenticates with this token. Values are shown once and stored hashed.
        </p>
        <Button
          size="sm"
          variant="outline"
          onClick={() => setDialog({ kind: 'create' })}
          data-testid="create-agent-token"
        >
          <PlusIcon /> New token
        </Button>
      </div>
      {tokens.isError ? (
        <ErrorState error={tokens.error} compact />
      ) : !tokens.data ? (
        <Skeleton className="h-16" />
      ) : !list.length ? (
        <p className="rounded-lg border border-dashed px-3 py-5 text-center text-[13px] text-muted-foreground">
          No agent token yet. Create one, then install the agent with it.
        </p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {list.map((t) => (
            <li
              key={t.id}
              className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-[13px]"
            >
              <div className="flex min-w-0 items-center gap-2.5">
                <KeyRoundIcon className="size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-xs">#{t.id}</span>
                    <span className="truncate">{t.description}</span>
                    {t.revokedAt ? (
                      <Badge variant="muted">revoked</Badge>
                    ) : (
                      <Badge variant="success">active</Badge>
                    )}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    Created {formatDateTime(t.createdAt, true)}
                    {t.createdBy ? ` by ${t.createdBy}` : ''} ·{' '}
                    {t.revokedAt
                      ? `revoked ${relativeTime(t.revokedAt)}`
                      : t.lastUsedAt
                        ? `last used ${relativeTime(t.lastUsedAt)}`
                        : 'never used'}
                  </div>
                </div>
              </div>
              {!t.revokedAt ? (
                <div className="flex gap-1.5">
                  <Tooltip content="Create a new token and revoke this one. Update the Secret on the cluster right after.">
                    <Button
                      size="xs"
                      variant="outline"
                      onClick={() => setDialog({ kind: 'rotate', token: t })}
                    >
                      <RotateCwIcon /> Rotate
                    </Button>
                  </Tooltip>
                  <Button
                    size="xs"
                    variant="ghost"
                    className="text-destructive hover:text-destructive"
                    onClick={() => setDialog({ kind: 'revoke', token: t })}
                  >
                    <Trash2Icon /> Revoke
                  </Button>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      <WriteDialog
        open={dialog?.kind === 'create' || dialog?.kind === 'rotate'}
        onOpenChange={(o) => !o && setDialog(null)}
        title={
          dialog?.kind === 'rotate'
            ? `Rotate agent token #${dialog.token?.id}`
            : `New agent token for ${cluster}`
        }
        description={
          dialog?.kind === 'rotate'
            ? 'Creates a new token and revokes the old one immediately. The agent fails to authenticate until its Secret holds the new value.'
            : active.length
              ? `This cluster already has ${active.length} active token(s); they stay valid.`
              : 'The value is shown once after you confirm.'
        }
        prodClusters={prod && dialog?.kind === 'rotate' ? [cluster] : []}
        confirmLabel={dialog?.kind === 'rotate' ? 'Rotate token' : 'Create token'}
        onConfirm={async (o) => setCreated((await create.mutateAsync(o)) as TokenCreated)}
      />
      <WriteDialog
        open={dialog?.kind === 'revoke'}
        onOpenChange={(o) => !o && setDialog(null)}
        tone="destructive"
        title={`Revoke agent token #${dialog?.token?.id}`}
        description={
          active.length <= 1
            ? 'This is the only active token: the agent stops reaching the hub until a new token is installed.'
            : 'The agent stops working if it still uses this token.'
        }
        prodClusters={prod ? [cluster] : []}
        confirmLabel="Revoke token"
        onConfirm={(o) => revoke.mutateAsync(o)}
        successMessage="Token revoked"
      />
      <TokenCreatedDialog created={created} onClose={() => setCreated(undefined)} cluster={cluster} />
    </div>
  )
}

export function TokenCreatedDialog({
  created,
  onClose,
  cluster,
}: {
  created?: TokenCreated
  onClose: () => void
  cluster?: string
}) {
  const secretCmd =
    created && cluster
      ? `kubectl -n resource-optimizer create secret generic resource-optimizer-agent-token \\\n  --from-literal=token='${created.value}' --dry-run=client -o yaml | kubectl apply -f -`
      : undefined
  return (
    <Dialog open={!!created} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="top-[12%] w-[min(620px,calc(100vw-2rem))] p-5" data-testid="token-created">
        <DialogTitle className="text-base font-semibold">Token created</DialogTitle>
        <DialogDescription className="mt-1 mb-4 text-[13px] text-muted-foreground">
          {created?.token.role} token #{created?.token.id}
          {created?.token.cluster ? ` for ${created.token.cluster}` : ''}
          {created?.token.expiresAt ? ` · expires ${formatDateTime(created.token.expiresAt, true)}` : ''}
        </DialogDescription>
        {created ? <SecretOnce value={created.value} /> : null}
        {secretCmd ? (
          <div className="mt-4">
            <div className="mb-1.5 text-[13px] font-medium">Store it in the cluster</div>
            <CodeBlock code={secretCmd} maxHeight={120} />
          </div>
        ) : null}
        <div className="mt-5 flex justify-end">
          <Button size="sm" onClick={onClose}>
            I have stored it
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

export function Pending({ label }: { label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <Loader2Icon className="size-3 animate-spin" /> {label}
    </span>
  )
}
