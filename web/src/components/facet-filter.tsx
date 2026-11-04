import { CheckIcon, PlusCircleIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

export interface FacetOption {
  value: string
  label?: React.ReactNode
  icon?: React.ComponentType<{ className?: string }>
  count?: number
}

/** Multi-select filter button in the style of shadcn's data-table faceted filter. */
export function FacetFilter({
  title,
  options,
  selected,
  onToggle,
  onClear,
  searchable = options.length > 8,
}: {
  title: string
  options: FacetOption[]
  selected: string[]
  /** Toggles one value; implemented against the latest state so quick successive clicks compose. */
  onToggle: (v: string) => void
  onClear: () => void
  searchable?: boolean
}) {
  const set = new Set(selected)
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className={cn('border-dashed', set.size && 'border-solid')}>
          <PlusCircleIcon className={cn(set.size && 'hidden')} />
          {title}
          {set.size ? (
            <>
              <span className="mx-0.5 h-4 w-px bg-border" />
              {set.size <= 2 ? (
                selected.map((s) => (
                  <span key={s} className="rounded bg-secondary px-1.5 py-0.5 text-[11px] font-medium">
                    {options.find((o) => o.value === s)?.label ?? s}
                  </span>
                ))
              ) : (
                <span className="rounded bg-secondary px-1.5 py-0.5 text-[11px] font-medium">
                  {set.size} selected
                </span>
              )}
            </>
          ) : null}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-60 p-0">
        <Command>
          {searchable ? <CommandInput placeholder={title} /> : null}
          <CommandList>
            <CommandEmpty>No results.</CommandEmpty>
            <CommandGroup>
              {options.map((o) => {
                const on = set.has(o.value)
                return (
                  <CommandItem
                    key={o.value}
                    value={o.value}
                    onSelect={() => onToggle(o.value)}
                    className={cn(o.count === 0 && !on && 'opacity-50')}
                  >
                    <span
                      className={cn(
                        'flex size-4 items-center justify-center rounded-[4px] border',
                        on ? 'border-primary bg-primary text-primary-foreground' : 'border-input',
                      )}
                    >
                      <CheckIcon className={cn('size-3 text-current', !on && 'invisible')} />
                    </span>
                    {o.icon ? <o.icon className="size-3.5 text-muted-foreground" /> : null}
                    <span className="truncate">{o.label ?? o.value}</span>
                    {o.count != null ? (
                      <span className="ml-auto font-mono text-[11px] text-muted-foreground">{o.count}</span>
                    ) : null}
                  </CommandItem>
                )
              })}
            </CommandGroup>
            {set.size ? (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem onSelect={onClear} className="justify-center text-center">
                    Clear filter
                  </CommandItem>
                </CommandGroup>
              </>
            ) : null}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
