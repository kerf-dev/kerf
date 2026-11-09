import { useEffect, useId, useState } from 'react'
import { useWrite, writes } from '@/api/admin'
import type { Datasource, DatasourceAuthType, DatasourceInput, DatasourceType } from '@/api/types'
import { Checkbox, Field, NativeSelect, Textarea } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { isDuration } from '@/lib/validate'
import { WriteDialog } from './write-dialog'

export const DS_TYPE_LABEL: Record<DatasourceType, string> = {
  prometheus: 'Prometheus',
  victoriametrics: 'VictoriaMetrics',
  thanos: 'Thanos',
  mimir: 'Grafana Mimir',
  amp: 'Amazon Managed Prometheus',
  dynatrace: 'Dynatrace',
}
export const AUTH_LABEL: Record<DatasourceAuthType, string> = {
  none: 'None',
  bearer: 'Bearer token',
  basic: 'Basic auth',
  sigv4: 'AWS SigV4 (IRSA / Pod Identity)',
  mtls: 'Client certificate (mTLS)',
}

type TokenSource = 'secret' | 'paste'
/** A Kubernetes Secret name (DNS-1123 subdomain), as the hub accepts for auth.tokenSecret. */
const SECRET_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const validSecretName = (s: string) => s.length <= 253 && SECRET_NAME.test(s)

interface Form {
  name: string
  type: DatasourceType
  url: string
  auth: DatasourceAuthType
  tokenSource: TokenSource
  tokenSecret: string
  token: string
  username: string
  password: string
  region: string
  roleARN: string
  clientCert: string
  clientKey: string
  caCert: string
  insecure: boolean
  headers: string
  timeout: string
  maxPoints: string
  clearSecrets: boolean
  activeGateURL: string
}

/** Where a bearer token comes from: a Secret mounted into the hub, or pasted and stored encrypted. */
function tokenSourceOf(d?: Datasource): TokenSource {
  if (!d) return 'paste'
  if (d.auth.tokenSecret) return 'secret'
  return d.type === 'dynatrace' && !d.auth.secretConfigured ? 'secret' : 'paste'
}

function isHttpURL(v: string) {
  try {
    const u = new URL(v)
    return u.protocol === 'http:' || u.protocol === 'https:'
  } catch {
    return false
  }
}

function toForm(d?: Datasource): Form {
  return {
    name: d?.name ?? '',
    type: d?.type ?? 'prometheus',
    url: d?.url ?? '',
    auth: d?.auth.type ?? 'none',
    tokenSource: tokenSourceOf(d),
    tokenSecret: d?.auth.tokenSecret ?? '',
    token: '',
    username: d?.auth.username ?? '',
    password: '',
    region: d?.auth.sigv4?.region ?? '',
    roleARN: d?.auth.sigv4?.roleARN ?? '',
    clientCert: '',
    clientKey: '',
    caCert: '',
    insecure: d?.auth.insecureSkipVerify ?? false,
    headers: Object.entries(d?.headers ?? {})
      .map(([k, v]) => `${k}: ${v}`)
      .join('\n'),
    timeout: d?.timeout ?? '',
    maxPoints: d?.maxPoints ? String(d.maxPoints) : '',
    clearSecrets: false,
    activeGateURL: d?.dynatrace?.activeGateURL ?? '',
  }
}

function parseHeaders(s: string): Record<string, string> | string {
  const out: Record<string, string> = {}
  for (const line of s
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)) {
    const i = line.indexOf(':')
    if (i <= 0) return `Line "${line}" is not "Header: value".`
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

/** Create or edit a metrics datasource. Secrets are write-only: blank keeps the stored one. */
export function DatasourceDialog({
  open,
  onOpenChange,
  datasource,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  datasource?: Datasource
  onCreated?: (d: Datasource) => void
}) {
  const id = useId()
  const [f, setF] = useState<Form>(toForm(datasource))
  useEffect(() => {
    if (open) setF(toForm(datasource))
  }, [open, datasource])
  const set = (patch: Partial<Form>) => setF((x) => ({ ...x, ...patch }))
  const headers = parseHeaders(f.headers)
  const isDt = f.type === 'dynatrace'
  // A Dynatrace datasource is always read with a platform token.
  const auth: DatasourceAuthType = isDt ? 'bearer' : f.auth
  const secret = datasource?.auth.secretConfigured && datasource.auth.type === auth
  const errors: Record<string, string | undefined> = {
    name: !/^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/.test(f.name)
      ? 'Lowercase letters, digits and "-", up to 63 characters.'
      : undefined,
    url: (() => {
      try {
        const u = new URL(f.url)
        return u.protocol === 'http:' || u.protocol === 'https:' ? undefined : 'Use an http(s) URL.'
      } catch {
        return isDt
          ? 'A full URL, e.g. https://abc12345.apps.dynatrace.com'
          : 'A full URL, e.g. https://prometheus.monitoring:9090'
      }
    })(),
    tokenSecret:
      auth === 'bearer' && f.tokenSource === 'secret'
        ? !f.tokenSecret
          ? 'Required.'
          : !validSecretName(f.tokenSecret)
            ? 'A Secret name: lowercase letters, digits, "-" and ".", up to 253 characters.'
            : undefined
        : undefined,
    token: auth === 'bearer' && f.tokenSource === 'paste' && !f.token && !secret ? 'Required.' : undefined,
    activeGateURL: isDt && f.activeGateURL && !isHttpURL(f.activeGateURL) ? 'An http(s) URL.' : undefined,
    username: auth === 'basic' && !f.username ? 'Required.' : undefined,
    password: auth === 'basic' && !f.password && !secret ? 'Required.' : undefined,
    region: auth === 'sigv4' && !f.region ? 'Required.' : undefined,
    clientCert:
      auth === 'mtls' && !f.clientCert && !datasource?.auth.clientCertConfigured ? 'Required.' : undefined,
    headers: !isDt && typeof headers === 'string' ? headers : undefined,
    timeout: f.timeout && !isDuration(f.timeout) ? 'A duration like 30s.' : undefined,
    maxPoints: !isDt && f.maxPoints && !(Number(f.maxPoints) >= 100) ? 'At least 100.' : undefined,
  }
  const invalid = Object.values(errors).some(Boolean)
  const [touched, setTouched] = useState(false)
  useEffect(() => setTouched(false), [open])

  const body = (reason: string): DatasourceInput => ({
    name: f.name,
    type: f.type,
    url: f.url,
    auth: {
      type: auth,
      ...(auth === 'bearer'
        ? f.tokenSource === 'secret'
          ? { tokenSecret: f.tokenSecret }
          : f.token
            ? { token: f.token }
            : {}
        : {}),
      ...(auth === 'basic' ? { username: f.username, ...(f.password ? { password: f.password } : {}) } : {}),
      ...(auth === 'sigv4'
        ? { sigv4: { region: f.region, service: 'aps', ...(f.roleARN ? { roleARN: f.roleARN } : {}) } }
        : {}),
      ...(auth === 'mtls' || f.caCert || f.insecure
        ? {
            tls: {
              ...(f.caCert ? { caCert: f.caCert } : {}),
              ...(f.clientCert ? { clientCert: f.clientCert } : {}),
              ...(f.clientKey ? { clientKey: f.clientKey } : {}),
              ...(f.insecure ? { insecureSkipVerify: true } : {}),
            },
          }
        : {}),
    },
    ...(isDt && f.activeGateURL ? { dynatrace: { activeGateURL: f.activeGateURL } } : {}),
    ...(!isDt && typeof headers === 'object' && Object.keys(headers).length ? { headers } : {}),
    ...(f.timeout ? { timeout: f.timeout } : {}),
    ...(!isDt && f.maxPoints ? { maxPoints: Number(f.maxPoints) } : {}),
    ...(f.clearSecrets ? { clearSecrets: true } : {}),
    reason,
  })
  const write = useWrite(({ reason }: { reason: string }) =>
    datasource
      ? writes.updateDatasource({ id: datasource.id, version: datasource.version, body: body(reason) })
      : writes.createDatasource(body(reason)),
  )
  const err = (k: string) => (touched ? errors[k] : undefined)
  const secretHint = secret ? 'Stored. Leave blank to keep it.' : undefined
  return (
    <WriteDialog
      open={open}
      onOpenChange={onOpenChange}
      wide
      testId="datasource-dialog"
      title={datasource ? `Edit ${datasource.name}` : 'New metrics datasource'}
      description={
        isDt
          ? "A Dynatrace environment that one or more clusters' metrics are read from."
          : "A Prometheus-compatible query endpoint that one or more clusters' metrics come from."
      }
      confirmLabel={datasource ? 'Save datasource' : 'Create datasource'}
      onConfirm={async (o) => {
        setTouched(true)
        if (invalid) throw new Error('Fix the highlighted fields first.')
        const d = (await write.mutateAsync(o)) as Datasource
        onCreated?.(d)
        return d
      }}
      successMessage={datasource ? 'Datasource saved' : 'Datasource created'}
    >
      <div className="grid gap-3 sm:grid-cols-2" onBlur={() => setTouched(true)}>
        <Field label="Name" htmlFor={`${id}-n`} error={err('name')}>
          <Input
            id={`${id}-n`}
            value={f.name}
            onChange={(e) => set({ name: e.target.value })}
            className="font-mono"
            disabled={!!datasource?.builtIn}
            placeholder="amp-eu1"
          />
        </Field>
        <Field label="Type" htmlFor={`${id}-t`}>
          <NativeSelect
            id={`${id}-t`}
            value={f.type}
            onChange={(e) =>
              set({
                type: e.target.value as DatasourceType,
                ...(e.target.value === 'amp' ? { auth: 'sigv4' } : {}),
                ...(e.target.value === 'dynatrace'
                  ? { auth: 'bearer', tokenSource: datasource?.auth.secretConfigured ? 'paste' : 'secret' }
                  : {}),
              })
            }
          >
            {Object.entries(DS_TYPE_LABEL).map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <Field
          label={isDt ? 'Environment URL' : 'Query URL'}
          htmlFor={`${id}-u`}
          className="sm:col-span-2"
          hint={
            isDt
              ? 'https://<environment-id>.apps.dynatrace.com, the environment the platform token belongs to.'
              : '/api/v1/query is appended.'
          }
          error={err('url')}
        >
          <Input
            id={`${id}-u`}
            value={f.url}
            onChange={(e) => set({ url: e.target.value })}
            className="font-mono text-[13px]"
            placeholder={
              isDt
                ? 'https://abc12345.apps.dynatrace.com'
                : f.type === 'amp'
                  ? 'https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-…/'
                  : 'http://prometheus.monitoring:9090'
            }
          />
        </Field>
        {isDt ? (
          <Field
            label="Authentication"
            className="sm:col-span-2"
            hint="Needs the scopes storage:metrics:read, storage:smartscape:read and storage:buckets:read."
          >
            <div className="flex h-8 items-center text-sm">Platform token</div>
          </Field>
        ) : (
          <Field label="Authentication" htmlFor={`${id}-a`} className="sm:col-span-2">
            <NativeSelect
              id={`${id}-a`}
              value={f.auth}
              onChange={(e) => set({ auth: e.target.value as DatasourceAuthType })}
            >
              {Object.entries(AUTH_LABEL).map(([v, l]) => (
                <option key={v} value={v}>
                  {l}
                </option>
              ))}
            </NativeSelect>
          </Field>
        )}
        {auth === 'bearer' ? (
          <>
            <Field label="Token source" htmlFor={`${id}-ts`} className="sm:col-span-2">
              <NativeSelect
                id={`${id}-ts`}
                value={f.tokenSource}
                onChange={(e) => set({ tokenSource: e.target.value as TokenSource })}
              >
                <option value="secret">Kubernetes Secret (recommended)</option>
                <option value="paste">Paste token</option>
              </NativeSelect>
            </Field>
            {f.tokenSource === 'secret' ? (
              <Field
                label="Secret name"
                htmlFor={`${id}-sn`}
                className="sm:col-span-2"
                hint="A Secret mounted into the hub with the hub chart's onboarding.datasourceSecrets; the token is its key token."
                error={err('tokenSecret')}
              >
                <Input
                  id={`${id}-sn`}
                  value={f.tokenSecret}
                  onChange={(e) => set({ tokenSecret: e.target.value.trim() })}
                  className="font-mono"
                  placeholder="dynatrace"
                />
              </Field>
            ) : (
              <Field
                label={isDt ? 'Platform token' : 'Token'}
                htmlFor={`${id}-tk`}
                className="sm:col-span-2"
                hint={secretHint ?? 'Stored encrypted; never shown again.'}
                error={err('token')}
              >
                <Input
                  id={`${id}-tk`}
                  type="password"
                  autoComplete="off"
                  value={f.token}
                  onChange={(e) => set({ token: e.target.value })}
                  placeholder={secret ? '••••••••' : ''}
                />
              </Field>
            )}
          </>
        ) : null}
        {isDt ? (
          <Field
            label="ActiveGate URL"
            htmlFor={`${id}-ag`}
            optional
            className="sm:col-span-2"
            hint="Queries go here instead of to the environment URL, for a hub that reaches Dynatrace only through an ActiveGate or proxy."
            error={err('activeGateURL')}
          >
            <Input
              id={`${id}-ag`}
              value={f.activeGateURL}
              onChange={(e) => set({ activeGateURL: e.target.value })}
              className="font-mono text-[13px]"
              placeholder="https://activegate.example.internal:9999"
            />
          </Field>
        ) : null}
        {auth === 'basic' ? (
          <>
            <Field label="Username" error={err('username')}>
              <Input
                value={f.username}
                onChange={(e) => set({ username: e.target.value })}
                autoComplete="off"
              />
            </Field>
            <Field label="Password" hint={secretHint} error={err('password')}>
              <Input
                type="password"
                autoComplete="off"
                value={f.password}
                onChange={(e) => set({ password: e.target.value })}
                placeholder={secret ? '••••••••' : ''}
              />
            </Field>
          </>
        ) : null}
        {auth === 'sigv4' ? (
          <>
            <Field label="AWS region" error={err('region')}>
              <Input
                value={f.region}
                onChange={(e) => set({ region: e.target.value })}
                placeholder="eu-west-1"
              />
            </Field>
            <Field label="Role ARN" optional hint="Absent: the hub's own identity.">
              <Input
                value={f.roleARN}
                onChange={(e) => set({ roleARN: e.target.value })}
                className="font-mono text-[13px]"
                placeholder="arn:aws:iam::123456789012:role/ro-hub"
              />
            </Field>
          </>
        ) : null}
        {auth === 'mtls' ? (
          <>
            <Field
              label="Client certificate (PEM)"
              error={err('clientCert')}
              hint={datasource?.auth.clientCertConfigured ? 'Stored. Leave blank to keep it.' : undefined}
            >
              <Textarea
                rows={3}
                value={f.clientCert}
                onChange={(e) => set({ clientCert: e.target.value })}
                className="font-mono text-xs"
              />
            </Field>
            <Field label="Client key (PEM)" hint={secretHint ?? 'Write-only.'}>
              <Textarea
                rows={3}
                value={f.clientKey}
                onChange={(e) => set({ clientKey: e.target.value })}
                className="font-mono text-xs"
              />
            </Field>
          </>
        ) : null}
        {isDt ? null : (
          <Field
            label="Extra headers"
            optional
            className="sm:col-span-2"
            hint="One per line, e.g. X-Scope-OrgID: tenant-a (Mimir/Cortex). Not secret."
            error={err('headers')}
          >
            <Textarea
              rows={2}
              value={f.headers}
              onChange={(e) => set({ headers: e.target.value })}
              className="font-mono text-xs"
            />
          </Field>
        )}
        <Field label="Timeout" optional error={err('timeout')}>
          <Input
            value={f.timeout}
            onChange={(e) => set({ timeout: e.target.value })}
            placeholder="30s"
            className="font-mono"
          />
        </Field>
        {isDt ? null : (
          <Field
            label="Max points per query"
            optional
            error={err('maxPoints')}
            hint="The recommender chunks queries to fit."
          >
            <Input
              inputMode="numeric"
              value={f.maxPoints}
              onChange={(e) => set({ maxPoints: e.target.value })}
              placeholder="11000"
              className="font-mono"
            />
          </Field>
        )}
        <label className="flex items-center gap-2 text-[13px] sm:col-span-2">
          <Checkbox checked={f.insecure} onCheckedChange={(v) => set({ insecure: v === true })} /> Skip TLS
          verification (testing only)
        </label>
        {datasource?.auth.secretConfigured ? (
          <label className="flex items-center gap-2 text-[13px] sm:col-span-2">
            <Checkbox checked={f.clearSecrets} onCheckedChange={(v) => set({ clearSecrets: v === true })} />{' '}
            Remove the stored secrets
          </label>
        ) : null}
      </div>
    </WriteDialog>
  )
}
