import { cn } from '@/lib/utils'

// The Kerf mark: a tile with one corner trimmed off along a saw cut, the
// trimmed piece set apart by the width of the cut. Same geometry as docs/branding/kerf-mark-*.svg.
const BODY = 'M12 4H29L60 35V52A12 12 0 0 1 48 64H12A12 12 0 0 1 0 52V16A12 12 0 0 1 12 4Z'
const TRIM = 'M33 0H52A12 12 0 0 1 64 12V31Z'

export function LogoMark({ size = 'md' }: { size?: 'md' | 'lg' }) {
  return (
    <svg viewBox="0 0 64 64" className={cn('shrink-0', size === 'lg' ? 'size-12' : 'size-8')} aria-hidden>
      <path d={BODY} className="fill-primary" />
      <path d={TRIM} className="fill-[#5cc8b6] dark:fill-[#9ae6d8]" />
    </svg>
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
