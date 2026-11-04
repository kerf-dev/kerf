import { HistoryIcon, Loader2Icon, PlayIcon } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useSettings, useSettingsVersions, useWrite, writes } from '@/api/admin'
import { useSummary } from '@/api/queries'
import type { RecommenderProfileSettings, RecommenderSettings } from '@/api/types'
import { ActorChip } from '@/components/admin/common'
import { SaveBar } from '@/components/admin/save-bar'
import { WriteDialog } from '@/components/admin/write-dialog'
import { ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FormSection } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toast'
import { formatGoDuration, relativeTime } from '@/lib/format'
import { isDuration } from '@/lib/validate'
import { cn } from '@/lib/utils'

const PROFILE_DESC: Record<string, string> = {
  dev: 'Short window, lower percentiles: aggressive savings for non-critical workloads.',
  production: 'The default: two weeks, P95 CPU and P99 memory with moderate headroom.',
  'high-availability': 'Long window, P99 and generous buffers for critical services.',
}

/** Numbers are edited as percent in the UI and stored as fractions (0.15 = 15 %). */
const pct = (f: number | undefined) => (f == null ? '' : String(Math.round(f * 1000) / 10))
const frac = (s: string) => (s === '' ? undefined : Number(s) / 100)

function validate(v: RecommenderSettings) {
  const e: Record<string, string> = {}
  if (!isDuration(v.interval)) e.interval = 'A duration like 1h or 24h.'
  if (!isDuration(v.retention)) e.retention = 'A duration like 2160h (90 days).'
  v.profiles.forEach((p, i) => {
    if (!isDuration(p.window)) e[`${i}.window`] = 'A duration like 336h.'
    if (p.memoryPeakWindow && !isDuration(p.memoryPeakWindow))
      e[`${i}.memoryPeakWindow`] = 'A duration, or empty.'
    for (const k of ['cpuPercentile', 'memoryPercentile'] as const)
      if (!(p[k] > 0 && p[k] <= 1)) e[`${i}.${k}`] = 'Between 0 and 100.'
    for (const k of ['cpuHeadroom', 'memoryBuffer'] as const)
      if (!(p[k] >= 0 && p[k] <= 10) || Number.isNaN(p[k])) e[`${i}.${k}`] = '0 to 1000 %.'
  })
  return e
}

function NumCell({
  value,
  onChange,
  err,
  prefix,
  suffix,
  label,
}: {
  value: string
  onChange: (s: string) => void
  err?: string
  prefix?: string
  suffix?: string
  label: string
}) {
  return (
    <div className="relative">
      {prefix ? (
        <span className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-xs text-muted-foreground">
          {prefix}
        </span>
      ) : null}
      <Input
        value={value}
        inputMode="decimal"
        onChange={(e) => onChange(e.target.value)}
        className={cn('font-mono text-[13px] tabular', prefix && 'pl-5', suffix && 'pr-6')}
        aria-invalid={!!err}
        aria-label={label}
        title={err}
      />
      {suffix ? (
        <span className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 text-xs text-muted-foreground">
          {suffix}
        </span>
      ) : null}
    </div>
  )
}

export default function RecommenderPage() {
  const settings = useSettings()
  const versions = useSettingsVersions('recommender')
  const summary = useSummary()
  const doc = settings.data?.recommender
  const base = useMemo(() => doc?.value, [doc])
  const [v, setV] = useState<RecommenderSettings>()
  const [confirm, setConfirm] = useState<'save' | 'recompute' | null>(null)
  useEffect(() => setV(base), [base])
  const save = useWrite(({ reason }: { reason: string }) =>
    writes.putRecommender({ value: v!, reason, version: doc!.version }),
  )
  const recompute = useWrite(() => writes.recompute({}))
  const errors = v ? validate(v) : {}
  const dirty = !!v && JSON.stringify(v) !== JSON.stringify(base)
  const setProfile = (i: number, patch: Partial<RecommenderProfileSettings>) =>
    setV((x) => x && { ...x, profiles: x.profiles.map((p, j) => (j === i ? { ...p, ...patch } : p)) })
  const lastRun = summary.data?.fleet.lastRecommenderRunAt

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Recommender"
        description="Hub-only settings: how recommendations are computed. They never reach clusters; a change takes effect on the next run."
        actions={
          <Button
            size="sm"
            variant="outline"
            onClick={() => setConfirm('recompute')}
            disabled={recompute.isPending}
            data-testid="recompute"
          >
            {recompute.isPending ? <Loader2Icon className="animate-spin" /> : <PlayIcon />} Recompute now
          </Button>
        }
      />
      {settings.isError ? (
        <Card>
          <ErrorState error={settings.error} onRetry={() => settings.refetch()} />
        </Card>
      ) : !v || !doc ? (
        <Skeleton className="h-[30rem] rounded-xl" />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px] text-muted-foreground">
            <span>
              {doc.version
                ? `Version ${doc.version}`
                : 'Values from the hub configuration file (never saved here)'}
            </span>
            {doc.updatedBy ? (
              <span className="inline-flex items-center gap-1.5">
                · changed by <ActorChip actor={doc.updatedBy} time={doc.updatedAt} />
              </span>
            ) : null}
            {lastRun ? <span>· last run {relativeTime(lastRun)}</span> : null}
          </div>
          <Card>
            <CardHeader>
              <div>
                <CardTitle>Profiles</CardTitle>
                <CardDescription>
                  A workload's profile comes from its policy (default: production). Percentiles of usage over
                  the window, plus headroom or buffer.
                </CardDescription>
              </div>
            </CardHeader>
            <CardContent className="overflow-x-auto pt-0">
              <table className="w-full min-w-[760px] text-[13px]" data-testid="profiles-table">
                <thead className="text-xs text-muted-foreground">
                  <tr>
                    <th className="py-2 pr-3 text-left font-medium">Profile</th>
                    <th className="px-1.5 py-2 text-left font-medium">Window</th>
                    <th className="px-1.5 py-2 text-left font-medium">CPU percentile</th>
                    <th className="px-1.5 py-2 text-left font-medium">CPU headroom</th>
                    <th className="px-1.5 py-2 text-left font-medium">Memory percentile</th>
                    <th className="px-1.5 py-2 text-left font-medium">Memory buffer</th>
                    <th className="px-1.5 py-2 text-left font-medium">Memory peak window</th>
                  </tr>
                </thead>
                <tbody>
                  {v.profiles.map((p, i) => (
                    <tr key={p.name} className="border-t align-top">
                      <td className="py-2.5 pr-3">
                        <div className="font-mono font-medium">{p.name}</div>
                        <div className="max-w-56 text-xs text-muted-foreground">{PROFILE_DESC[p.name]}</div>
                      </td>
                      <td className="w-24 px-1.5 py-2.5">
                        <NumCell
                          label={`${p.name} window`}
                          value={p.window}
                          onChange={(s) => setProfile(i, { window: s })}
                          err={errors[`${i}.window`]}
                        />
                        <div className="mt-1 text-[11px] text-muted-foreground">
                          {formatGoDuration(p.window)}
                        </div>
                      </td>
                      <td className="w-24 px-1.5 py-2.5">
                        <NumCell
                          label={`${p.name} CPU percentile`}
                          prefix="P"
                          value={pct(p.cpuPercentile)}
                          onChange={(s) => setProfile(i, { cpuPercentile: frac(s)! })}
                          err={errors[`${i}.cpuPercentile`]}
                        />
                      </td>
                      <td className="w-24 px-1.5 py-2.5">
                        <NumCell
                          label={`${p.name} CPU headroom`}
                          suffix="%"
                          value={pct(p.cpuHeadroom)}
                          onChange={(s) => setProfile(i, { cpuHeadroom: frac(s)! })}
                          err={errors[`${i}.cpuHeadroom`]}
                        />
                      </td>
                      <td className="w-24 px-1.5 py-2.5">
                        <NumCell
                          label={`${p.name} memory percentile`}
                          prefix="P"
                          value={pct(p.memoryPercentile)}
                          onChange={(s) => setProfile(i, { memoryPercentile: frac(s)! })}
                          err={errors[`${i}.memoryPercentile`]}
                        />
                      </td>
                      <td className="w-24 px-1.5 py-2.5">
                        <NumCell
                          label={`${p.name} memory buffer`}
                          suffix="%"
                          value={pct(p.memoryBuffer)}
                          onChange={(s) => setProfile(i, { memoryBuffer: frac(s)! })}
                          err={errors[`${i}.memoryBuffer`]}
                        />
                      </td>
                      <td className="w-28 px-1.5 py-2.5">
                        <NumCell
                          label={`${p.name} memory peak window`}
                          value={p.memoryPeakWindow ?? ''}
                          onChange={(s) => setProfile(i, { memoryPeakWindow: s || undefined })}
                          err={errors[`${i}.memoryPeakWindow`]}
                        />
                        <div className="mt-1 text-[11px] text-muted-foreground">
                          {p.memoryPeakWindow ? formatGoDuration(p.memoryPeakWindow) : 'whole window'}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {Object.keys(errors).some((k) => k.includes('.')) ? (
                <p className="mt-2 text-xs text-destructive">
                  {Object.entries(errors)
                    .filter(([k]) => k.includes('.'))
                    .map(([k, m]) => `${v.profiles[Number(k.split('.')[0])]?.name} ${k.split('.')[1]}: ${m}`)
                    .join(' · ')}
                </p>
              ) : null}
            </CardContent>
          </Card>
          <Card>
            <CardContent className="py-6">
              <FormSection
                title="Schedule and retention"
                description="How often the recommender runs and how long history is kept (the latest per workload is always kept)."
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field
                    label="Run interval"
                    error={errors.interval}
                    hint={isDuration(v.interval) ? `Every ${formatGoDuration(v.interval)}` : undefined}
                  >
                    <Input
                      value={v.interval}
                      onChange={(e) => setV({ ...v, interval: e.target.value })}
                      className="font-mono"
                      aria-invalid={!!errors.interval}
                    />
                  </Field>
                  <Field
                    label="History retention"
                    error={errors.retention}
                    hint={isDuration(v.retention) ? formatGoDuration(v.retention) : undefined}
                  >
                    <Input
                      value={v.retention}
                      onChange={(e) => setV({ ...v, retention: e.target.value })}
                      className="font-mono"
                      aria-invalid={!!errors.retention}
                    />
                  </Field>
                </div>
              </FormSection>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <HistoryIcon className="size-4 text-muted-foreground" /> Changes
              </CardTitle>
            </CardHeader>
            <CardContent className="pt-0">
              {versions.data?.items.length ? (
                <ul className="divide-y">
                  {versions.data.items.map((x) => (
                    <li
                      key={x.version}
                      className="flex flex-wrap items-center justify-between gap-2 py-2 text-[13px]"
                    >
                      <span className="flex items-center gap-2">
                        <span className="font-mono text-xs">v{x.version}</span>
                        <span className="text-muted-foreground">“{x.reason}”</span>
                      </span>
                      <ActorChip actor={x.actor} time={x.createdAt} />
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-[13px] text-muted-foreground">No changes recorded yet.</p>
              )}
            </CardContent>
          </Card>
          <SaveBar
            dirty={dirty}
            errors={Object.keys(errors).length}
            onDiscard={() => setV(base)}
            onSave={() => setConfirm('save')}
            saveLabel="Save"
          />
        </>
      )}
      <WriteDialog
        open={confirm === 'save'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Save recommender settings"
        description="Takes effect on the next run. Run it now with Recompute to see the new recommendations right away."
        onConfirm={(o) => save.mutateAsync(o)}
        successMessage="Recommender settings saved"
      />
      <WriteDialog
        open={confirm === 'recompute'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Recompute all recommendations now"
        description="Starts a recommender run for the whole fleet outside the schedule. It runs in the background; results appear within minutes."
        reasonRequired={false}
        confirmLabel="Start run"
        onConfirm={async () => {
          await recompute.mutateAsync(undefined)
          toast.success('Recompute started', 'New recommendations appear when the run finishes.')
        }}
      />
    </div>
  )
}
