import {
  createColumnHelper,
  flexRender,
  getCoreRowModel,
  useReactTable,
  type Row,
  type SortingState,
  type VisibilityState,
} from '@tanstack/react-table'
import {
  ArrowDownIcon,
  ArrowUpDownIcon,
  ArrowUpIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  Columns3Icon,
  DownloadIcon,
  GpuIcon,
  Loader2Icon,
  SearchIcon,
  SearchXIcon,
  UsersRoundIcon,
  XIcon,
} from 'lucide-react'
import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, unwrap } from '@/api/client'
import { useTeams } from '@/api/admin'
import { useClusters, useFacets, useWorkloads } from '@/api/queries'
import {
  GPU_FILTERS,
  GPU_STATUSES,
  MODES,
  RISK_FLAGS,
  WORKLOAD_KINDS,
  type GPUFilter,
  type ListWorkloadsQuery,
  type Mode,
  type RiskFlag,
  type WorkloadKind,
  type WorkloadRow,
} from '@/api/types'
import {
  Change,
  Confidence,
  GPU_FILTER_LABEL,
  GPU_STATUS_INFO,
  GPUCell,
  KindBadge,
  MODE_INFO,
  ModePair,
  PhaseBadge,
  RISK_INFO,
  RiskFlags,
  Savings,
} from '@/components/domain'
import { FacetFilter } from '@/components/facet-filter'
import { TeamChip } from '@/components/team'
import { EmptyState, ErrorState, PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Checkbox } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { download, workloadsToCsv } from '@/lib/csv'
import { formatBytes, formatCores, formatNumber, formatUSD, relativeTime, parseQuantity } from '@/lib/format'
import { cn } from '@/lib/utils'
import { teamLabel, UNASSIGNED } from '@/lib/teams'
import { workloadPath } from '@/lib/workload-id'
import { BulkBar } from './opportunities-bulk'

interface TableMeta {
  selected: Map<string, WorkloadRow>
  toggle: (w: WorkloadRow) => void
  togglePage: (on: boolean) => void
  pageState: boolean | 'indeterminate'
}

type SortKey = 'savings' | 'confidence' | 'name'
const PAGE_SIZES = [25, 50, 100] as const

function sumRequests(w: WorkloadRow, kind: 'requests' | 'limits', resource: 'cpu' | 'memory') {
  let cur: number | undefined
  let rec: number | undefined
  for (const c of w.containers ?? []) {
    if (c.init) continue
    const a = parseQuantity(c.current[kind]?.[resource])
    const b = parseQuantity(c.recommended[kind]?.[resource])
    if (a != null) cur = (cur ?? 0) + a
    if (b != null) rec = (rec ?? 0) + b
  }
  return { cur, rec }
}

const col = createColumnHelper<WorkloadRow>()

const COLUMN_LABELS: Record<string, string> = {
  workload: 'Workload',
  team: 'Team',
  cluster: 'Cluster',
  modes: 'Mode',
  phase: 'Phase',
  cpuRequest: 'CPU request',
  memoryRequest: 'Memory request',
  cpuLimit: 'CPU limit',
  memoryLimit: 'Memory limit',
  confidence: 'Confidence',
  riskFlags: 'Risk',
  gpu: 'GPU',
  generatedAt: 'Updated',
  savings: 'Savings / mo',
}
const DEFAULT_HIDDEN: VisibilityState = {
  cluster: false,
  cpuLimit: false,
  memoryLimit: false,
  generatedAt: false,
}
/**
 * The GPU column is not in DEFAULT_HIDDEN: until the reader picks it in the Columns menu it follows the
 * data (shown while a GPU filter is active or the filtered workloads include GPU workloads).
 */
const VIS_KEY = 'ro-opportunities-columns-v1'

/**
 * Default columns depend on the width: on a tablet the table would otherwise scroll sideways from the
 * start, with the requests hidden under the pinned savings column. Only explicit choices are stored, so
 * the defaults keep following the screen until the reader picks columns.
 */
function defaultHidden(): VisibilityState {
  const w = window.innerWidth
  if (w < 1024)
    return { ...DEFAULT_HIDDEN, team: false, modes: false, phase: false, confidence: false, riskFlags: false }
  if (w < 1280) return { ...DEFAULT_HIDDEN, confidence: false, riskFlags: false }
  return DEFAULT_HIDDEN
}

function useColumnVisibility() {
  const [vis, setVisState] = useState<VisibilityState>(() => {
    try {
      return { ...defaultHidden(), ...(JSON.parse(localStorage.getItem(VIS_KEY) ?? '{}') as VisibilityState) }
    } catch {
      return defaultHidden()
    }
  })
  const setVis = useCallback((u: VisibilityState | ((prev: VisibilityState) => VisibilityState)) => {
    setVisState((prev) => {
      const next = typeof u === 'function' ? u(prev) : u
      try {
        localStorage.setItem(VIS_KEY, JSON.stringify(next))
      } catch {
        /* ignore */
      }
      return next
    })
  }, [])
  return [vis, setVis] as const
}

/** Filters, sort and page size live in the URL so views can be shared and bookmarked. */
function useUrlState() {
  const [sp, setSp] = useSearchParams()
  const state = {
    cluster: sp.getAll('cluster'),
    namespace: sp.getAll('namespace'),
    kind: sp.getAll('kind') as WorkloadKind[],
    mode: sp.getAll('mode') as Mode[],
    riskFlag: sp.getAll('riskFlag') as RiskFlag[],
    team: sp.getAll('team'),
    group: sp.get('group') === 'team' ? 'team' : '',
    gpu: sp.getAll('gpu') as GPUFilter[],
    minSavings: sp.get('minSavings') ?? '',
    q: sp.get('q') ?? '',
    sort: (sp.get('sort') as SortKey | null) ?? 'savings',
    order: (sp.get('order') as 'asc' | 'desc' | null) ?? undefined,
    size: Number(sp.get('size')) || 50,
  }
  const update = (patch: Partial<Record<keyof typeof state, string | string[] | number | undefined>>) => {
    setSp(
      (prev) => {
        const next = new URLSearchParams(prev)
        for (const [k, v] of Object.entries(patch)) {
          next.delete(k)
          if (Array.isArray(v)) v.forEach((x) => next.append(k, x))
          else if (
            v !== undefined &&
            v !== '' &&
            !(k === 'sort' && v === 'savings') &&
            !(k === 'size' && v === 50)
          )
            next.set(k, String(v))
        }
        return next
      },
      { replace: true },
    )
  }
  const toggle = (
    key: 'cluster' | 'namespace' | 'kind' | 'mode' | 'riskFlag' | 'team' | 'gpu',
    value: string,
  ) =>
    setSp(
      (prev) => {
        const next = new URLSearchParams(prev)
        const cur = next.getAll(key)
        next.delete(key)
        for (const v of cur.includes(value) ? cur.filter((x) => x !== value) : [...cur, value])
          next.append(key, v)
        return next
      },
      { replace: true },
    )
  return [state, update, toggle] as const
}

export default function OpportunitiesPage() {
  const [s, update, toggle] = useUrlState()
  const navigate = useNavigate()
  const [qInput, setQInput] = useState(s.q)
  const [minInput, setMinInput] = useState(s.minSavings)
  const [cursors, setCursors] = useState<string[]>([])
  const [exporting, setExporting] = useState(false)
  const [vis, setVis] = useColumnVisibility()
  const [selected, setSelected] = useState<Map<string, WorkloadRow>>(new Map())

  // Debounce free-text inputs into the URL.
  useEffect(() => {
    const t = setTimeout(() => qInput !== s.q && update({ q: qInput.trim() }), 250)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [qInput])
  useEffect(() => {
    const t = setTimeout(() => minInput !== s.minSavings && update({ minSavings: minInput }), 350)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [minInput])

  const filterQuery: ListWorkloadsQuery = useMemo(
    () => ({
      ...(s.cluster.length ? { cluster: s.cluster } : {}),
      ...(s.namespace.length ? { namespace: s.namespace } : {}),
      ...(s.kind.length ? { kind: s.kind } : {}),
      ...(s.mode.length ? { mode: s.mode } : {}),
      ...(s.riskFlag.length ? { riskFlag: s.riskFlag } : {}),
      ...(s.team.length ? { team: s.team } : {}),
      ...(s.gpu.length ? { gpu: s.gpu } : {}),
      ...(s.minSavings !== '' && !Number.isNaN(Number(s.minSavings))
        ? { minSavings: Number(s.minSavings) }
        : {}),
      ...(s.q ? { q: s.q } : {}),
      sort: s.sort,
      ...(s.order ? { order: s.order } : {}),
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [JSON.stringify(s)],
  )
  const filterKey = JSON.stringify(filterQuery) + s.size
  // Reset to the first page whenever the filters change.
  const [lastKey, setLastKey] = useState(filterKey)
  if (lastKey !== filterKey) {
    setLastKey(filterKey)
    setCursors([])
  }
  const cursor = cursors[cursors.length - 1]
  const query = { ...filterQuery, limit: s.size, ...(cursor ? { continue: cursor } : {}) }
  const list = useWorkloads(query)
  const clusters = useClusters()
  // Facet options with counts from GET /v1/workloads/facets (D-016). Each facet is counted without its
  // own filter, so a multi-select keeps showing the other values. Hubs without the endpoint fall back
  // to static values and the namespaces of one large page.
  const { sort: _sort, order: _order, ...facetQuery } = filterQuery
  const facets = useFacets(facetQuery)
  // Team names come from the teams list; the filter shows only once teams exist.
  const teams = useTeams()
  const hasTeams = !!teams.data?.items.length
  const fallback = useWorkloads({ limit: 500, sort: 'name' }, { enabled: facets.isError })
  const options = useMemo(() => {
    const withSelected = (counts: { value: string; count?: number }[], selected: string[]) => {
      const have = new Set(counts.map((c) => c.value))
      return [...counts, ...selected.filter((v) => !have.has(v)).map((value) => ({ value, count: 0 }))]
    }
    const f = facets.data
    if (f) {
      const count = (xs: { value: string; count: number }[]) => new Map(xs.map((x) => [x.value, x.count]))
      const kinds = count(f.kinds)
      const modes = count(f.modes)
      const flags = count(f.riskFlags)
      const gpus = count(f.gpu ?? [])
      return {
        clusters: withSelected(f.clusters, s.cluster),
        namespaces: withSelected(f.namespaces, s.namespace),
        kinds: WORKLOAD_KINDS.map((k) => ({ value: k, count: kinds.get(k) ?? 0 })),
        modes: MODES.map((m) => ({ value: m, count: modes.get(m) ?? 0 })),
        riskFlags: RISK_FLAGS.map((r) => ({ value: r, count: flags.get(r) ?? 0 })),
        teams: withSelected(f.teams ?? [], s.team),
        // `any` counts every workload with GPUs; a workload has one status, so the sum is exact.
        gpus: GPU_FILTERS.map((g) => ({
          value: g,
          count:
            g === 'any' ? GPU_STATUSES.reduce((n, st) => n + (gpus.get(st) ?? 0), 0) : (gpus.get(g) ?? 0),
        })),
      }
    }
    const ns = new Map<string, number>()
    for (const w of fallback.data?.items ?? []) ns.set(w.namespace, (ns.get(w.namespace) ?? 0) + 1)
    return {
      clusters: withSelected(
        (clusters.data?.items ?? []).map((c) => ({ value: c.name, count: c.counts.workloads })),
        s.cluster,
      ),
      namespaces: withSelected(
        [...ns.entries()]
          .sort((a, b) => a[0].localeCompare(b[0]))
          .map(([value, count]) => ({ value, count })),
        s.namespace,
      ),
      kinds: WORKLOAD_KINDS.map((k) => ({ value: k, count: undefined as number | undefined })),
      modes: MODES.map((m) => ({ value: m, count: undefined as number | undefined })),
      riskFlags: RISK_FLAGS.map((r) => ({ value: r, count: undefined as number | undefined })),
      teams: withSelected(
        [...(teams.data?.items ?? []).map((t) => t.id), ...(teams.data?.unassigned ? [UNASSIGNED] : [])].map(
          (value) => ({ value, count: undefined as number | undefined }),
        ),
        s.team,
      ),
      gpus: GPU_FILTERS.map((g) => ({ value: g, count: undefined as number | undefined })),
    }
  }, [facets.data, fallback.data, clusters.data, teams.data, s.cluster, s.namespace, s.team])
  const teamOptions = useMemo(
    () => options.teams.map((o) => ({ ...o, label: teamLabel(o.value, teams.data?.items) })),
    [options.teams, teams.data],
  )
  const showTeams = hasTeams || s.team.length > 0 || teamOptions.some((o) => o.value !== UNASSIGNED)

  const sorting: SortingState = [
    { id: s.sort, desc: (s.order ?? (s.sort === 'name' ? 'asc' : 'desc')) === 'desc' },
  ]

  const columns = useMemo(
    () => [
      col.display({
        id: 'workload',
        header: ({ table }) => {
          const m = table.options.meta as TableMeta
          return (
            <span className="inline-flex items-center gap-3">
              <Checkbox
                checked={m.pageState}
                onCheckedChange={(v) => m.togglePage(v === true)}
                aria-label="Select all on this page"
                data-testid="select-page"
              />
              Workload
            </span>
          )
        },
        enableSorting: true,
        meta: { sortKey: 'name' },
        cell: ({ row: { original: w }, table }) => {
          const m = table.options.meta as TableMeta
          return (
            <div className="flex min-w-0 items-center gap-3 py-0.5">
              <Checkbox
                checked={m.selected.has(w.id)}
                onCheckedChange={() => m.toggle(w)}
                onClick={(e) => e.stopPropagation()}
                onKeyDown={(e) => e.stopPropagation()}
                aria-label={`Select ${w.name}`}
                data-testid="select-row"
              />
              <div className="min-w-0">
                <div className="flex items-center gap-1.5">
                  <Link
                    to={workloadPath(w.id)}
                    tabIndex={-1}
                    onClick={(e) => e.stopPropagation()}
                    className="truncate font-medium hover:underline"
                  >
                    {w.name}
                  </Link>
                  <KindBadge kind={w.kind} />
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {w.namespace} · {w.cluster}
                </div>
              </div>
            </div>
          )
        },
      }),
      col.display({
        id: 'team',
        header: 'Team',
        cell: ({ row: { original: w } }) =>
          w.team ? <TeamChip team={w.team} /> : <span className="text-muted-foreground/60">—</span>,
      }),
      col.accessor('cluster', {
        header: 'Cluster',
        enableSorting: false,
        cell: (c) => <span className="text-xs whitespace-nowrap text-muted-foreground">{c.getValue()}</span>,
      }),
      col.display({
        id: 'modes',
        header: 'Mode',
        cell: ({ row: { original: w } }) => (
          <ModePair
            cpu={w.modes.cpu}
            memory={w.modes.memory}
            cpuSource={w.modes.cpuSource}
            memorySource={w.modes.memorySource}
          />
        ),
      }),
      col.accessor('phase', {
        header: 'Phase',
        enableSorting: false,
        cell: (c) => <PhaseBadge phase={c.getValue()} />,
      }),
      col.display({
        id: 'cpuRequest',
        header: 'CPU request',
        cell: ({ row: { original: w } }) => {
          if (!w.containers) return <Pending />
          const { cur, rec } = sumRequests(w, 'requests', 'cpu')
          return <Change from={cur} to={rec} format={formatCores} />
        },
      }),
      col.display({
        id: 'memoryRequest',
        header: 'Memory request',
        cell: ({ row: { original: w } }) => {
          if (!w.containers) return <Pending />
          const { cur, rec } = sumRequests(w, 'requests', 'memory')
          return <Change from={cur} to={rec} format={(v) => formatBytes(v)} />
        },
      }),
      col.display({
        id: 'cpuLimit',
        header: 'CPU limit',
        cell: ({ row: { original: w } }) => {
          if (!w.containers) return <Pending />
          const { cur, rec } = sumRequests(w, 'limits', 'cpu')
          if (cur == null && rec == null) return <span className="text-xs text-muted-foreground">none</span>
          return <Change from={cur} to={rec} format={(v) => (v == null ? 'none' : formatCores(v))} />
        },
      }),
      col.display({
        id: 'memoryLimit',
        header: 'Memory limit',
        cell: ({ row: { original: w } }) => {
          if (!w.containers) return <Pending />
          const { cur, rec } = sumRequests(w, 'limits', 'memory')
          return <Change from={cur} to={rec} format={(v) => (v == null ? 'none' : formatBytes(v))} />
        },
      }),
      col.accessor('confidence', {
        header: 'Confidence',
        enableSorting: true,
        meta: { sortKey: 'confidence' },
        cell: (c) => <Confidence value={c.getValue()} />,
      }),
      col.accessor('riskFlags', {
        header: 'Risk',
        enableSorting: false,
        cell: (c) => <RiskFlags flags={c.getValue()?.filter((f) => f !== 'sidecar')} max={3} compact />,
      }),
      col.accessor('gpu', {
        header: 'GPU',
        enableSorting: false,
        cell: (c) => <GPUCell gpu={c.getValue()} />,
      }),
      col.accessor('generatedAt', {
        header: 'Updated',
        enableSorting: false,
        cell: (c) => (
          <span className="text-xs whitespace-nowrap text-muted-foreground">
            {relativeTime(c.getValue())}
          </span>
        ),
      }),
      col.accessor((w) => w.monthlySavingsUSD?.total, {
        id: 'savings',
        header: 'Savings / mo',
        enableSorting: true,
        meta: { sortKey: 'savings', align: 'right' },
        cell: (c) => <Savings value={c.getValue()} />,
      }),
    ],
    [],
  )

  // The GPU column follows the data until the reader decides: shown while a GPU filter is active or the
  // filtered workloads include GPU workloads (from the facets, so paging does not make it flicker).
  const gpuAuto =
    s.gpu.length > 0 ||
    (facets.data ? (facets.data.gpu ?? []).length > 0 : (list.data?.items ?? []).some((w) => w.gpu))
  const columnVisibility = useMemo(() => ({ ...vis, gpu: vis.gpu ?? gpuAuto }), [vis, gpuAuto])
  const onColumnVisibilityChange = useCallback(
    (u: VisibilityState | ((prev: VisibilityState) => VisibilityState)) =>
      setVis((prev) => {
        const next = typeof u === 'function' ? u({ ...prev, gpu: prev.gpu ?? gpuAuto }) : u
        // Only an explicit choice for the GPU column is stored.
        const { gpu, ...rest } = next
        return gpu !== undefined && (prev.gpu !== undefined || gpu !== gpuAuto) ? { ...rest, gpu } : rest
      }),
    [setVis, gpuAuto],
  )

  const table = useReactTable({
    data: list.data?.items ?? [],
    columns,
    state: { sorting, columnVisibility: showTeams ? columnVisibility : { ...columnVisibility, team: false } },
    onColumnVisibilityChange,
    manualSorting: true,
    manualPagination: true,
    manualFiltering: true,
    enableSortingRemoval: false,
    getCoreRowModel: getCoreRowModel(),
    getRowId: (r) => r.id,
    meta: {
      selected,
      toggle: (w: WorkloadRow) =>
        setSelected((m) => {
          const n = new Map(m)
          if (n.has(w.id)) n.delete(w.id)
          else n.set(w.id, w)
          return n
        }),
      togglePage: (on: boolean) =>
        setSelected((m) => {
          const n = new Map(m)
          for (const w of list.data?.items ?? []) {
            if (on) n.set(w.id, w)
            else n.delete(w.id)
          }
          return n
        }),
      pageState: (() => {
        const page = list.data?.items ?? []
        const k = page.filter((w) => selected.has(w.id)).length
        return k === 0 ? false : k === page.length ? true : 'indeterminate'
      })(),
    } satisfies TableMeta,
  })

  const activeFilters =
    s.cluster.length +
    s.namespace.length +
    s.kind.length +
    s.mode.length +
    s.riskFlag.length +
    s.team.length +
    s.gpu.length +
    (s.minSavings ? 1 : 0) +
    (s.q ? 1 : 0)
  const clearAll = () => {
    setQInput('')
    setMinInput('')
    update({
      cluster: [],
      namespace: [],
      kind: [],
      mode: [],
      riskFlag: [],
      team: [],
      gpu: [],
      minSavings: undefined,
      q: undefined,
    })
  }

  const toggleSort = (key: SortKey) => {
    if (s.sort === key) {
      const cur = s.order ?? (key === 'name' ? 'asc' : 'desc')
      update({ sort: key, order: cur === 'asc' ? 'desc' : 'asc' })
    } else update({ sort: key, order: undefined })
  }

  const exportCsv = async () => {
    setExporting(true)
    try {
      const all: WorkloadRow[] = []
      let cont: string | undefined
      do {
        const page = await unwrap(
          api.GET('/v1/workloads', {
            params: { query: { ...filterQuery, limit: 500, ...(cont ? { continue: cont } : {}) } },
          }),
        )
        all.push(...page.items)
        cont = page.continue
      } while (cont && all.length < 20_000)
      download(`rightsizing-opportunities-${new Date().toISOString().slice(0, 10)}.csv`, workloadsToCsv(all))
    } finally {
      setExporting(false)
    }
  }

  const renderRow = (row: Row<WorkloadRow>) => (
    <tr
      key={row.id}
      tabIndex={0}
      data-testid="opportunity-row"
      onClick={() => navigate(workloadPath(row.original.id))}
      onKeyDown={(e) => {
        if (e.key === 'Enter') navigate(workloadPath(row.original.id))
      }}
      className="group cursor-pointer border-b border-border/60 transition-colors outline-none last:border-0 hover:bg-accent/50 focus-visible:bg-accent/70 focus-visible:shadow-[inset_2px_0_0_var(--primary)]"
    >
      {row.getVisibleCells().map((cell) => {
        const meta = cell.column.columnDef.meta as { align?: 'right' } | undefined
        return (
          <td
            key={cell.id}
            className={cn(
              'h-14 max-w-72 px-2.5 first:pl-4 last:pr-4',
              // Workload and savings stay visible while the table scrolls sideways.
              'first:sticky first:left-0 first:z-[1] first:bg-card last:sticky last:right-0 last:z-[1] last:bg-card group-hover:first:bg-[color-mix(in_oklch,var(--accent)_50%,var(--card))] group-hover:last:bg-[color-mix(in_oklch,var(--accent)_50%,var(--card))]',
              meta?.align === 'right' && 'text-right',
            )}
          >
            {flexRender(cell.column.columnDef.cell, cell.getContext())}
          </td>
        )
      })}
    </tr>
  )
  // "Group by team": the rows of the page under one header per team, biggest savings first.
  const groups = (() => {
    if (s.group !== 'team') return undefined
    const by = new Map<string, { team: string; rows: Row<WorkloadRow>[]; savings: number }>()
    for (const row of table.getRowModel().rows) {
      const team = row.original.team ?? UNASSIGNED
      const g = by.get(team) ?? { team, rows: [], savings: 0 }
      g.rows.push(row)
      g.savings += row.original.monthlySavingsUSD?.total ?? 0
      by.set(team, g)
    }
    return [...by.values()].sort(
      (a, b) => Number(a.team === UNASSIGNED) - Number(b.team === UNASSIGNED) || b.savings - a.savings,
    )
  })()

  const total = list.data?.total ?? 0
  const pageStart = cursors.length * s.size
  const items = list.data?.items ?? []

  return (
    <div className="space-y-5">
      <PageHeader
        title="Opportunities"
        description="Every workload with its latest recommendation. Filter, sort and open a row to see why."
        actions={
          <>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm">
                  <Columns3Icon /> Columns
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent className="w-48">
                <DropdownMenuLabel>Visible columns</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {table
                  .getAllLeafColumns()
                  .filter((c) => c.id !== 'workload' && (c.id !== 'team' || showTeams))
                  .map((c) => (
                    <DropdownMenuCheckboxItem
                      key={c.id}
                      checked={c.getIsVisible()}
                      onCheckedChange={(v) => c.toggleVisibility(!!v)}
                      onSelect={(e) => e.preventDefault()}
                    >
                      {COLUMN_LABELS[c.id] ?? c.id}
                    </DropdownMenuCheckboxItem>
                  ))}
              </DropdownMenuContent>
            </DropdownMenu>
            <Button variant="outline" size="sm" onClick={exportCsv} disabled={exporting || !total}>
              {exporting ? <Loader2Icon className="animate-spin" /> : <DownloadIcon />} Export CSV
            </Button>
          </>
        }
      />

      <div className="flex flex-wrap items-center gap-2" role="search" aria-label="Filter workloads">
        <div className="relative w-full sm:w-64">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={qInput}
            onChange={(e) => setQInput(e.target.value)}
            placeholder="Search name or namespace"
            className="pl-8"
            aria-label="Search workloads"
          />
        </div>
        <FacetFilter
          title="Cluster"
          options={options.clusters}
          selected={s.cluster}
          onToggle={(v) => toggle('cluster', v)}
          onClear={() => update({ cluster: [] })}
        />
        {showTeams ? (
          <FacetFilter
            title="Team"
            options={teamOptions}
            selected={s.team}
            onToggle={(v) => toggle('team', v)}
            onClear={() => update({ team: [] })}
          />
        ) : null}
        <FacetFilter
          title="Namespace"
          options={options.namespaces}
          selected={s.namespace}
          onToggle={(v) => toggle('namespace', v)}
          onClear={() => update({ namespace: [] })}
          searchable
        />
        <FacetFilter
          title="Kind"
          options={options.kinds}
          selected={s.kind}
          onToggle={(v) => toggle('kind', v)}
          onClear={() => update({ kind: [] })}
        />
        <FacetFilter
          title="Mode"
          options={options.modes.map((m) => ({ ...m, label: m.value, icon: MODE_INFO[m.value].icon }))}
          selected={s.mode}
          onToggle={(v) => toggle('mode', v)}
          onClear={() => update({ mode: [] })}
        />
        <FacetFilter
          title="Risk"
          options={options.riskFlags.map((f) => ({
            ...f,
            label: RISK_INFO[f.value].label,
            icon: RISK_INFO[f.value].icon,
          }))}
          selected={s.riskFlag}
          onToggle={(v) => toggle('riskFlag', v)}
          onClear={() => update({ riskFlag: [] })}
        />
        <FacetFilter
          title="GPU"
          options={options.gpus.map((g) => ({
            ...g,
            label: GPU_FILTER_LABEL[g.value],
            icon: g.value === 'any' ? GpuIcon : GPU_STATUS_INFO[g.value].icon,
          }))}
          selected={s.gpu}
          onToggle={(v) => toggle('gpu', v)}
          onClear={() => update({ gpu: [] })}
        />
        <div className="relative">
          <span className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-xs text-muted-foreground">
            min $
          </span>
          <Input
            type="number"
            min={0}
            inputMode="numeric"
            value={minInput}
            onChange={(e) => setMinInput(e.target.value)}
            placeholder="0"
            className="h-8 w-28 pl-11 tabular"
            aria-label="Minimum monthly savings in USD"
          />
        </div>
        {showTeams ? (
          <label className="flex h-8 items-center gap-2 rounded-lg border px-2.5 text-[13px]">
            <Checkbox
              checked={s.group === 'team'}
              onCheckedChange={(v) => update({ group: v === true ? 'team' : undefined })}
              aria-label="Group by team"
              data-testid="group-by-team"
            />
            <UsersRoundIcon className="size-3.5 text-muted-foreground" />
            Group by team
          </label>
        ) : null}
        {activeFilters ? (
          <Button variant="ghost" size="sm" onClick={clearAll} className="text-muted-foreground">
            Reset <XIcon />
          </Button>
        ) : null}
      </div>

      <Card className="overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b px-4 py-2.5 text-[13px]">
          <div className="text-muted-foreground" aria-live="polite">
            {list.data ? (
              <>
                <span className="font-semibold text-foreground tabular">{formatNumber(total)}</span> workload
                {total === 1 ? '' : 's'}
                {activeFilters ? ' match your filters' : ''}
              </>
            ) : list.isError ? (
              'Workloads unavailable'
            ) : (
              <Skeleton className="h-4 w-32" />
            )}
          </div>
          {list.isFetching && list.data ? (
            <Loader2Icon className="size-4 animate-spin text-muted-foreground" aria-label="Updating" />
          ) : null}
        </div>

        {list.isError ? (
          <ErrorState error={list.error} onRetry={() => list.refetch()} />
        ) : (
          <div className="relative overflow-x-auto scrollbar-thin">
            <table
              className="w-full min-w-[880px] text-sm"
              data-testid="opportunities-table"
              data-loading={list.isFetching ? 'true' : 'false'}
            >
              <thead className="sticky top-0 z-10 bg-card">
                {table.getHeaderGroups().map((hg) => (
                  <tr key={hg.id} className="border-b">
                    {hg.headers.map((h) => {
                      const meta = h.column.columnDef.meta as
                        { sortKey?: SortKey; align?: 'right' } | undefined
                      const sortKey = meta?.sortKey
                      const active = sortKey && s.sort === sortKey
                      const dir = active ? (s.order ?? (sortKey === 'name' ? 'asc' : 'desc')) : undefined
                      return (
                        <th
                          key={h.id}
                          scope="col"
                          aria-sort={active ? (dir === 'asc' ? 'ascending' : 'descending') : undefined}
                          className={cn(
                            'h-10 bg-card px-2.5 text-left text-[11px] font-medium tracking-wide whitespace-nowrap text-muted-foreground uppercase first:sticky first:left-0 first:z-[2] first:pl-4 last:sticky last:right-0 last:z-[2] last:pr-4',
                            meta?.align === 'right' && 'text-right',
                          )}
                        >
                          {sortKey ? (
                            <button
                              onClick={() => toggleSort(sortKey)}
                              className={cn(
                                '-mx-1.5 inline-flex items-center gap-1 rounded px-1.5 py-1 uppercase outline-none hover:bg-accent hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/40',
                                active && 'text-foreground',
                              )}
                            >
                              {flexRender(h.column.columnDef.header, h.getContext())}
                              {active ? (
                                dir === 'asc' ? (
                                  <ArrowUpIcon className="size-3" />
                                ) : (
                                  <ArrowDownIcon className="size-3" />
                                )
                              ) : (
                                <ArrowUpDownIcon className="size-3 opacity-40" />
                              )}
                            </button>
                          ) : (
                            flexRender(h.column.columnDef.header, h.getContext())
                          )}
                        </th>
                      )
                    })}
                  </tr>
                ))}
              </thead>
              <tbody className={cn(list.isPlaceholderData && 'opacity-60 transition-opacity')}>
                {!list.data
                  ? Array.from({ length: 10 }, (_, i) => (
                      <tr key={i} className="border-b border-border/60">
                        {table.getVisibleLeafColumns().map((c) => (
                          <td key={c.id} className="h-14 px-3 first:pl-4 last:pr-4">
                            <Skeleton className={cn('h-4', c.id === 'workload' ? 'w-40' : 'w-20')} />
                          </td>
                        ))}
                      </tr>
                    ))
                  : groups
                    ? groups.map((g) => (
                        <Fragment key={g.team}>
                          <tr className="border-b bg-muted/40" data-testid="team-group">
                            <th
                              scope="rowgroup"
                              colSpan={table.getVisibleLeafColumns().length}
                              className="h-9 px-4 text-left text-[13px] font-medium"
                            >
                              {teamLabel(g.team, teams.data?.items)}
                              <span className="ml-2 font-normal text-muted-foreground">
                                {formatNumber(g.rows.length)} on this page · {formatUSD(g.savings)}/mo
                              </span>
                            </th>
                          </tr>
                          {g.rows.map(renderRow)}
                        </Fragment>
                      ))
                    : table.getRowModel().rows.map(renderRow)}
              </tbody>
            </table>
            {list.data && !items.length ? (
              <EmptyState
                icon={SearchXIcon}
                title="No workloads match these filters"
                description="Try removing a filter or lowering the minimum savings."
                action={
                  activeFilters ? (
                    <Button variant="outline" size="sm" onClick={clearAll}>
                      Clear all filters
                    </Button>
                  ) : undefined
                }
              />
            ) : null}
          </div>
        )}

        {list.data && total > 0 ? (
          <div className="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-2.5 text-[13px] text-muted-foreground">
            <div className="flex items-center gap-2">
              Rows per page
              <select
                value={s.size}
                onChange={(e) => update({ size: Number(e.target.value) })}
                className="h-7 rounded-md border bg-card px-1.5 text-[13px] text-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40"
                aria-label="Rows per page"
              >
                {PAGE_SIZES.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </div>
            <div className="flex items-center gap-3">
              <span className="tabular">
                {formatNumber(pageStart + 1)}–{formatNumber(pageStart + items.length)} of{' '}
                {formatNumber(total)}
              </span>
              <div className="flex gap-1">
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Previous page"
                  disabled={!cursors.length}
                  onClick={() => setCursors((c) => c.slice(0, -1))}
                >
                  <ChevronLeftIcon />
                </Button>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Next page"
                  disabled={!list.data.continue || list.isPlaceholderData}
                  onClick={() => setCursors((c) => [...c, list.data!.continue!])}
                >
                  <ChevronRightIcon />
                </Button>
              </div>
            </div>
          </div>
        ) : null}
      </Card>
      <BulkBar selected={selected} onClear={() => setSelected(new Map())} />
    </div>
  )
}

function Pending() {
  return <span className="text-xs text-muted-foreground italic">collecting data…</span>
}
