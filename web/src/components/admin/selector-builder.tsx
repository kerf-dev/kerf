import { PlusIcon, SearchIcon, XIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useFacets, useWorkloads } from '@/api/queries'
import type { NamespacedWorkloadRef, PolicySelector, WorkloadKind, WorkloadRow } from '@/api/types'
import { WORKLOAD_KINDS } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { prune } from '@/lib/spec'
import { globMatch, validateGlob, validateLabelKey, validateLabelValue, type Errors } from '@/lib/validate'
import { cn } from '@/lib/utils'

/** Does a workload row match a selector (all set criteria, AND)? `matchLabels` cannot be checked here. */
export function matchesSelector(
  w: Pick<WorkloadRow, 'namespace' | 'kind' | 'name' | 'cluster'>,
  s: PolicySelector | undefined,
  clusters?: string[],
) {
  if (clusters?.length && !clusters.includes(w.cluster)) return false
  if (!s) return true
  if (s.namespaces?.length && !s.namespaces.some((g) => globMatch(g, w.namespace))) return false
  if (s.excludeNamespaces?.some((g) => globMatch(g, w.namespace))) return false
  if (s.kinds?.length && !s.kinds.includes(w.kind)) return false
  if (
    s.workloads?.length &&
    !s.workloads.some((r) => r.kind === w.kind && r.namespace === w.namespace && r.name === w.name)
  )
    return false
  return true
}

function ChipInput({
  values,
  onChange,
  suggestions,
  placeholder,
  validate,
  disabled,
  label,
  testId,
}: {
  values: string[]
  onChange: (v: string[]) => void
  suggestions: string[]
  placeholder: string
  validate: (v: string) => string | undefined
  disabled?: boolean
  label: string
  testId?: string
}) {
  const [text, setText] = useState('')
  const [err, setErr] = useState<string>()
  const id = useId()
  const add = (raw: string) => {
    const v = raw.trim()
    if (!v) return
    const e = values.includes(v) ? 'Already added.' : validate(v)
    if (e) return setErr(e)
    onChange([...values, v])
    setText('')
    setErr(undefined)
  }
  return (
    <div className="flex flex-col gap-1.5" data-testid={testId}>
      <div
        className={cn(
          'flex min-h-8 flex-wrap items-center gap-1 rounded-md border border-input bg-card px-1.5 py-1 shadow-xs focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/30',
          disabled && 'opacity-60',
          err && 'border-destructive',
        )}
      >
        {values.map((v) => (
          <span
            key={v}
            className="inline-flex h-6 items-center gap-1 rounded bg-secondary px-1.5 font-mono text-xs"
          >
            {v}
            {!disabled ? (
              <button
                type="button"
                onClick={() => onChange(values.filter((x) => x !== v))}
                aria-label={`Remove ${v}`}
                className="text-muted-foreground hover:text-foreground"
              >
                <XIcon className="size-3" />
              </button>
            ) : null}
          </span>
        ))}
        <input
          id={id}
          list={`${id}-list`}
          value={text}
          disabled={disabled}
          aria-label={label}
          onChange={(e) => {
            setText(e.target.value)
            setErr(undefined)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ',' || e.key === 'Tab') {
              if (text.trim()) {
                e.preventDefault()
                add(text)
              }
            } else if (e.key === 'Backspace' && !text && values.length) onChange(values.slice(0, -1))
          }}
          onBlur={() => text.trim() && add(text)}
          placeholder={values.length ? '' : placeholder}
          className="h-6 min-w-24 flex-1 bg-transparent px-1 font-mono text-[13px] outline-none placeholder:font-sans placeholder:text-muted-foreground/80"
        />
        <datalist id={`${id}-list`}>
          {suggestions
            .filter((s) => !values.includes(s))
            .map((s) => (
              <option key={s} value={s} />
            ))}
        </datalist>
      </div>
      {err ? <p className="text-xs text-destructive">{err}</p> : null}
    </div>
  )
}

function WorkloadPicker({
  onPick,
  clusters,
  disabled,
}: {
  onPick: (w: WorkloadRow) => void
  clusters?: string[]
  disabled?: boolean
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const list = useWorkloads(
    { q: q || undefined, cluster: clusters?.length ? clusters : undefined, limit: 8, sort: 'name' },
    { enabled: open && q.length >= 2 },
  )
  return (
    <Popover open={open && q.length >= 2} onOpenChange={setOpen}>
      <PopoverAnchor asChild>
        <div className="relative">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={q}
            disabled={disabled}
            onChange={(e) => {
              setQ(e.target.value)
              setOpen(true)
            }}
            onFocus={() => setOpen(true)}
            placeholder="Search a workload to add…"
            className="pl-8"
            aria-label="Search workloads"
          />
        </div>
      </PopoverAnchor>
      <PopoverContent
        align="start"
        className="w-[var(--radix-popover-trigger-width)] min-w-72 p-1"
        onOpenAutoFocus={(e) => e.preventDefault()}
      >
        {list.data?.items.length ? (
          list.data.items.map((w) => (
            <button
              key={w.id}
              type="button"
              onClick={() => {
                onPick(w)
                setQ('')
                setOpen(false)
              }}
              className="flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left text-[13px] hover:bg-accent"
            >
              <span className="font-medium">{w.name}</span>
              <span className="text-xs text-muted-foreground">
                {w.cluster} · {w.namespace} · {w.kind}
              </span>
            </button>
          ))
        ) : (
          <div className="px-2 py-3 text-center text-xs text-muted-foreground">
            {list.isFetching ? 'Searching…' : 'No workloads found'}
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}

/** Builder for `spec.selector`: namespaces (globs), excludes, kinds, labels and specific workloads. */
export function SelectorBuilder({
  value,
  onChange,
  errors = {},
  disabled,
  clusters,
}: {
  value: PolicySelector
  onChange: (s: PolicySelector) => void
  errors?: Errors
  disabled?: boolean
  clusters?: string[]
}) {
  const facets = useFacets({})
  const namespaces = useMemo(() => (facets.data?.namespaces ?? []).map((n) => n.value), [facets.data])
  const set = (patch: Partial<PolicySelector>) => onChange(prune({ ...value, ...patch }))
  const labels = Object.entries(value.matchLabels ?? {})
  const [lk, setLk] = useState('')
  const [lv, setLv] = useState('')
  const labelErr = lk ? (validateLabelKey(lk) ?? validateLabelValue(lv)) : undefined

  return (
    <div className="grid gap-4">
      <div className="grid gap-4 md:grid-cols-2">
        <Field
          label="Namespaces"
          hint="Globs allowed, e.g. team-*. Empty = every namespace."
          error={errors['selector.namespaces']}
        >
          <ChipInput
            label="Namespaces"
            values={value.namespaces ?? []}
            onChange={(v) => set({ namespaces: v })}
            suggestions={namespaces}
            placeholder="payments, team-*"
            validate={validateGlob}
            disabled={disabled}
            testId="selector-namespaces"
          />
        </Field>
        <Field label="Exclude namespaces" optional error={errors['selector.excludeNamespaces']}>
          <ChipInput
            label="Exclude namespaces"
            values={value.excludeNamespaces ?? []}
            onChange={(v) => set({ excludeNamespaces: v })}
            suggestions={namespaces}
            placeholder="kube-*"
            validate={validateGlob}
            disabled={disabled}
          />
        </Field>
      </div>
      <Field label="Kinds" hint="Empty = every kind.">
        <div className="flex flex-wrap gap-1.5" role="group" aria-label="Kinds">
          {WORKLOAD_KINDS.map((k) => {
            const on = value.kinds?.includes(k) ?? false
            return (
              <button
                key={k}
                type="button"
                disabled={disabled}
                aria-pressed={on}
                onClick={() =>
                  set({
                    kinds: on
                      ? (value.kinds ?? []).filter((x) => x !== k)
                      : [...(value.kinds ?? []), k as WorkloadKind],
                  })
                }
                className={cn(
                  'h-7 rounded-md border px-2.5 text-[13px] transition-colors disabled:opacity-60',
                  on
                    ? 'border-primary/40 bg-primary/10 text-primary'
                    : 'bg-card text-muted-foreground hover:text-foreground',
                )}
              >
                {k}
              </button>
            )
          })}
        </div>
      </Field>
      <Field
        label="Match labels"
        optional
        hint="Workload labels; every pair must match. Evaluated by the agent on the cluster."
        error={errors['selector.matchLabels'] ?? labelErr}
      >
        <div className="flex flex-col gap-1.5">
          {labels.map(([k, v]) => (
            <div key={k} className="flex items-center gap-1.5">
              <code className="flex h-7 min-w-0 flex-1 items-center truncate rounded bg-secondary px-2 text-xs">
                {k}={v}
              </code>
              {!disabled ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Remove label ${k}`}
                  onClick={() => {
                    const next = { ...value.matchLabels }
                    delete next[k]
                    set({ matchLabels: next })
                  }}
                >
                  <XIcon />
                </Button>
              ) : null}
            </div>
          ))}
          {!disabled ? (
            <div className="flex items-center gap-1.5">
              <Input
                value={lk}
                onChange={(e) => setLk(e.target.value)}
                placeholder="app.kubernetes.io/part-of"
                className="font-mono text-[13px]"
                aria-label="Label key"
              />
              <span className="text-muted-foreground">=</span>
              <Input
                value={lv}
                onChange={(e) => setLv(e.target.value)}
                placeholder="shop"
                className="font-mono text-[13px]"
                aria-label="Label value"
              />
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!lk || !!labelErr}
                onClick={() => {
                  set({ matchLabels: { ...value.matchLabels, [lk]: lv } })
                  setLk('')
                  setLv('')
                }}
              >
                <PlusIcon /> Add
              </Button>
            </div>
          ) : null}
        </div>
      </Field>
      <Field
        label="Specific workloads"
        optional
        hint="Narrow the policy to these workloads (combined with the criteria above)."
        error={errors['selector.workloads']}
      >
        <div className="flex flex-col gap-1.5">
          {(value.workloads ?? []).map((w: NamespacedWorkloadRef) => (
            <div
              key={`${w.namespace}/${w.kind}/${w.name}`}
              className="flex items-center justify-between gap-2 rounded-md border bg-card px-2.5 py-1 text-[13px]"
            >
              <span className="truncate">
                <span className="font-medium">{w.name}</span>{' '}
                <span className="text-muted-foreground">
                  {w.namespace} · {w.kind}
                </span>
              </span>
              {!disabled ? (
                <button
                  type="button"
                  aria-label={`Remove ${w.name}`}
                  className="text-muted-foreground hover:text-foreground"
                  onClick={() => set({ workloads: (value.workloads ?? []).filter((x) => x !== w) })}
                >
                  <XIcon className="size-3.5" />
                </button>
              ) : null}
            </div>
          ))}
          {!disabled ? (
            <WorkloadPicker
              clusters={clusters}
              onPick={(w) => {
                if (
                  (value.workloads ?? []).some(
                    (x) => x.kind === w.kind && x.namespace === w.namespace && x.name === w.name,
                  )
                )
                  return
                set({
                  workloads: [
                    ...(value.workloads ?? []),
                    { kind: w.kind, namespace: w.namespace, name: w.name },
                  ],
                })
              }}
            />
          ) : null}
        </div>
      </Field>
    </div>
  )
}
