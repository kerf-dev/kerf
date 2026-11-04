import {
  ArrowLeftIcon,
  ArrowRightIcon,
  CheckIcon,
  FlaskConicalIcon,
  Loader2Icon,
  PlusIcon,
  ServerIcon,
  XIcon,
} from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useDatasources, useReadiness, useWrite, writes } from '@/api/admin'
import { useClusters } from '@/api/queries'
import type {
  ClusterCreated,
  ClusterPolicySource,
  DatasourceTestResult,
  Environment,
  MetricsBinding,
} from '@/api/types'
import { POLICY_SOURCE_LABEL, SecretOnce } from '@/components/admin/common'
import { DatasourceDialog, DS_TYPE_LABEL } from '@/components/admin/datasource-dialog'
import { InstallSnippets } from '@/components/admin/install'
import {
  isProblem,
  ReadinessChecklist,
  ReadinessStepper,
  STATE_INFO,
  stateRank,
} from '@/components/admin/readiness'
import { WriteDialog, WriteError } from '@/components/admin/write-dialog'
import { PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Callout, Field, NativeSelect, Textarea } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

const STEPS = ['Identify', 'Metrics', 'Install the agent', 'Verify'] as const
const NAME = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
const MATCHER = /^[a-zA-Z_][a-zA-Z0-9_]*(=|!=|=~|!~)"([^"\\]|\\.)*"$/
const LABEL = /^([a-zA-Z_][a-zA-Z0-9_]*)?$/

function Steps({ step }: { step: number }) {
  return (
    <ol className="grid grid-cols-4 gap-2" aria-label="Steps">
      {STEPS.map((s, i) => (
        <li key={s} className="min-w-0" aria-current={i === step ? 'step' : undefined}>
          <div className={cn('h-1 rounded-full', i <= step ? 'bg-primary' : 'bg-muted')} />
          <div
            className={cn(
              'mt-2 flex items-center gap-1.5 text-xs font-medium',
              i === step ? 'text-foreground' : 'text-muted-foreground',
            )}
          >
            <span
              className={cn(
                'flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] tabular',
                i < step
                  ? 'bg-primary text-primary-foreground'
                  : i === step
                    ? 'bg-primary/15 text-primary ring-1 ring-primary/40'
                    : 'bg-muted',
              )}
            >
              {i < step ? <CheckIcon className="size-3" /> : i + 1}
            </span>
            <span className="truncate">{s}</span>
          </div>
        </li>
      ))}
    </ol>
  )
}

function TestResult({ r, onPick }: { r: DatasourceTestResult; onPick: (v: string) => void }) {
  const problems = r.checks.filter(isProblem)
  return (
    <div className="flex flex-col gap-3" data-testid="datasource-test-result">
      <Callout
        tone={r.ok ? 'success' : 'destructive'}
        icon={r.ok ? CheckIcon : XIcon}
        title={r.ok ? 'The datasource has what recommendations need' : 'Blocking problems found'}
      >
        {r.found
          ? `Found ${formatNumber(r.found.containerSeries)} cAdvisor container series, kube-state-metrics for ${formatNumber(r.found.pods)} pods and ${formatNumber(r.found.nodes)} nodes`
          : 'No series found for these matchers'}
        {r.latencyMs != null ? ` · ${r.latencyMs} ms` : ''}
        {problems.length ? `; ${problems.length} check(s) need attention.` : '.'}
      </Callout>
      {r.labelValues?.length ? (
        <div className="text-[13px]">
          <span className="text-muted-foreground">Values of the cluster label in this datasource: </span>
          <span className="inline-flex flex-wrap gap-1">
            {r.labelValues.map((v) => (
              <button
                key={v}
                type="button"
                onClick={() => onPick(v)}
                className="rounded bg-secondary px-1.5 py-0.5 font-mono text-xs hover:bg-accent"
              >
                {v}
              </button>
            ))}
          </span>
        </div>
      ) : null}
      {r.checks.length ? <ReadinessChecklist checks={r.checks} onlyProblems={problems.length > 0} /> : null}
    </div>
  )
}

function VerifyStep({ cluster }: { cluster: string }) {
  const q = useReadiness(cluster, { poll: true })
  const r = q.data
  if (!r)
    return (
      <div className="flex items-center gap-2 py-10 text-[13px] text-muted-foreground">
        <Loader2Icon className="size-4 animate-spin" /> Loading the checklist…
      </div>
    )
  const connected = stateRank(r.state) >= stateRank('AgentConnected')
  return (
    <div className="flex flex-col gap-5" data-testid="verify-step">
      <div className="flex items-center gap-3">
        {connected ? (
          <span className="flex size-9 items-center justify-center rounded-full bg-success/12 text-success">
            <CheckIcon className="size-5" />
          </span>
        ) : (
          <span className="relative flex size-9 items-center justify-center rounded-full bg-muted">
            <span className="absolute inset-0 animate-ping rounded-full bg-primary/15" />
            <Loader2Icon className="size-4 animate-spin text-muted-foreground" />
          </span>
        )}
        <div>
          <div className="font-semibold">
            {connected ? STATE_INFO[r.state].label : 'Waiting for the agent'}
          </div>
          <div className="text-[13px] text-muted-foreground">
            {connected
              ? STATE_INFO[r.state].desc
              : 'Install the agent with the commands from the previous step. This page updates as soon as it pulls its config.'}
          </div>
        </div>
      </div>
      <ReadinessStepper state={r.state} nextState={r.nextState} blockedBy={r.blockedBy} />
      <ReadinessChecklist checks={r.checks} capabilities={r.capabilities} blockedBy={r.blockedBy} />
    </div>
  )
}

/** Admin → Clusters → Add cluster (plan cluster-onboarding.md §3). */
export default function ClusterWizardPage() {
  const navigate = useNavigate()
  const clusters = useClusters()
  const datasources = useDatasources()
  const [step, setStep] = useState(0)
  const [touched, setTouched] = useState(false)
  const [id, setId] = useState({
    name: '',
    environment: 'non-prod' as Environment,
    owner: '',
    provider: 'aws',
    region: '',
    description: '',
    policySource: 'both' as ClusterPolicySource,
  })
  const [binding, setBinding] = useState<MetricsBinding>({
    datasourceId: 'local',
    clusterLabel: 'cluster',
    clusterLabelValue: '',
    extraMatchers: [],
  })
  const [matcher, setMatcher] = useState('')
  const [test, setTest] = useState<DatasourceTestResult>()
  const [dsOpen, setDsOpen] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const [created, setCreated] = useState<ClusterCreated>()
  const testing = useWrite(() =>
    writes.testDatasource({
      id: binding.datasourceId,
      binding: { ...binding, clusterLabelValue: binding.clusterLabelValue || id.name },
    }),
  )
  const create = useWrite(({ reason }: { reason: string }) =>
    writes.createCluster({
      ...id,
      owner: id.owner || undefined,
      provider: id.provider || undefined,
      region: id.region || undefined,
      description: id.description || undefined,
      metrics: { ...binding, clusterLabelValue: binding.clusterLabelValue || id.name },
      createAgentToken: true,
      reason,
    }),
  )
  const taken = clusters.data?.items.some((c) => c.name === id.name)
  const nameErr = !id.name
    ? 'Required.'
    : !NAME.test(id.name)
      ? 'Lowercase letters, digits and "-", up to 63 characters.'
      : taken
        ? 'A cluster with this name exists.'
        : undefined
  const labelErr = !LABEL.test(binding.clusterLabel)
    ? 'A Prometheus label name, or empty for no cluster matcher.'
    : undefined
  const matcherErr =
    matcher && !MATCHER.test(matcher) ? 'Like env="prod" or namespace!~"kube-.*".' : undefined
  const ds = datasources.data?.items.find((d) => d.id === binding.datasourceId)

  const next = () => {
    setTouched(true)
    if (step === 0 && nameErr) return
    if (step === 1 && labelErr) return
    setTouched(false)
    setStep((s) => s + 1)
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <Link
        to="/clusters"
        className="inline-flex items-center gap-1 text-[13px] text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-3.5" /> Clusters
      </Link>
      <PageHeader
        eyebrow="Administration"
        title="Add a cluster"
        description="Register it, check its metrics before any agent exists, install the agent and watch it become ready."
      />
      <Steps step={step} />
      <Card>
        <CardContent className="py-6">
          {step === 0 ? (
            <div className="grid gap-4 sm:grid-cols-2" data-testid="wizard-identify">
              <Field
                label="Cluster name"
                htmlFor="wz-name"
                hint="How the hub and the UI name it; it cannot change later."
                error={touched ? nameErr : undefined}
              >
                <Input
                  id="wz-name"
                  value={id.name}
                  onChange={(e) => setId({ ...id, name: e.target.value.trim() })}
                  className="font-mono"
                  placeholder="eks-prod-eu1"
                  autoFocus
                />
              </Field>
              <Field label="Environment" hint="prod clusters ask for confirmation on every write.">
                <Segmented
                  label="Environment"
                  value={id.environment}
                  onChange={(v) => setId({ ...id, environment: v })}
                  className="w-full [&>*]:flex-1"
                  options={[
                    { value: 'non-prod', label: 'non-prod' },
                    { value: 'prod', label: 'prod' },
                  ]}
                />
              </Field>
              <Field label="Owner team" optional>
                <Input
                  value={id.owner}
                  onChange={(e) => setId({ ...id, owner: e.target.value })}
                  placeholder="platform"
                />
              </Field>
              <Field label="Provider · region" optional hint="Informational.">
                <div className="flex gap-2">
                  <Input
                    value={id.provider}
                    onChange={(e) => setId({ ...id, provider: e.target.value })}
                    placeholder="aws"
                  />
                  <Input
                    value={id.region}
                    onChange={(e) => setId({ ...id, region: e.target.value })}
                    placeholder="eu-west-1"
                  />
                </div>
              </Field>
              <Field
                label="Policy source"
                className="sm:col-span-2"
                hint="Git-only clusters get no hub policies; the admin pages then offer YAML export instead."
              >
                <Segmented
                  label="Policy source"
                  value={id.policySource}
                  onChange={(v) => setId({ ...id, policySource: v })}
                  options={(['both', 'hub', 'git'] as ClusterPolicySource[]).map((v) => ({
                    value: v,
                    label: POLICY_SOURCE_LABEL[v],
                  }))}
                />
              </Field>
              <Field label="Description" optional className="sm:col-span-2">
                <Textarea
                  rows={2}
                  value={id.description}
                  onChange={(e) => setId({ ...id, description: e.target.value })}
                  placeholder="EU production · m6i on-demand"
                />
              </Field>
            </div>
          ) : step === 1 ? (
            <div className="flex flex-col gap-5" data-testid="wizard-metrics">
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Datasource" hint={ds ? `${DS_TYPE_LABEL[ds.type]} · ${ds.url}` : undefined}>
                  <div className="flex gap-2">
                    <NativeSelect
                      className="flex-1"
                      value={binding.datasourceId}
                      onChange={(e) => (
                        setBinding({ ...binding, datasourceId: e.target.value }),
                        setTest(undefined)
                      )}
                      aria-label="Datasource"
                    >
                      {(datasources.data?.items ?? []).map((d) => (
                        <option key={d.id} value={d.id}>
                          {d.name}
                          {d.builtIn ? ' (built-in)' : ''}
                        </option>
                      ))}
                    </NativeSelect>
                    <Button type="button" variant="outline" size="sm" onClick={() => setDsOpen(true)}>
                      <PlusIcon /> New
                    </Button>
                  </div>
                </Field>
                <div className="grid grid-cols-2 gap-2">
                  <Field label="Cluster label" error={labelErr} hint="Empty = no matcher.">
                    <Input
                      value={binding.clusterLabel}
                      onChange={(e) => (
                        setBinding({ ...binding, clusterLabel: e.target.value.trim() }),
                        setTest(undefined)
                      )}
                      className="font-mono"
                    />
                  </Field>
                  <Field label="Label value">
                    <Input
                      value={binding.clusterLabelValue}
                      placeholder={id.name}
                      onChange={(e) => (
                        setBinding({ ...binding, clusterLabelValue: e.target.value.trim() }),
                        setTest(undefined)
                      )}
                      className="font-mono"
                      disabled={!binding.clusterLabel}
                    />
                  </Field>
                </div>
              </div>
              <Field
                label="Extra matchers"
                optional
                hint='Added to every selector, e.g. env="prod".'
                error={matcherErr}
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  {binding.extraMatchers?.map((m) => (
                    <span
                      key={m}
                      className="inline-flex h-7 items-center gap-1 rounded bg-secondary px-2 font-mono text-xs"
                    >
                      {m}
                      <button
                        type="button"
                        aria-label={`Remove ${m}`}
                        onClick={() =>
                          setBinding({
                            ...binding,
                            extraMatchers: binding.extraMatchers?.filter((x) => x !== m),
                          })
                        }
                      >
                        <XIcon className="size-3" />
                      </button>
                    </span>
                  ))}
                  <Input
                    value={matcher}
                    onChange={(e) => setMatcher(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && matcher && !matcherErr) {
                        e.preventDefault()
                        setBinding({ ...binding, extraMatchers: [...(binding.extraMatchers ?? []), matcher] })
                        setMatcher('')
                      }
                    }}
                    placeholder='env="prod"  ⏎'
                    className="w-56 font-mono text-[13px]"
                    aria-label="Extra matcher"
                  />
                </div>
              </Field>
              <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/30 px-4 py-3">
                <code className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
                  {'{'}
                  {[
                    binding.clusterLabel
                      ? `${binding.clusterLabel}="${binding.clusterLabelValue || id.name}"`
                      : '',
                    ...(binding.extraMatchers ?? []),
                  ]
                    .filter(Boolean)
                    .join(', ')}
                  {'}'}
                </code>
                <Button
                  type="button"
                  size="sm"
                  disabled={testing.isPending || !!labelErr}
                  onClick={() =>
                    testing.mutate(undefined, { onSuccess: (r) => setTest(r as DatasourceTestResult) })
                  }
                  data-testid="test-datasource"
                >
                  {testing.isPending ? <Loader2Icon className="animate-spin" /> : <FlaskConicalIcon />} Test
                </Button>
              </div>
              {testing.error ? <WriteError error={testing.error} /> : null}
              {test ? (
                <TestResult
                  r={test}
                  onPick={(v) => (setBinding({ ...binding, clusterLabelValue: v }), setTest(undefined))}
                />
              ) : (
                <p className="text-[13px] text-muted-foreground">
                  Test runs the metrics checks against the datasource now, before any agent is installed. You
                  can register the cluster either way and fix problems later.
                </p>
              )}
            </div>
          ) : step === 2 ? (
            <div className="flex flex-col gap-5" data-testid="wizard-install">
              {!created ? (
                <div className="flex flex-col items-center gap-3 py-8 text-center">
                  <span className="flex size-12 items-center justify-center rounded-xl bg-primary/10">
                    <ServerIcon className="size-6 text-primary" />
                  </span>
                  <div className="font-semibold">Register {id.name}</div>
                  <p className="max-w-md text-[13px] text-muted-foreground">
                    The hub creates the cluster in state Registered and issues its agent token, which is shown
                    once. Workloads stay in audit until the cluster is Automation ready.
                  </p>
                  <Button onClick={() => setConfirm(true)} data-testid="register-cluster">
                    Register cluster
                  </Button>
                </div>
              ) : (
                <>
                  {created.agentToken ? (
                    <div>
                      <div className="mb-2 text-sm font-semibold">
                        1. Store the agent token in the cluster
                      </div>
                      <SecretOnce value={created.agentToken.value} />
                      <pre className="mt-2 overflow-x-auto rounded-lg border bg-muted/40 px-3.5 py-3 font-mono text-[12px]">
                        {`kubectl create namespace resource-optimizer\nkubectl -n resource-optimizer create secret generic resource-optimizer-agent-token \\\n  --from-literal=token='<the token above>'`}
                      </pre>
                    </div>
                  ) : null}
                  <div>
                    <div className="mb-2 text-sm font-semibold">2. Install the agent</div>
                    <InstallSnippets cluster={created.cluster.name} compact />
                  </div>
                </>
              )}
            </div>
          ) : (
            <VerifyStep cluster={created!.cluster.name} />
          )}
        </CardContent>
      </Card>
      <div className="flex items-center justify-between">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => (step === 0 ? navigate('/clusters') : setStep((s) => s - 1))}
          disabled={step === 2 && !!created}
        >
          <ArrowLeftIcon /> {step === 0 ? 'Cancel' : 'Back'}
        </Button>
        {step < 2 ? (
          <Button size="sm" onClick={next} data-testid="wizard-next">
            Continue <ArrowRightIcon />
          </Button>
        ) : step === 2 ? (
          <Button size="sm" onClick={() => setStep(3)} disabled={!created} data-testid="wizard-next">
            I have installed it <ArrowRightIcon />
          </Button>
        ) : (
          <Button
            size="sm"
            onClick={() => navigate(`/clusters/${encodeURIComponent(created!.cluster.name)}`)}
          >
            Open the cluster <ArrowRightIcon />
          </Button>
        )}
      </div>
      <DatasourceDialog
        open={dsOpen}
        onOpenChange={setDsOpen}
        onCreated={(d) => (setBinding({ ...binding, datasourceId: d.id }), setTest(undefined))}
      />
      <WriteDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={`Register ${id.name}`}
        description={`${id.environment} · ${POLICY_SOURCE_LABEL[id.policySource]} · metrics from ${ds?.name ?? binding.datasourceId}`}
        confirmLabel="Register cluster"
        reasonPlaceholder="e.g. new EU region for payments"
        onConfirm={async (o) => setCreated((await create.mutateAsync(o)) as ClusterCreated)}
        successMessage={`${id.name} registered`}
      />
    </div>
  )
}
