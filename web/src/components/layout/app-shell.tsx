import {
  ActivityIcon,
  BadgeDollarSignIcon,
  BellIcon,
  CommandIcon,
  DatabaseIcon,
  FlaskConicalIcon,
  GaugeIcon,
  HistoryIcon,
  KeyRoundIcon,
  LayoutDashboardIcon,
  Loader2Icon,
  LogOutIcon,
  MonitorIcon,
  MoonIcon,
  PiggyBankIcon,
  RotateCwIcon,
  ScrollTextIcon,
  SearchIcon,
  ServerIcon,
  SlidersHorizontalIcon,
  SparklesIcon,
  SunIcon,
  TrendingDownIcon,
  UsersIcon,
  WrenchIcon,
} from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { Link, Navigate, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { loginPath, ME_KEY, useAuth, useLogout, type Auth } from '@/api/auth'
import { useSummary } from '@/api/queries'
import type { UserRole } from '@/api/types'
import { RoleBadge } from '@/components/admin/common'
import { ChangePasswordDialog } from '@/components/layout/change-password'
import { Toaster } from '@/components/ui/toast'
import { useTheme, type Theme } from '@/components/theme-provider'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tooltip } from '@/components/ui/tooltip'
import { formatDateTime, formatNumber, formatUSD, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { CommandPalette } from './command-palette'
import { Logo } from './logo'

interface NavItem {
  to: string
  label: string
  icon: typeof ActivityIcon
  end?: boolean
  /** Lowest role that sees the item (signed-in users only). */
  min?: UserRole
}

export const NAV: NavItem[] = [
  { to: '/', label: 'Overview', icon: LayoutDashboardIcon, end: true },
  { to: '/opportunities', label: 'Opportunities', icon: TrendingDownIcon },
  { to: '/clusters', label: 'Clusters', icon: ServerIcon },
  { to: '/activity', label: 'Activity', icon: ActivityIcon },
]

/** The admin area (plan §4): operators see automation, policies, audit, notifications and maintenance; the rest is admin-only. */
export const ADMIN_NAV: NavItem[] = [
  { to: '/admin', label: 'Automation', icon: GaugeIcon, end: true, min: 'operator' },
  { to: '/admin/policies', label: 'Policies', icon: ScrollTextIcon, min: 'operator' },
  { to: '/admin/defaults', label: 'Defaults', icon: SlidersHorizontalIcon, min: 'admin' },
  { to: '/admin/recommender', label: 'Recommender', icon: SparklesIcon, min: 'admin' },
  { to: '/admin/pricing', label: 'Pricing', icon: BadgeDollarSignIcon, min: 'admin' },
  { to: '/admin/datasources', label: 'Metrics sources', icon: DatabaseIcon, min: 'admin' },
  { to: '/admin/access', label: 'Access', icon: UsersIcon, min: 'admin' },
  { to: '/admin/audit', label: 'Audit', icon: HistoryIcon, min: 'operator' },
  { to: '/admin/notifications', label: 'Notifications', icon: BellIcon, min: 'operator' },
  { to: '/admin/maintenance', label: 'Maintenance', icon: WrenchIcon, min: 'operator' },
]

export const visibleAdminNav = (auth: Auth) => ADMIN_NAV.filter((n) => auth.can(n.min ?? 'viewer'))

function Freshness() {
  const { data } = useSummary()
  const at = data?.fleet.lastRecommenderRunAt
  const [, tick] = useState(0)
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30_000)
    return () => clearInterval(t)
  }, [])
  if (!at) return null
  const stale = Date.now() - Date.parse(at) > 2 * 3600_000
  return (
    <Tooltip content={`Last recommender run: ${formatDateTime(at)}`} side="right">
      <div className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-[11px] text-muted-foreground">
        <span className="relative flex size-2 shrink-0">
          {!stale ? (
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-50" />
          ) : null}
          <span
            className={cn('relative inline-flex size-2 rounded-full', stale ? 'bg-warning' : 'bg-success')}
          />
        </span>
        <span className="hidden lg:inline">Recommender ran {relativeTime(at)}</span>
      </div>
    </Tooltip>
  )
}

/** Fleet-wide savings, always visible in the top bar. */
function FleetSavings() {
  const { data, isError } = useSummary()
  if (isError) return null
  if (!data)
    return (
      <div
        className="h-8 w-40 animate-pulse rounded-lg bg-muted"
        aria-hidden
        data-testid="fleet-savings-loading"
      />
    )
  const f = data.fleet
  if (!f.workloads) return null
  const clusters = data.clusters.filter((c) => c.workloads > 0).length
  return (
    <Tooltip
      content={
        <div className="space-y-0.5">
          <div>
            {formatUSD(f.monthlySavingsUSD.total * 12, { compact: true })} per year · CPU{' '}
            {formatUSD(f.monthlySavingsUSD.cpu)} · memory {formatUSD(f.monthlySavingsUSD.memory)} per month
          </div>
          <div className="text-muted-foreground">
            Estimated if every recommendation were applied, across {formatNumber(f.workloads)} workloads in{' '}
            {clusters} cluster{clusters === 1 ? '' : 's'}.
          </div>
        </div>
      }
    >
      <Link
        to="/opportunities"
        data-testid="fleet-savings"
        className="group flex h-8 shrink-0 items-center gap-2 rounded-lg border border-success/25 bg-success/8 pr-2.5 pl-2 text-[13px] transition-colors outline-none hover:border-success/45 hover:bg-success/12 focus-visible:ring-[3px] focus-visible:ring-ring/30"
      >
        <PiggyBankIcon className="size-4 text-success" />
        <span className="hidden text-muted-foreground sm:inline">Estimated savings</span>
        <span className="font-semibold text-success tabular">
          {formatUSD(f.monthlySavingsUSD.total)}
          <span className="text-xs font-normal text-muted-foreground">/mo</span>
        </span>
      </Link>
    </Tooltip>
  )
}

function useMediaQuery(q: string) {
  const [m, setM] = useState(() => window.matchMedia(q).matches)
  useEffect(() => {
    const mq = window.matchMedia(q)
    const on = () => setM(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [q])
  return m
}

function NavRow({ n, wide }: { n: NavItem; wide: boolean }) {
  return (
    <Tooltip content={wide ? null : n.label} side="right">
      <span className="block">
        <NavLink
          to={n.to}
          end={n.end ?? false}
          className={({ isActive }) =>
            cn(
              'group flex h-9 items-center justify-center gap-2.5 rounded-lg px-2.5 text-[13px] font-medium transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40 lg:justify-start',
              isActive
                ? 'bg-sidebar-accent text-foreground shadow-[inset_0_0_0_1px_var(--sidebar-border)]'
                : 'text-sidebar-foreground/80 hover:bg-sidebar-accent/60 hover:text-foreground',
            )
          }
        >
          {({ isActive }) => (
            <>
              <n.icon
                className={cn(
                  'size-4 shrink-0',
                  isActive ? 'text-primary' : 'text-muted-foreground group-hover:text-foreground',
                )}
              />
              <span className="hidden lg:inline">{n.label}</span>
            </>
          )}
        </NavLink>
      </span>
    </Tooltip>
  )
}

function NavHeading({ children }: { children: React.ReactNode }) {
  return (
    <>
      <div className="hidden px-2 pt-3 pb-1.5 text-[11px] font-medium tracking-wide text-muted-foreground/80 uppercase first:pt-0 lg:block">
        {children}
      </div>
      <div className="mx-2 my-2 border-t border-sidebar-border first:hidden lg:hidden" />
    </>
  )
}

function Sidebar({ auth }: { auth: Auth }) {
  const wide = useMediaQuery('(min-width: 1024px)')
  const admin = visibleAdminNav(auth)
  return (
    <aside className="w-16 shrink-0 border-r border-sidebar-border bg-sidebar text-sidebar-foreground lg:w-60">
      <div className="sticky top-0 flex h-dvh flex-col">
        <div className="flex h-14 items-center justify-center px-4 lg:justify-start">
          <Logo />
        </div>
        <nav aria-label="Main" className="flex flex-1 flex-col gap-0.5 overflow-y-auto px-2.5 pt-2 lg:px-3">
          <NavHeading>Rightsizing</NavHeading>
          {NAV.map((n) => (
            <NavRow key={n.to} n={n} wide={wide} />
          ))}
          {admin.length ? (
            <>
              <NavHeading>Administration</NavHeading>
              <div data-testid="admin-nav" className="flex flex-col gap-0.5">
                {admin.map((n) => (
                  <NavRow key={n.to} n={n} wide={wide} />
                ))}
              </div>
            </>
          ) : null}
        </nav>
        <div className="border-t border-sidebar-border p-2.5 lg:p-3">
          <Freshness />
        </div>
      </div>
    </aside>
  )
}

function initials(name: string) {
  const parts = name
    .trim()
    .split(/[\s._-]+/)
    .filter(Boolean)
  return ((parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '')).toUpperCase() || name.slice(0, 2).toUpperCase()
}

/** The signed-in user: name, role, change password, sign out. */
function UserMenu({ auth, onChangePassword }: { auth: Auth; onChangePassword: () => void }) {
  const logout = useLogout()
  const navigate = useNavigate()
  if (!auth.signedIn) return null
  const u = auth.me?.user
  const name = u?.displayName || u?.username || 'Signed in'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          className="flex h-8 items-center gap-2 rounded-full border bg-card py-0.5 pr-2.5 pl-0.5 text-[13px] shadow-xs transition-colors outline-none hover:border-ring/40 focus-visible:ring-[3px] focus-visible:ring-ring/30"
          data-testid="user-menu"
          aria-label={`Account: ${name}`}
        >
          <span className="flex size-7 items-center justify-center rounded-full bg-primary/14 text-[11px] font-semibold text-primary">
            {initials(name)}
          </span>
          <span className="hidden max-w-28 truncate font-medium md:inline">{u?.username}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <div className="flex items-center gap-2.5 px-2 py-2">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-primary/14 text-xs font-semibold text-primary">
            {initials(name)}
          </span>
          <div className="min-w-0">
            <div className="truncate text-[13px] font-medium">{name}</div>
            <div className="truncate text-xs text-muted-foreground">{u?.username}</div>
          </div>
        </div>
        <div className="flex items-center justify-between px-2 pb-2">
          <RoleBadge role={auth.role} />
          {auth.me?.sessionExpiresAt ? (
            <span className="text-[11px] text-muted-foreground">
              Session until {formatDateTime(auth.me.sessionExpiresAt, true)}
            </span>
          ) : null}
        </div>
        <DropdownMenuSeparator />
        {u?.source === 'oidc' ? null : (
          <DropdownMenuItem onSelect={onChangePassword}>
            <KeyRoundIcon /> Change password
          </DropdownMenuItem>
        )}
        <DropdownMenuItem
          data-testid="sign-out"
          onSelect={() =>
            logout.mutate(undefined, {
              onSettled: () => navigate('/login', { replace: true }),
            })
          }
        >
          <LogOutIcon /> Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ThemeMenu() {
  const { theme, setTheme, resolved } = useTheme()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label="Theme">
          {resolved === 'dark' ? <MoonIcon /> : <SunIcon />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-40">
        <DropdownMenuLabel>Theme</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(v as Theme)}>
          <DropdownMenuRadioItem value="light">
            <SunIcon /> Light
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="dark">
            <MoonIcon /> Dark
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="system">
            <MonitorIcon /> System
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

const SCENARIOS = [
  { value: 'normal', label: 'Realistic fleet' },
  { value: 'empty', label: 'Empty fleet' },
  { value: 'slow', label: 'Slow hub (2 s)' },
  { value: 'error', label: 'Hub errors' },
]

function MockMenu() {
  const [scenario, setScenario] = useState(() => {
    try {
      return localStorage.getItem('ro-mock-scenario') ?? 'normal'
    } catch {
      return 'normal'
    }
  })
  if (!__MOCK_API__) return null
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="xs" className="gap-1.5 border-dashed text-muted-foreground">
          <FlaskConicalIcon className="size-3.5" />
          Mock API
          {scenario !== 'normal' ? (
            <span className="rounded bg-warning/20 px-1 text-[10px] text-foreground">{scenario}</span>
          ) : null}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-64">
        <DropdownMenuLabel>Mock scenario</DropdownMenuLabel>
        <DropdownMenuRadioGroup
          value={scenario}
          onValueChange={(v) => {
            try {
              localStorage.setItem('ro-mock-scenario', v)
            } catch {
              /* ignore */
            }
            setScenario(v)
            window.location.reload()
          }}
        >
          {SCENARIOS.map((s) => (
            <DropdownMenuRadioItem key={s.value} value={s.value}>
              {s.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <div className="px-2 py-1.5 text-[11px] leading-relaxed text-muted-foreground">
          Data is generated in the browser by MSW from the hub OpenAPI contract. Users: admin, operator,
          viewer (password dev-password).
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Neutral screen while the session is checked: no app chrome and no data before it answers. */
function Splash() {
  return (
    <div className="flex min-h-dvh items-center justify-center" aria-busy="true" data-testid="auth-splash">
      <Loader2Icon className="size-5 animate-spin text-muted-foreground" />
      <span className="sr-only">Loading</span>
    </div>
  )
}

/** The session check failed for another reason than a missing session (hub down, network). */
function HubUnavailable({ error }: { error: unknown }) {
  const qc = useQueryClient()
  const [retrying, setRetrying] = useState(false)
  return (
    <div
      className="flex min-h-dvh items-center justify-center bg-background px-4"
      data-testid="auth-unavailable"
    >
      <div className="flex w-full max-w-[400px] flex-col items-center text-center">
        <Logo size="lg" />
        <h1 className="mt-5 text-lg font-semibold tracking-tight">Cannot reach the hub</h1>
        <p className="mt-1 text-[13px] text-muted-foreground">
          {error instanceof Error ? error.message : 'The session check failed.'} Kerf shows nothing until it
          knows who you are.
        </p>
        <Button
          className="mt-5"
          variant="outline"
          size="sm"
          disabled={retrying}
          onClick={() => {
            setRetrying(true)
            void qc.refetchQueries({ queryKey: ME_KEY }).finally(() => setRetrying(false))
          }}
        >
          {retrying ? <Loader2Icon className="animate-spin" /> : <RotateCwIcon />}
          Try again
        </Button>
      </div>
    </div>
  )
}

export function AppShell() {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [pwOpen, setPwOpen] = useState(false)
  const loc = useLocation()
  const auth = useAuth()
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((o) => !o)
      }
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [])
  useEffect(() => {
    window.scrollTo({ top: 0 })
  }, [loc.pathname])

  // Sign-in is required: nothing of the app renders, and no page asks for data, until /v1/me
  // answers with a session. An ended session goes back to sign-in, which returns here afterwards.
  if (auth.loading) return <Splash />
  // window.location, not the router's: during a lazy page's load the router still renders the page
  // being left, while the address bar already shows the one asked for.
  if (auth.loginRequired)
    return (
      <Navigate
        to={loginPath(window.location)}
        replace
        state={auth.expired ? { expired: true } : undefined}
      />
    )
  if (auth.error || !auth.me) return <HubUnavailable error={auth.error} />
  const mustChange = !!auth.me?.user?.mustChangePassword

  return (
    <div className="flex min-h-dvh">
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-primary px-3 py-2 text-primary-foreground focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <Sidebar auth={auth} />
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b bg-background/80 px-4 backdrop-blur-md md:px-6 lg:px-8">
          <button
            onClick={() => setPaletteOpen(true)}
            className="flex h-8 w-full max-w-md items-center gap-2 rounded-lg border bg-card px-2.5 text-[13px] text-muted-foreground shadow-xs transition-colors outline-none hover:border-ring/40 focus-visible:ring-[3px] focus-visible:ring-ring/30"
          >
            <SearchIcon className="size-4" />
            <span className="truncate">Search workloads, namespaces…</span>
            <kbd className="ml-auto hidden items-center gap-0.5 rounded border bg-muted px-1.5 font-mono text-[10px] sm:inline-flex">
              <CommandIcon className="size-2.5" />K
            </kbd>
          </button>
          <div className="ml-auto flex items-center gap-1.5">
            <MockMenu />
            <FleetSavings />
            <ThemeMenu />
            <UserMenu auth={auth} onChangePassword={() => setPwOpen(true)} />
          </div>
        </header>
        <main id="main" className="mx-auto w-full max-w-[1440px] flex-1 px-4 py-6 md:px-6 lg:px-8 lg:py-8">
          <Outlet />
        </main>
      </div>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <ChangePasswordDialog open={pwOpen || mustChange} forced={mustChange} onOpenChange={setPwOpen} />
      <Toaster />
    </div>
  )
}
