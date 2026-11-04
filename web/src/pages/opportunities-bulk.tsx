import { useQueryClient } from '@tanstack/react-query'
import { BanIcon, SlidersHorizontalIcon, XIcon } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, ApiError, unwrap } from '@/api/client'
import { writes } from '@/api/admin'
import { useAuth } from '@/api/auth'
import { useClusters } from '@/api/queries'
import type { Mode, RightsizingPolicySpec, WorkloadRow } from '@/api/types'
import { ImpactPreview } from '@/components/admin/impact-preview'
import { ModeField } from '@/components/admin/policy-form'
import { usePreview } from '@/components/admin/save-bar'
import { WriteDialog } from '@/components/admin/write-dialog'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/form'
import { formatNumber, formatUSD } from '@/lib/format'
import { withoutSelector } from '@/lib/policy'
import { prune } from '@/lib/spec'

/** Merges a mode change into a workload's existing override (keeping its other settings). */
async function applyOverride(
  w: WorkloadRow,
  modes: { cpu?: Mode; memory?: Mode },
  reason: string,
  overrideReadiness?: boolean,
) {
  let version: number | undefined
  let base: RightsizingPolicySpec = {}
  try {
    const cur = await unwrap(api.GET('/v1/workloads/{id}/override', { params: { path: { id: w.id } } }))
    version = cur.version
    base = withoutSelector(cur.spec)
  } catch (e) {
    if (!(e instanceof ApiError && e.status === 404)) throw e
  }
  const spec = prune({
    ...base,
    cpu: { ...base.cpu, ...(modes.cpu ? { mode: modes.cpu } : {}) },
    memory: { ...base.memory, ...(modes.memory ? { mode: modes.memory } : {}) },
  })
  return writes.putOverride({
    id: w.id,
    version,
    body: { spec, reason, ...(overrideReadiness ? { overrideReadiness } : {}) },
  })
}

/**
 * Bulk "Set mode…" / "Exclude" for the selected rows (plan §4): one workload override per workload,
 * always confirmed with the count and the savings impact from the hub's dry-run.
 */
export function BulkBar({ selected, onClear }: { selected: Map<string, WorkloadRow>; onClear: () => void }) {
  const auth = useAuth()
  const qc = useQueryClient()
  const clusters = useClusters()
  const [open, setOpen] = useState<'mode' | 'exclude' | null>(null)
  const [modes, setModes] = useState<{ cpu?: Mode; memory?: Mode }>({})
  const [progress, setProgress] = useState<{ done: number; total: number }>()
  const done = useRef(new Set<string>())
  useEffect(() => {
    if (open) {
      done.current = new Set()
      setProgress(undefined)
      if (open === 'mode') setModes({})
    }
  }, [open])
  const rows = [...selected.values()]
  const savings = rows.reduce((s, w) => s + (w.monthlySavingsUSD?.total ?? 0), 0)
  const target = open === 'exclude' ? { cpu: 'off' as Mode, memory: 'off' as Mode } : modes
  const spec = prune({
    ...(target.cpu ? { cpu: { mode: target.cpu } } : {}),
    ...(target.memory ? { memory: { mode: target.memory } } : {}),
  })
  const preview = usePreview(
    rows.map((w) => ({
      op: 'upsert' as const,
      policy: { type: 'workload-override' as const, workloadId: w.id, clusters: [w.cluster], spec },
    })),
    !!open && Object.keys(spec).length > 0,
  )
  const prodEnv = new Set(
    (clusters.data?.items ?? []).filter((c) => c.environment === 'prod').map((c) => c.name),
  )
  const prod = [...new Set(rows.map((w) => w.cluster))].filter((c) => prodEnv.has(c))
  const setsAuto = target.cpu === 'auto-patch' || target.memory === 'auto-patch'

  if (!selected.size || !auth.can('operator')) return null
  return (
    <>
      <div
        className="sticky bottom-4 z-20 flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-popover/95 px-4 py-2.5 shadow-lg backdrop-blur animate-in fade-in-0 slide-in-from-bottom-2"
        data-testid="bulk-bar"
        role="region"
        aria-label="Bulk actions"
      >
        <div className="text-[13px]">
          <span className="font-semibold tabular">{formatNumber(selected.size)}</span> selected
          <span className="text-muted-foreground"> · {formatUSD(savings)}/mo of savings</span>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={onClear}>
            <XIcon /> Clear
          </Button>
          <Button size="sm" variant="outline" onClick={() => setOpen('exclude')} data-testid="bulk-exclude">
            <BanIcon /> Exclude
          </Button>
          <Button size="sm" onClick={() => setOpen('mode')} data-testid="bulk-set-mode">
            <SlidersHorizontalIcon /> Set mode…
          </Button>
        </div>
      </div>
      <WriteDialog
        open={!!open}
        onOpenChange={(o) => !o && setOpen(null)}
        wide
        testId="bulk-dialog"
        title={
          open === 'exclude'
            ? `Exclude ${formatNumber(selected.size)} workloads`
            : `Set the mode of ${formatNumber(selected.size)} workloads`
        }
        description="Creates or updates one workload override per workload. Agents apply them on their next pull; annotations on a workload still win."
        prodClusters={prod}
        confirmLabel={
          progress
            ? `Saving ${progress.done}/${progress.total}…`
            : open === 'exclude'
              ? 'Exclude workloads'
              : 'Set mode'
        }
        confirmDisabled={!Object.keys(spec).length || !preview.data}
        reasonPlaceholder={
          open === 'exclude'
            ? 'e.g. batch jobs owned by data-platform'
            : 'e.g. FinOps Q4 rollout for payments'
        }
        onConfirm={async ({ reason, overrideReadiness }) => {
          const todo = rows.filter((w) => !done.current.has(w.id))
          const failures: { w: WorkloadRow; e: unknown }[] = []
          setProgress({ done: rows.length - todo.length, total: rows.length })
          for (const w of todo) {
            try {
              await applyOverride(w, target, reason, overrideReadiness)
              done.current.add(w.id)
            } catch (e) {
              failures.push({ w, e })
            }
            setProgress({ done: done.current.size, total: rows.length })
          }
          void qc.invalidateQueries()
          setProgress(undefined)
          if (failures.length) {
            // Same refusal for all (e.g. readiness or role): surface it as such so the dialog can offer the override.
            const first = failures[0]!.e
            if (
              first instanceof ApiError &&
              failures.every((f) => f.e instanceof ApiError && f.e.code === first.code)
            ) {
              throw new ApiError(first.status, {
                ...first.problem,
                detail: `${done.current.size} of ${rows.length} saved. ${failures.length} refused: ${first.message}`,
              })
            }
            throw new Error(
              `${done.current.size} of ${rows.length} saved. Failed: ${failures
                .slice(0, 5)
                .map((f) => `${f.w.name} (${f.e instanceof ApiError ? (f.e.code ?? f.e.status) : 'error'})`)
                .join(
                  ', ',
                )}${failures.length > 5 ? ` and ${failures.length - 5} more` : ''}. Confirm again to retry the failed ones.`,
            )
          }
          onClear()
        }}
        successMessage={
          open === 'exclude'
            ? `Excluded ${formatNumber(selected.size)} workloads`
            : `Mode set for ${formatNumber(selected.size)} workloads`
        }
      >
        {open === 'mode' ? (
          <div className="grid gap-4 md:grid-cols-2">
            <ModeField
              label="CPU mode"
              value={modes.cpu}
              onChange={(m) => setModes({ ...modes, cpu: m })}
              testId="bulk-cpu"
            />
            <ModeField
              label="Memory mode"
              value={modes.memory}
              onChange={(m) => setModes({ ...modes, memory: m })}
              testId="bulk-memory"
            />
          </div>
        ) : null}
        {setsAuto && prod.length && !auth.can('admin') ? (
          <Callout tone="warning" title="Needs an admin">
            Auto-patch on prod clusters ({prod.join(', ')}) can only be set by an admin; those workloads will
            be refused.
          </Callout>
        ) : null}
        {Object.keys(spec).length ? (
          <ImpactPreview preview={preview.data} loading={preview.isFetching} error={preview.error} compact />
        ) : (
          <p className="rounded-lg border border-dashed px-4 py-4 text-center text-[13px] text-muted-foreground">
            Choose a CPU or memory mode to see the impact.
          </p>
        )}
      </WriteDialog>
    </>
  )
}
