import { PlusIcon, Trash2Icon } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useSettings, useSettingsVersions, useWrite, writes } from '@/api/admin'
import { useClusters } from '@/api/queries'
import type { ClusterPricingAdjustment, PricingSettings } from '@/api/types'
import { ActorChip } from '@/components/admin/common'
import { SaveBar } from '@/components/admin/save-bar'
import { WriteDialog } from '@/components/admin/write-dialog'
import { ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Callout, Field, NativeSelect } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

function num(s: string): number | undefined {
  if (s.trim() === '') return undefined
  const n = Number(s)
  return Number.isFinite(n) ? n : Number.NaN
}

function validate(v: PricingSettings) {
  const e: Record<string, string> = {}
  const d = v.defaultDiscountPercent
  if (d != null && !(d >= 0 && d <= 100)) e.default = '0 to 100.'
  v.clusters.forEach((c, i) => {
    if (c.discountPercent != null && !(c.discountPercent >= 0 && c.discountPercent <= 100))
      e[`${i}.discountPercent`] = '0 to 100.'
    for (const k of ['cpuCoreHourUSD', 'memoryGiBHourUSD'] as const)
      if (c[k] != null && !(c[k]! >= 0)) e[`${i}.${k}`] = 'A positive rate.'
  })
  return e
}

/** Per-cluster discounts (Savings Plans / EDP) and fixed rate overrides; hub-only. */
export default function PricingPage() {
  const settings = useSettings()
  const versions = useSettingsVersions('pricing')
  const clusters = useClusters()
  const doc = settings.data?.pricing
  const base = useMemo(() => doc?.value, [doc])
  const [v, setV] = useState<PricingSettings>()
  const [confirm, setConfirm] = useState(false)
  useEffect(() => setV(base), [base])
  const save = useWrite(({ reason }: { reason: string }) =>
    writes.putPricing({ value: v!, reason, version: doc!.version }),
  )
  const errors = v ? validate(v) : {}
  const dirty = !!v && JSON.stringify(v) !== JSON.stringify(base)
  const names = (clusters.data?.items ?? []).map((c) => c.name)
  const unused = names.filter((n) => !v?.clusters.some((c) => c.cluster === n))
  const setRow = (i: number, patch: Partial<ClusterPricingAdjustment>) =>
    setV((x) => x && { ...x, clusters: x.clusters.map((c, j) => (j === i ? clean({ ...c, ...patch }) : c)) })

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Pricing"
        description="Adjust the rates savings are computed with. The discount applies to the metric, default or fixed rate; savings update on the next recommender run."
      />
      {settings.isError ? (
        <Card>
          <ErrorState error={settings.error} onRetry={() => settings.refetch()} />
        </Card>
      ) : !v || !doc ? (
        <Skeleton className="h-96 rounded-xl" />
      ) : (
        <>
          {doc.updatedBy ? (
            <div className="flex items-center gap-1.5 text-[13px] text-muted-foreground">
              Version {doc.version} · changed by <ActorChip actor={doc.updatedBy} time={doc.updatedAt} />
            </div>
          ) : null}
          <Callout tone="info" title="Until the AWS Cost and Usage Report arrives (WO-16)">
            Rates come from the pricing service (on-demand list prices per instance type) or its default
            rates. Use discounts for Savings Plans and EDP, and fixed rates where you have negotiated prices.
          </Callout>
          <Card>
            <CardContent className="grid gap-4 py-5 sm:grid-cols-[1fr_12rem] sm:items-end">
              <div>
                <div className="text-sm font-semibold">Default discount</div>
                <p className="text-[13px] text-muted-foreground">
                  For clusters without their own entry below.
                </p>
              </div>
              <Field label="Discount" error={errors.default}>
                <div className="relative">
                  <Input
                    value={v.defaultDiscountPercent ?? ''}
                    onChange={(e) => setV({ ...v, defaultDiscountPercent: num(e.target.value) })}
                    inputMode="decimal"
                    className="pr-7 font-mono"
                    placeholder="0"
                    aria-invalid={!!errors.default}
                  />
                  <span className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs text-muted-foreground">
                    %
                  </span>
                </div>
              </Field>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <div>
                <CardTitle>Per cluster</CardTitle>
                <CardDescription>
                  A fixed rate replaces the metric or default rate; leave it empty to keep using it.
                </CardDescription>
              </div>
              {unused.length ? (
                <NativeSelect
                  className="w-48"
                  value=""
                  aria-label="Add a cluster"
                  onChange={(e) =>
                    e.target.value &&
                    setV({ ...v, clusters: [...v.clusters, { cluster: e.target.value, discountPercent: 0 }] })
                  }
                >
                  <option value="">+ Add a cluster…</option>
                  {unused.map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </NativeSelect>
              ) : null}
            </CardHeader>
            <CardContent className="overflow-x-auto pt-0">
              {v.clusters.length ? (
                <table className="w-full min-w-[640px] text-[13px]" data-testid="pricing-table">
                  <thead className="text-xs text-muted-foreground">
                    <tr>
                      <th className="py-2 text-left font-medium">Cluster</th>
                      <th className="px-2 py-2 text-left font-medium">Discount</th>
                      <th className="px-2 py-2 text-left font-medium">CPU $/core-hour</th>
                      <th className="px-2 py-2 text-left font-medium">Memory $/GiB-hour</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {v.clusters.map((c, i) => (
                      <tr key={c.cluster} className="border-t">
                        <td className="py-2 font-mono text-xs">{c.cluster}</td>
                        {(
                          [
                            ['discountPercent', '%', '0'],
                            ['cpuCoreHourUSD', '$', 'metric rate'],
                            ['memoryGiBHourUSD', '$', 'metric rate'],
                          ] as const
                        ).map(([k, unit, ph]) => (
                          <td key={k} className="w-44 px-2 py-2">
                            <div className="relative">
                              {unit === '$' ? (
                                <span className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-xs text-muted-foreground">
                                  $
                                </span>
                              ) : null}
                              <Input
                                value={c[k] ?? ''}
                                onChange={(e) => setRow(i, { [k]: num(e.target.value) })}
                                inputMode="decimal"
                                placeholder={ph}
                                className={cn('font-mono text-[13px]', unit === '$' ? 'pl-6' : 'pr-7')}
                                aria-invalid={!!errors[`${i}.${k}`]}
                                aria-label={`${c.cluster} ${k}`}
                              />
                              {unit === '%' ? (
                                <span className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs text-muted-foreground">
                                  %
                                </span>
                              ) : null}
                            </div>
                            {errors[`${i}.${k}`] ? (
                              <p className="mt-1 text-xs text-destructive">{errors[`${i}.${k}`]}</p>
                            ) : null}
                          </td>
                        ))}
                        <td className="w-10 py-2 text-right">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Remove ${c.cluster}`}
                            onClick={() => setV({ ...v, clusters: v.clusters.filter((_, j) => j !== i) })}
                          >
                            <Trash2Icon />
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed py-8 text-center text-[13px] text-muted-foreground">
                  Every cluster uses the default discount.
                  {unused[0] ? (
                    <Button
                      size="xs"
                      variant="outline"
                      onClick={() => setV({ ...v, clusters: [{ cluster: unused[0]!, discountPercent: 0 }] })}
                    >
                      <PlusIcon /> Add {unused[0]}
                    </Button>
                  ) : null}
                </div>
              )}
            </CardContent>
          </Card>
          {versions.data?.items.length ? (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle>Changes</CardTitle>
              </CardHeader>
              <CardContent className="pt-0">
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
              </CardContent>
            </Card>
          ) : null}
          <SaveBar
            dirty={dirty}
            errors={Object.keys(errors).length}
            onDiscard={() => setV(base)}
            onSave={() => setConfirm(true)}
            saveLabel="Save"
          />
        </>
      )}
      <WriteDialog
        open={confirm}
        onOpenChange={setConfirm}
        title="Save pricing"
        description="Savings figures change after the next recommender run."
        reasonPlaceholder="e.g. EDP 2027 signed: 28 % on compute"
        onConfirm={(o) => save.mutateAsync(o)}
        successMessage="Pricing saved"
      />
    </div>
  )
}

function clean(c: ClusterPricingAdjustment): ClusterPricingAdjustment {
  const out = { ...c }
  for (const k of ['discountPercent', 'cpuCoreHourUSD', 'memoryGiBHourUSD'] as const)
    if (out[k] === undefined) delete out[k]
  return out
}
