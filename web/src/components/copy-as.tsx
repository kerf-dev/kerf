import { ChevronDownIcon, CopyIcon, EyeIcon } from 'lucide-react'
import { Fragment, useMemo, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Segmented } from '@/components/ui/segmented'
import { toast } from '@/components/ui/toast'
import {
  formatPatches,
  hasPatch,
  PATCH_FORMATS,
  type PatchFormat,
  type PatchWorkload,
} from '@/lib/patch/patch'

type Language = (typeof PATCH_FORMATS)[number]['language']

/** Copies text; resolves false where the browser refuses (no secure context, no permission). */
async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

function summary(n: number, skipped: number): string {
  const what = n === 1 ? '1 workload' : `${n} workloads`
  return skipped ? `${what} (${skipped} without a recommendation skipped)` : what
}

/**
 * "Copy as…" for one workload or a selection: the recommended values as a strategic merge patch, a
 * `kubectl patch` command, a Kustomize patch or Helm values, ready to paste into a pull request.
 * Everything runs in the browser from values the page already has.
 */
export function CopyAsMenu({
  workloads,
  size = 'sm',
  variant = 'outline',
  testId = 'copy-as',
}: {
  workloads: PatchWorkload[]
  size?: 'sm' | 'default'
  variant?: 'outline' | 'ghost'
  testId?: string
}) {
  const usable = useMemo(() => workloads.filter(hasPatch), [workloads])
  const skipped = workloads.length - usable.length
  const [preview, setPreview] = useState<PatchFormat | null>(null)

  const copy = async (format: PatchFormat) => {
    const text = formatPatches(format, usable)
    const label = PATCH_FORMATS.find((f) => f.id === format)!.label
    if (await copyText(text)) toast.success(`Copied as ${label}`, summary(usable.length, skipped))
    else {
      toast.error('Could not copy to the clipboard', 'Select the text in the preview and copy it by hand.')
      setPreview(format)
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size={size} variant={variant} disabled={!usable.length} data-testid={testId}>
            <CopyIcon /> Copy as…
            <ChevronDownIcon className="opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent className="min-w-64" data-testid={`${testId}-menu`}>
          <DropdownMenuLabel>
            Recommended values for {usable.length === 1 ? '1 workload' : `${usable.length} workloads`}
          </DropdownMenuLabel>
          {PATCH_FORMATS.map((f) => (
            <DropdownMenuItem key={f.id} onSelect={() => void copy(f.id)} data-testid={`${testId}-${f.id}`}>
              <span className="flex flex-col">
                <span>{f.label}</span>
                <span className="text-xs text-muted-foreground">{f.hint}</span>
              </span>
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setPreview('patch')} data-testid={`${testId}-preview`}>
            <EyeIcon /> Preview…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <PatchPreview
        open={!!preview}
        onOpenChange={(o) => !o && setPreview(null)}
        initial={preview ?? 'patch'}
        workloads={usable}
        skipped={skipped}
      />
    </>
  )
}

function PatchPreview({
  open,
  onOpenChange,
  initial,
  workloads,
  skipped,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  initial: PatchFormat
  workloads: PatchWorkload[]
  skipped: number
}) {
  const [chosen, setChosen] = useState<PatchFormat | null>(null)
  const format = chosen ?? initial
  const meta = PATCH_FORMATS.find((f) => f.id === format)!
  const text = useMemo(() => (open ? formatPatches(format, workloads) : ''), [open, format, workloads])

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) setChosen(null)
        onOpenChange(o)
      }}
    >
      <DialogContent className="top-[8%] w-[min(820px,calc(100vw-2rem))]" data-testid="copy-as-dialog">
        <div className="space-y-3 border-b px-5 py-4">
          <div>
            <DialogTitle className="text-base font-semibold">Copy as patch</DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px] text-muted-foreground">
              {summary(workloads.length, skipped)}: only the resources Kerf recommends, with each container
              matched by name. Apply it through your usual pull request.
            </DialogDescription>
          </div>
          <Segmented
            label="Format"
            value={format}
            onChange={setChosen}
            options={PATCH_FORMATS.map((f) => ({ value: f.id, label: f.label }))}
            className="h-auto max-w-full flex-wrap"
          />
        </div>
        <pre
          tabIndex={0}
          aria-label={`${meta.label} output`}
          data-testid="copy-as-output"
          className="max-h-[52vh] overflow-auto bg-muted/30 px-5 py-4 font-mono text-xs leading-relaxed whitespace-pre outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:ring-inset"
        >
          <code>
            {text.split('\n').map((line, i, all) => (
              <Fragment key={i}>
                <Highlighted line={line} language={meta.language} />
                {i < all.length - 1 ? '\n' : null}
              </Fragment>
            ))}
          </code>
        </pre>
        <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
          <Button type="button" variant="ghost" size="sm" onClick={() => onOpenChange(false)}>
            Close
          </Button>
          <Button
            type="button"
            size="sm"
            data-testid="copy-as-copy"
            onClick={async () => {
              if (await copyText(text))
                toast.success(`Copied as ${meta.label}`, summary(workloads.length, skipped))
              else
                toast.error('Could not copy to the clipboard', 'Select the text above and copy it by hand.')
            }}
          >
            <CopyIcon /> Copy
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// A line-based highlighter for the two shapes we emit (YAML and one-line shell). The bundle has no
// syntax highlighter; this uses the theme tokens, so it follows light and dark mode.

const COMMENT = 'text-muted-foreground italic'
const KEY = 'text-info'
const STRING = 'text-success'
const FLAG = 'text-warning'

function Highlighted({ line, language }: { line: string; language: Language }) {
  if (/^\s*#/.test(line)) return <span className={COMMENT}>{line}</span>
  if (line === '---') return <span className="text-muted-foreground">{line}</span>
  if (language === 'shell') return <Shell line={line} />
  const m = /^(\s*(?:- )?)([^\s:#][^:]*?)(:)(?:( +)(.*))?$/.exec(line)
  if (!m) return <>{line}</>
  const [, lead, key, colon, gap, value] = m
  return (
    <>
      {lead}
      <span className={KEY}>{key}</span>
      {colon}
      {gap}
      {value ? <span className={/^[|>]/.test(value) ? 'text-muted-foreground' : STRING}>{value}</span> : null}
    </>
  )
}

function Shell({ line }: { line: string }) {
  const parts = line.split(/('(?:[^']|'\\'')*')/)
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith("'") ? (
          <span key={i} className={STRING}>
            {p}
          </span>
        ) : (
          <Fragment key={i}>
            {p.split(/(\s--[a-z-]+|^kubectl)/).map((q, j) =>
              /^\s?--|^kubectl$/.test(q) ? (
                <span key={j} className={q === 'kubectl' ? KEY : FLAG}>
                  {q}
                </span>
              ) : (
                <Fragment key={j}>{q}</Fragment>
              ),
            )}
          </Fragment>
        ),
      )}
    </>
  )
}
