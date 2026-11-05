import { cn } from '@/lib/utils'

export function LogoMark({ size = 'md' }: { size?: 'md' | 'lg' }) {
  return (
    <div
      className={cn(
        'relative flex shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-primary to-[color-mix(in_oklch,var(--primary)_60%,var(--chart-2))] shadow-sm ring-1 ring-black/5',
        size === 'lg' ? 'size-12 rounded-xl' : 'size-8',
      )}
    >
      <svg viewBox="0 0 32 32" className={size === 'lg' ? 'size-7' : 'size-5'} aria-hidden>
        <path
          d="M7 22 13 14.5l4.5 4 7.5-9"
          fill="none"
          stroke="white"
          strokeWidth="2.8"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        <circle cx="25" cy="9.5" r="2.2" fill="white" />
      </svg>
    </div>
  )
}

export function Logo({ size = 'md' }: { size?: 'md' | 'lg' }) {
  if (size === 'lg') return <LogoMark size="lg" />
  return (
    <div className="flex items-center gap-2.5">
      <LogoMark />
      <div className="hidden leading-tight lg:block">
        <div className="text-[13px] font-semibold tracking-tight">Kerf</div>
        <div className="text-[11px] text-muted-foreground">Kubernetes rightsizing</div>
      </div>
    </div>
  )
}
