import { CalendarClockIcon, PlusIcon, Trash2Icon } from 'lucide-react'
import { useId } from 'react'
import type { Mode, RightsizingPolicySpec, TimeWindow } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Field, FormSection, NativeSelect, Switch } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { formatGoDuration } from '@/lib/format'
import { getIn, MODE_LABEL, setIn } from '@/lib/spec'
import type { Errors } from '@/lib/validate'
import { cn } from '@/lib/utils'

export type SpecSection = 'modes' | 'limits' | 'guardrails' | 'rollback' | 'hpa' | 'jvm' | 'windows'
export const ALL_SECTIONS: SpecSection[] = [
  'modes',
  'limits',
  'guardrails',
  'rollback',
  'hpa',
  'jvm',
  'windows',
]

interface Ctx {
  value: RightsizingPolicySpec
  set: (path: string, v: unknown) => void
  /** Effective values of the layers below, shown as hints and placeholders. */
  inherited: RightsizingPolicySpec
  inheritedFrom: string
  errors: Errors
  disabled?: boolean
  idp: string
}

/**
 * Editor for a RightsizingPolicy spec. Every field can be left unset to inherit from the layer below
 * (built-in < global defaults < cluster defaults < policy < workload override); the inherited value is
 * shown next to the field so the effect of "unset" is always visible.
 */
export function PolicySpecForm({
  value,
  onChange,
  inherited,
  inheritedFrom,
  errors = {},
  disabled,
  sections = ALL_SECTIONS,
}: {
  value: RightsizingPolicySpec
  onChange: (v: RightsizingPolicySpec) => void
  inherited: RightsizingPolicySpec
  inheritedFrom: string
  errors?: Errors
  disabled?: boolean
  sections?: SpecSection[]
}) {
  const idp = useId()
  const ctx: Ctx = {
    value,
    set: (path, v) => onChange(setIn(value, path, v)),
    inherited,
    inheritedFrom,
    errors,
    disabled,
    idp,
  }
  const has = (s: SpecSection) => sections.includes(s)
  return (
    <div className="divide-y">
      {has('modes') ? <ModesSection {...ctx} /> : null}
      {has('limits') ? <LimitsSection {...ctx} /> : null}
      {has('guardrails') ? <GuardrailsSection {...ctx} /> : null}
      {has('rollback') ? <RollbackSection {...ctx} /> : null}
      {has('hpa') || has('jvm') ? <HpaJvmSection {...ctx} hpa={has('hpa')} jvm={has('jvm')} /> : null}
      {has('windows') ? <WindowsSection {...ctx} /> : null}
    </div>
  )
}

function inheritText(c: Ctx, path: string, fmt?: (v: unknown) => string) {
  const v = getIn(c.inherited, path)
  if (v === undefined) return undefined
  return `Inherits ${fmt ? fmt(v) : String(v)} from ${c.inheritedFrom}`
}

const boolText = (v: unknown) => (v ? 'on' : 'off')

export function ModeField({
  label,
  value,
  inherited,
  onChange,
  disabled,
  testId,
}: {
  label: string
  value?: Mode
  inherited?: Mode
  onChange: (m: Mode | undefined) => void
  disabled?: boolean
  testId?: string
}) {
  return (
    <div className="flex flex-col gap-1.5" data-testid={testId}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-[13px] font-medium">{label}</span>
        {inherited && value !== inherited ? (
          <span className="text-[11px] text-muted-foreground">
            {value
              ? `overrides ${MODE_LABEL[inherited].toLowerCase()}`
              : `inherits ${MODE_LABEL[inherited].toLowerCase()}`}
          </span>
        ) : null}
      </div>
      <fieldset disabled={disabled} className="contents">
        <Segmented
          label={label}
          value={value ?? 'inherit'}
          onChange={(v) => onChange(v === 'inherit' ? undefined : (v as Mode))}
          className="w-full [&>*]:flex-1 [&>*]:justify-center [&>*]:px-1.5 [&>*]:whitespace-nowrap"
          options={[
            { value: 'inherit', label: 'Inherit' },
            ...(['off', 'audit', 'auto-patch'] as Mode[]).map((m) => ({ value: m, label: MODE_LABEL[m] })),
          ]}
        />
      </fieldset>
    </div>
  )
}

function SelectRow({
  c,
  path,
  label,
  options,
  hint,
  fmt,
}: {
  c: Ctx
  path: string
  label: string
  options: { value: string; label: string }[]
  hint?: string
  fmt?: (v: unknown) => string
}) {
  const id = `${c.idp}-${path}`
  const v = getIn(c.value, path)
  return (
    <Field
      label={label}
      htmlFor={id}
      hint={hint}
      inherited={v === undefined ? inheritText(c, path, fmt) : undefined}
      error={c.errors[path]}
    >
      <NativeSelect
        id={id}
        value={v === undefined ? '' : String(v)}
        disabled={c.disabled}
        onChange={(e) => {
          const raw = e.target.value
          c.set(path, raw === '' ? undefined : raw === 'true' ? true : raw === 'false' ? false : raw)
        }}
        aria-invalid={!!c.errors[path]}
      >
        <option value="">Inherit</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </NativeSelect>
    </Field>
  )
}

function TextRow({
  c,
  path,
  label,
  hint,
  placeholderFmt,
  number,
  suffix,
  mono = true,
}: {
  c: Ctx
  path: string
  label: string
  hint?: string
  placeholderFmt?: (v: unknown) => string
  number?: boolean
  suffix?: string
  mono?: boolean
}) {
  const id = `${c.idp}-${path}`
  const v = getIn(c.value, path)
  const inh = getIn(c.inherited, path)
  return (
    <Field
      label={label}
      htmlFor={id}
      hint={hint}
      inherited={v === undefined ? inheritText(c, path, placeholderFmt) : undefined}
      error={c.errors[path]}
    >
      <div className="relative">
        <Input
          id={id}
          value={v === undefined ? '' : String(v)}
          disabled={c.disabled}
          inputMode={number ? 'numeric' : undefined}
          placeholder={inh === undefined ? '' : String(inh)}
          className={cn(mono && 'font-mono text-[13px]', suffix && 'pr-10')}
          onChange={(e) => {
            const raw = e.target.value.trim()
            if (!number) return c.set(path, raw || undefined)
            if (raw === '') return c.set(path, undefined)
            const n = Number(raw)
            c.set(path, Number.isFinite(n) ? n : raw)
          }}
          aria-invalid={!!c.errors[path]}
        />
        {suffix ? (
          <span className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs text-muted-foreground">
            {suffix}
          </span>
        ) : null}
      </div>
    </Field>
  )
}

function ModesSection(c: Ctx) {
  return (
    <FormSection
      title="Modes and strategy"
      description="Audit only recommends; auto-patch applies changes within the guardrails. CPU and memory are set separately."
    >
      <div className="grid gap-4">
        <div className="grid gap-4 md:grid-cols-2">
          <ModeField
            label="CPU mode"
            value={c.value.cpu?.mode}
            inherited={c.inherited.cpu?.mode}
            onChange={(m) => c.set('cpu.mode', m)}
            disabled={c.disabled}
            testId="field-cpu-mode"
          />
          <ModeField
            label="Memory mode"
            value={c.value.memory?.mode}
            inherited={c.inherited.memory?.mode}
            onChange={(m) => c.set('memory.mode', m)}
            disabled={c.disabled}
            testId="field-memory-mode"
          />
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          <SelectRow
            c={c}
            path="apply.strategy"
            label="Apply strategy"
            options={[
              { value: 'in-place', label: 'In-place resize' },
              { value: 'rollout', label: 'Rolling restart' },
              { value: 'deferred', label: 'Deferred to window' },
            ]}
          />
          <SelectRow
            c={c}
            path="profile"
            label="Recommender profile"
            options={[
              { value: 'dev', label: 'dev' },
              { value: 'production', label: 'production' },
              { value: 'high-availability', label: 'high-availability' },
            ]}
          />
          <TextRow
            c={c}
            path="apply.fallbackAfter"
            label="Fall back to rollout after"
            hint="When in-place resize does not take effect."
            placeholderFmt={(v) => formatGoDuration(String(v))}
          />
        </div>
      </div>
    </FormSection>
  )
}

function LimitsSection(c: Ctx) {
  const limits = getIn(c.value, 'cpu.limits') ?? getIn(c.inherited, 'cpu.limits')
  return (
    <FormSection title="Limits" description="How limits follow the new requests.">
      <div className="grid gap-4 sm:grid-cols-3">
        <SelectRow
          c={c}
          path="cpu.limits"
          label="CPU limits"
          options={[
            { value: 'keep', label: 'Keep as they are' },
            { value: 'remove', label: 'Remove' },
            { value: 'ratio', label: 'Ratio of the request' },
          ]}
        />
        {limits === 'ratio' || getIn(c.value, 'cpu.limitRatio') !== undefined ? (
          <TextRow
            c={c}
            path="cpu.limitRatio"
            label="CPU limit ratio"
            hint='e.g. "2" for limit = 2 × request.'
            suffix="×"
          />
        ) : (
          <div className="hidden sm:block" />
        )}
        <SelectRow
          c={c}
          path="memory.limitEqualsRequest"
          label="Memory limit = request"
          options={[
            { value: 'true', label: 'Yes' },
            { value: 'false', label: 'No, keep the limit' },
          ]}
          fmt={(v) => (v ? 'yes' : 'no')}
        />
      </div>
    </FormSection>
  )
}

function GuardrailsSection(c: Ctx) {
  return (
    <FormSection
      title="Guardrails"
      description="Limits on what auto-patch may change. A policy change never bypasses them."
    >
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <TextRow
          c={c}
          path="guardrails.learningPeriod"
          label="Learning period"
          hint="No changes before a workload is this old."
        />
        <TextRow c={c} path="guardrails.minConfidence" label="Minimum confidence" hint="0 to 1, e.g. 0.8." />
        <TextRow c={c} path="guardrails.maxChangePercent" label="Max change per step" number suffix="%" />
        <TextRow c={c} path="guardrails.minCPU" label="Minimum CPU request" hint="e.g. 10m" />
        <TextRow c={c} path="guardrails.minMemory" label="Minimum memory request" hint="e.g. 32Mi" />
        <TextRow c={c} path="guardrails.cooldown" label="Cooldown between changes" />
      </div>
    </FormSection>
  )
}

function RollbackSection(c: Ctx) {
  return (
    <FormSection
      title="Rollback"
      description="After a change, watch the workload and roll back when it degrades."
    >
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <TextRow c={c} path="guardrails.rollback.watchDuration" label="Watch for" />
        <TextRow
          c={c}
          path="guardrails.rollback.oomKills"
          label="OOM kills"
          number
          hint="Roll back at this many."
        />
        <TextRow c={c} path="guardrails.rollback.restarts" label="Restarts" number />
        <TextRow c={c} path="guardrails.rollback.throttlingRatio" label="Throttling ratio" hint="0 to 1." />
      </div>
    </FormSection>
  )
}

function HpaJvmSection(c: Ctx & { hpa: boolean; jvm: boolean }) {
  return (
    <FormSection title="HPA and JVM" description="Coordinate with autoscalers; detect fixed JVM heaps.">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {c.hpa ? (
          <>
            <SelectRow
              c={c}
              path="hpa.coordinate"
              label="Coordinate with HPAs"
              options={[
                { value: 'true', label: 'On' },
                { value: 'false', label: 'Off' },
              ]}
              fmt={boolText}
            />
            <TextRow c={c} path="hpa.targetBounds.minUtilization" label="HPA target min" number suffix="%" />
            <TextRow c={c} path="hpa.targetBounds.maxUtilization" label="HPA target max" number suffix="%" />
          </>
        ) : null}
        {c.jvm ? (
          <SelectRow
            c={c}
            path="jvm.detect"
            label="Detect JVM heap"
            options={[
              { value: 'true', label: 'On' },
              { value: 'false', label: 'Off' },
            ]}
            fmt={boolText}
          />
        ) : null}
      </div>
    </FormSection>
  )
}

// ------------------------------------------------------------------ windows

const CRON_PRESETS: { label: string; schedule: string; duration: string }[] = [
  { label: 'Weeknights 02:00–04:00', schedule: '0 2 * * 1-5', duration: '2h' },
  { label: 'Every night 01:00–05:00', schedule: '0 1 * * *', duration: '4h' },
  { label: 'Weekends all day', schedule: '0 0 * * 6', duration: '48h' },
  { label: 'Sunday 03:00–06:00', schedule: '0 3 * * 0', duration: '3h' },
]

const COMMON_TZ = [
  'UTC',
  'Europe/London',
  'Europe/Berlin',
  'Asia/Jerusalem',
  'America/New_York',
  'America/Los_Angeles',
  'Asia/Singapore',
]

/** Plain-language reading of the common cron shapes; falls back to the expression. */
export function describeCron(expr: string): string {
  const f = expr.trim().split(/\s+/)
  if (f.length !== 5) return expr
  const [m, h, dom, mon, dow] = f as [string, string, string, string, string]
  if (!/^\d+$/.test(m) || !/^\d+$/.test(h) || dom !== '*' || mon !== '*') return expr
  const time = `${h.padStart(2, '0')}:${m.padStart(2, '0')}`
  const days: Record<string, string> = {
    '*': 'Every day',
    '1-5': 'Weekdays',
    '0,6': 'Weekends',
    '6,0': 'Weekends',
    '0': 'Sundays',
    '1': 'Mondays',
    '2': 'Tuesdays',
    '3': 'Wednesdays',
    '4': 'Thursdays',
    '5': 'Fridays',
    '6': 'Saturdays',
  }
  return days[dow] ? `${days[dow]} at ${time}` : expr
}

function WindowEditor({
  w,
  onChange,
  onRemove,
  path,
  c,
}: {
  w: TimeWindow
  onChange: (w: TimeWindow) => void
  onRemove: () => void
  path: string
  c: Ctx
}) {
  const recurring = !(w.start || w.end)
  const err = (k: string) => c.errors[`${path}.${k}`]
  const toLocal = (iso?: string) => (iso ? iso.slice(0, 16) : '')
  const fromLocal = (v: string) => (v ? new Date(v + ':00Z').toISOString().replace('.000', '') : undefined)
  return (
    <div className="rounded-lg border bg-muted/20 p-3" data-testid="window-editor">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <Segmented
          label="Window type"
          value={recurring ? 'recurring' : 'once'}
          onChange={(v) =>
            onChange(
              v === 'recurring'
                ? { schedule: '0 2 * * 1-5', duration: '2h', timeZone: w.timeZone }
                : {
                    start: new Date(Date.now() + 86400_000).toISOString().slice(0, 16) + ':00Z',
                    end: new Date(Date.now() + 2 * 86400_000).toISOString().slice(0, 16) + ':00Z',
                    timeZone: w.timeZone,
                  },
            )
          }
          options={[
            { value: 'recurring', label: 'Recurring' },
            { value: 'once', label: 'One-off' },
          ]}
        />
        <div className="flex items-center gap-2">
          {recurring && w.schedule ? (
            <span className="text-xs text-muted-foreground">
              {describeCron(w.schedule)}
              {w.duration ? ` for ${formatGoDuration(w.duration)}` : ''}
              {w.timeZone ? ` (${w.timeZone})` : ' (UTC)'}
            </span>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={onRemove}
            aria-label="Remove window"
            disabled={c.disabled}
          >
            <Trash2Icon />
          </Button>
        </div>
      </div>
      {c.errors[path] ? <p className="mb-2 text-xs text-destructive">{c.errors[path]}</p> : null}
      <div className="grid gap-3 sm:grid-cols-3">
        {recurring ? (
          <>
            <Field label="Schedule (cron)" error={err('schedule')} hint="minute hour day month weekday">
              <div className="flex gap-1.5">
                <Input
                  value={w.schedule ?? ''}
                  onChange={(e) => onChange({ ...w, schedule: e.target.value || undefined })}
                  className="font-mono text-[13px]"
                  aria-invalid={!!err('schedule')}
                  disabled={c.disabled}
                  aria-label="Cron schedule"
                />
                <NativeSelect
                  aria-label="Schedule presets"
                  className="w-9 shrink-0 [&_select]:pr-0 [&_select]:pl-2 [&_select]:text-transparent"
                  value=""
                  disabled={c.disabled}
                  onChange={(e) => {
                    const p = CRON_PRESETS.find((x) => x.label === e.target.value)
                    if (p) onChange({ ...w, schedule: p.schedule, duration: p.duration })
                  }}
                >
                  <option value="">Presets…</option>
                  {CRON_PRESETS.map((p) => (
                    <option key={p.label} value={p.label}>
                      {p.label}
                    </option>
                  ))}
                </NativeSelect>
              </div>
            </Field>
            <Field label="Duration" error={err('duration')}>
              <Input
                value={w.duration ?? ''}
                onChange={(e) => onChange({ ...w, duration: e.target.value || undefined })}
                className="font-mono text-[13px]"
                placeholder="2h"
                aria-invalid={!!err('duration')}
                disabled={c.disabled}
                aria-label="Duration"
              />
            </Field>
          </>
        ) : (
          <>
            <Field label="Start (UTC)" error={err('start')}>
              <Input
                type="datetime-local"
                value={toLocal(w.start)}
                onChange={(e) => onChange({ ...w, start: fromLocal(e.target.value) })}
                aria-invalid={!!err('start')}
                disabled={c.disabled}
              />
            </Field>
            <Field label="End (UTC)" error={err('end')}>
              <Input
                type="datetime-local"
                value={toLocal(w.end)}
                onChange={(e) => onChange({ ...w, end: fromLocal(e.target.value) })}
                aria-invalid={!!err('end')}
                disabled={c.disabled}
              />
            </Field>
          </>
        )}
        <Field label="Time zone" error={err('timeZone')}>
          <Input
            list={`${c.idp}-tz`}
            value={w.timeZone ?? ''}
            placeholder="UTC"
            onChange={(e) => onChange({ ...w, timeZone: e.target.value || undefined })}
            aria-invalid={!!err('timeZone')}
            disabled={c.disabled}
            aria-label="Time zone"
          />
        </Field>
      </div>
    </div>
  )
}

function WindowList({ c, kind }: { c: Ctx; kind: 'maintenanceWindows' | 'freezeWindows' }) {
  const path = `apply.${kind}`
  const list = (getIn(c.value, path) as TimeWindow[] | undefined) ?? []
  const inherited = (getIn(c.inherited, path) as TimeWindow[] | undefined) ?? []
  const title = kind === 'maintenanceWindows' ? 'Maintenance windows' : 'Freeze windows'
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <div>
          <div className="text-[13px] font-medium">{title}</div>
          <div className="text-xs text-muted-foreground">
            {kind === 'maintenanceWindows'
              ? 'Disruptive changes (restarts) wait for a window. Empty = any time.'
              : 'No changes at all during these windows, e.g. year-end.'}
            {!list.length && inherited.length ? ` Inherits ${inherited.length} from ${c.inheritedFrom}.` : ''}
          </div>
        </div>
        <Button
          type="button"
          variant="outline"
          size="xs"
          disabled={c.disabled}
          onClick={() =>
            c.set(path, [
              ...list,
              kind === 'maintenanceWindows'
                ? { schedule: '0 2 * * 1-5', duration: '2h' }
                : {
                    start: new Date(Date.now() + 86400_000).toISOString().slice(0, 16) + ':00Z',
                    end: new Date(Date.now() + 3 * 86400_000).toISOString().slice(0, 16) + ':00Z',
                  },
            ])
          }
        >
          <PlusIcon /> Add
        </Button>
      </div>
      {c.errors[path] ? <p className="text-xs text-destructive">{c.errors[path]}</p> : null}
      {list.map((w, i) => (
        <WindowEditor
          key={i}
          w={w}
          c={c}
          path={`${path}.${i}`}
          onChange={(nw) =>
            c.set(
              path,
              list.map((x, j) => (j === i ? nw : x)),
            )
          }
          onRemove={() =>
            c.set(
              path,
              list.filter((_, j) => j !== i),
            )
          }
        />
      ))}
    </div>
  )
}

function WindowsSection(c: Ctx) {
  return (
    <FormSection
      title="Windows"
      description="When changes may happen. Cron schedules use the given IANA time zone (default UTC)."
      aside={<CalendarClockIcon className="mt-3 hidden size-8 text-muted-foreground/30 lg:block" />}
    >
      <datalist id={`${c.idp}-tz`}>
        {COMMON_TZ.map((z) => (
          <option key={z} value={z} />
        ))}
      </datalist>
      <div className="flex flex-col gap-5">
        <WindowList c={c} kind="maintenanceWindows" />
        <WindowList c={c} kind="freezeWindows" />
      </div>
    </FormSection>
  )
}

/** Compact on/off with label, for simple booleans outside the spec form. */
export function SwitchRow({
  label,
  description,
  checked,
  onChange,
  disabled,
}: {
  label: string
  description?: string
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
}) {
  const id = useId()
  return (
    <div className="flex items-start justify-between gap-4">
      <label htmlFor={id} className="min-w-0">
        <div className="text-[13px] font-medium">{label}</div>
        {description ? <div className="text-xs text-muted-foreground">{description}</div> : null}
      </label>
      <Switch id={id} checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </div>
  )
}
