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
}
export const AUTH_LABEL: Record<DatasourceAuthType, string> = {
  none: 'None',
  bearer: 'Bearer token',
  basic: 'Basic auth',
  sigv4: 'AWS SigV4 (IRSA / Pod Identity)',
  mtls: 'Client certificate (mTLS)',
}

interface Form {
  name: string
  type: DatasourceType
  url: string
  auth: DatasourceAuthType
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
}

function toForm(d?: Datasource): Form {
  return {
    name: d?.name ?? '',
    type: d?.type ?? 'prometheus',
    url: d?.url ?? '',
    auth: d?.auth.type ?? 'none',
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
  const secret = datasource?.auth.secretConfigured && datasource.auth.type === f.auth
  const errors: Record<string, string | undefined> = {
    name: !/^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/.test(f.name)
      ? 'Lowercase letters, digits and "-", up to 63 characters.'
      : undefined,
    url: (() => {
      try {
        const u = new URL(f.url)
        return u.protocol === 'http:' || u.protocol === 'https:' ? undefined : 'Use an http(s) URL.'
      } catch {
        return 'A full URL, e.g. https://prometheus.monitoring:9090'
      }
    })(),
    token: f.auth === 'bearer' && !f.token && !secret ? 'Required.' : undefined,
    username: f.auth === 'basic' && !f.username ? 'Required.' : undefined,
    password: f.auth === 'basic' && !f.password && !secret ? 'Required.' : undefined,
    region: f.auth === 'sigv4' && !f.region ? 'Required.' : undefined,
    clientCert:
      f.auth === 'mtls' && !f.clientCert && !datasource?.auth.clientCertConfigured ? 'Required.' : undefined,
    headers: typeof headers === 'string' ? headers : undefined,
    timeout: f.timeout && !isDuration(f.timeout) ? 'A duration like 30s.' : undefined,
    maxPoints: f.maxPoints && !(Number(f.maxPoints) >= 100) ? 'At least 100.' : undefined,
  }
  const invalid = Object.values(errors).some(Boolean)
  const [touched, setTouched] = useState(false)
  useEffect(() => setTouched(false), [open])

  const body = (reason: string): DatasourceInput => ({
    name: f.name,
    type: f.type,
    url: f.url,
    auth: {
      type: f.auth,
      ...(f.auth === 'bearer' && f.token ? { token: f.token } : {}),
      ...(f.auth === 'basic'
        ? { username: f.username, ...(f.password ? { password: f.password } : {}) }
        : {}),
      ...(f.auth === 'sigv4'
        ? { sigv4: { region: f.region, service: 'aps', ...(f.roleARN ? { roleARN: f.roleARN } : {}) } }
        : {}),
      ...(f.auth === 'mtls' || f.caCert || f.insecure
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
    ...(typeof headers === 'object' && Object.keys(headers).length ? { headers } : {}),
    ...(f.timeout ? { timeout: f.timeout } : {}),
    ...(f.maxPoints ? { maxPoints: Number(f.maxPoints) } : {}),
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
      description="A Prometheus-compatible query endpoint that one or more clusters' metrics come from."
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
          label="Query URL"
          htmlFor={`${id}-u`}
          className="sm:col-span-2"
          hint="/api/v1/query is appended."
          error={err('url')}
        >
          <Input
            id={`${id}-u`}
            value={f.url}
            onChange={(e) => set({ url: e.target.value })}
            className="font-mono text-[13px]"
            placeholder={
              f.type === 'amp'
                ? 'https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-…/'
                : 'http://prometheus.monitoring:9090'
            }
          />
        </Field>
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
        {f.auth === 'bearer' ? (
          <Field
            label="Token"
            className="sm:col-span-2"
            hint={secretHint ?? 'Stored encrypted; never shown again.'}
            error={err('token')}
          >
            <Input
              type="password"
              autoComplete="off"
              value={f.token}
              onChange={(e) => set({ token: e.target.value })}
              placeholder={secret ? '••••••••' : ''}
            />
          </Field>
        ) : null}
        {f.auth === 'basic' ? (
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
        {f.auth === 'sigv4' ? (
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
        {f.auth === 'mtls' ? (
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
        <Field label="Timeout" optional error={err('timeout')}>
          <Input
            value={f.timeout}
            onChange={(e) => set({ timeout: e.target.value })}
            placeholder="30s"
            className="font-mono"
          />
        </Field>
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
