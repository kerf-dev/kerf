import { CornerDownLeftIcon, FolderIcon, ServerIcon } from 'lucide-react'
import { useDeferredValue, useState } from 'react'
import { useNavigate } from 'react-router'
import { useClusters, useWorkloads } from '@/api/queries'
import { KindBadge, Savings } from '@/components/domain'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { workloadPath } from '@/lib/workload-id'
import { NAV } from './app-shell'

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const [q, setQ] = useState('')
  const dq = useDeferredValue(q.trim())
  const navigate = useNavigate()
  const workloads = useWorkloads({ q: dq, limit: 8, sort: 'savings' }, { enabled: open && dq.length > 0 })
  const clusters = useClusters()
  const go = (to: string) => {
    onOpenChange(false)
    setQ('')
    navigate(to)
  }
  const namespaces = [...new Set((workloads.data?.items ?? []).map((w) => w.namespace))]
    .filter((n) => n.includes(dq.toLowerCase()))
    .slice(0, 4)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent aria-describedby="palette-desc">
        <DialogTitle className="sr-only">Search</DialogTitle>
        <DialogDescription id="palette-desc" className="sr-only">
          Search workloads and namespaces, or jump to a page.
        </DialogDescription>
        <Command shouldFilter={false} loop>
          <CommandInput
            placeholder="Search workloads, namespaces, pages…"
            value={q}
            onValueChange={setQ}
            autoFocus
          />
          <CommandList>
            {dq && !workloads.isFetching && !workloads.data?.items.length ? (
              <CommandEmpty>No workloads match “{dq}”.</CommandEmpty>
            ) : null}
            {dq && workloads.data?.items.length ? (
              <CommandGroup
                heading={`Workloads · ${workloads.data.total} match${workloads.data.total === 1 ? '' : 'es'}`}
              >
                {workloads.data.items.map((w) => (
                  <CommandItem key={w.id} value={w.id} onSelect={() => go(workloadPath(w.id))}>
                    <KindBadge kind={w.kind} />
                    <span className="truncate font-medium">{w.name}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {w.namespace} · {w.cluster}
                    </span>
                    <span className="ml-auto text-xs">
                      <Savings value={w.monthlySavingsUSD?.total} />
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {dq && namespaces.length ? (
              <CommandGroup heading="Namespaces">
                {namespaces.map((n) => (
                  <CommandItem
                    key={n}
                    value={`ns:${n}`}
                    onSelect={() => go(`/opportunities?namespace=${encodeURIComponent(n)}`)}
                  >
                    <FolderIcon />
                    {n}
                    <span className="ml-auto text-xs text-muted-foreground">Show opportunities</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {!dq ? (
              <>
                <CommandGroup heading="Go to">
                  {NAV.map((n) => (
                    <CommandItem key={n.to} value={n.to} onSelect={() => go(n.to)}>
                      <n.icon />
                      {n.label}
                      <CornerDownLeftIcon className="ml-auto size-3 opacity-50" />
                    </CommandItem>
                  ))}
                </CommandGroup>
                {clusters.data?.items.length ? (
                  <CommandGroup heading="Clusters">
                    {clusters.data.items.map((c) => (
                      <CommandItem
                        key={c.name}
                        value={`cluster:${c.name}`}
                        onSelect={() => go(`/opportunities?cluster=${encodeURIComponent(c.name)}`)}
                      >
                        <ServerIcon />
                        {c.name}
                        <span className="ml-auto text-xs text-muted-foreground">
                          {c.counts.workloads} workloads
                        </span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                ) : null}
              </>
            ) : null}
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  )
}
