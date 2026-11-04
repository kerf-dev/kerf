import { CheckCircle2Icon, InfoIcon, XCircleIcon, XIcon } from 'lucide-react'
import { useSyncExternalStore } from 'react'
import { cn } from '@/lib/utils'

/** Minimal toaster: `toast.success('Saved')`. Rendered once by <Toaster/> in the app shell. */
interface ToastItem {
  id: number
  tone: 'success' | 'error' | 'info'
  title: string
  description?: string
}

let items: ToastItem[] = []
let seq = 0
const listeners = new Set<() => void>()
const emit = () => listeners.forEach((l) => l())

function push(tone: ToastItem['tone'], title: string, description?: string) {
  const id = ++seq
  items = [...items.slice(-3), { id, tone, title, description }]
  emit()
  setTimeout(() => dismiss(id), tone === 'error' ? 8000 : 4500)
}
function dismiss(id: number) {
  items = items.filter((t) => t.id !== id)
  emit()
}

export const toast = {
  success: (title: string, description?: string) => push('success', title, description),
  error: (title: string, description?: string) => push('error', title, description),
  info: (title: string, description?: string) => push('info', title, description),
}

export function Toaster() {
  const list = useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => items,
  )
  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(380px,calc(100vw-2rem))] flex-col gap-2"
    >
      {list.map((t) => {
        const Icon = t.tone === 'success' ? CheckCircle2Icon : t.tone === 'error' ? XCircleIcon : InfoIcon
        return (
          <div
            key={t.id}
            role={t.tone === 'error' ? 'alert' : 'status'}
            data-testid="toast"
            className="pointer-events-auto flex items-start gap-3 rounded-xl border bg-popover px-3.5 py-3 text-popover-foreground shadow-lg animate-in fade-in-0 slide-in-from-bottom-2"
          >
            <Icon
              className={cn(
                'mt-0.5 size-4 shrink-0',
                t.tone === 'success' ? 'text-success' : t.tone === 'error' ? 'text-destructive' : 'text-info',
              )}
            />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium">{t.title}</div>
              {t.description ? (
                <div className="mt-0.5 text-xs text-pretty text-muted-foreground">{t.description}</div>
              ) : null}
            </div>
            <button
              onClick={() => dismiss(t.id)}
              aria-label="Dismiss"
              className="-m-1 rounded p-1 text-muted-foreground hover:text-foreground"
            >
              <XIcon className="size-3.5" />
            </button>
          </div>
        )
      })}
    </div>
  )
}
