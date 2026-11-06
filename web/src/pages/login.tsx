import {
  ChevronDownIcon,
  ClockIcon,
  EyeIcon,
  EyeOffIcon,
  FlaskConicalIcon,
  KeyRoundIcon,
  Loader2Icon,
  LockKeyholeIcon,
  LogInIcon,
} from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { Navigate, useLocation, useNavigate, useSearchParams } from 'react-router'
import { ApiError } from '@/api/client'
import { safeNext, useAuth, useAuthProviders, useLogin } from '@/api/auth'
import { Logo } from '@/components/layout/logo'
import { Button } from '@/components/ui/button'
import { Callout, Field } from '@/components/ui/form'
import { Input } from '@/components/ui/input'

function loginError(e: unknown): { title: string; detail?: string } | undefined {
  if (!e) return undefined
  if (e instanceof ApiError) {
    // One message for a wrong password and an unknown, disabled or locked user (the hub does the same).
    if (e.status === 401) return { title: 'Wrong username or password.' }
    if (e.status === 429) {
      const mins = e.retryAfter ? Math.max(1, Math.ceil(e.retryAfter / 60)) : undefined
      return {
        title: 'Too many attempts',
        detail: `Too many failed sign-ins. Try again${mins ? ` in ${mins} minute${mins === 1 ? '' : 's'}` : ' later'}, or ask an admin to reset your password.`,
      }
    }
    if (e.status === 404 || e.status === 501)
      return {
        title: 'This hub does not support sign-in',
        detail: 'Upgrade the hub to the version of this web UI.',
      }
    if (e.status === 0 || e.status >= 500) return { title: 'Cannot reach the hub', detail: e.message }
    return { title: 'Sign-in failed', detail: e.message }
  }
  return { title: 'Sign-in failed', detail: String(e) }
}

/** The sign-in page's `?error=` after a sign-in with the identity provider (hub: oidcFail). */
function oidcError(code: string | null, provider: string): { title: string; detail?: string } | undefined {
  switch (code) {
    case null:
    case '':
      return undefined
    case 'oidc_denied':
      return {
        title: 'No access to Kerf',
        detail: `${provider} signed you in, but none of your groups gives a Kerf role. Ask an admin to add you to a group with access.`,
      }
    case 'oidc_expired':
      return { title: 'The sign-in took too long', detail: 'Start again from this page.' }
    case 'oidc_conflict':
      return {
        title: 'This account name is taken',
        detail: `A local Kerf user has the name ${provider} gives you. Ask an admin to rename or delete that user.`,
      }
    case 'oidc_unavailable':
      return {
        title: `Cannot reach ${provider}`,
        detail: 'The identity provider does not answer. Try again in a moment.',
      }
    default:
      return {
        title: `Sign-in with ${provider} failed`,
        detail: 'Try again, or ask an admin to check the hub logs.',
      }
  }
}

const DEMO_USERS = [
  { username: 'admin', role: 'Full access' },
  { username: 'operator', role: 'Workload actions, pause' },
  { username: 'viewer', role: 'Read-only' },
]

export default function LoginPage() {
  const auth = useAuth()
  const login = useLogin()
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  // Set by the app shell when the session this tab had ended (expired, revoked, signed out elsewhere).
  const expired = (useLocation().state as { expired?: boolean } | null)?.expired === true
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [show, setShow] = useState(false)
  const [touched, setTouched] = useState(false)
  const providers = useAuthProviders()
  const oidc = providers.data?.oidc
  // Local sign-in off: only the break-glass admin uses a password, behind a disclosure.
  const localOff = providers.data?.local === false
  const [showLocal, setShowLocal] = useState(false)
  const id = useId()

  useEffect(() => {
    document.title = 'Sign in · Kerf'
  }, [])

  if (auth.signedIn && !login.isPending) return <Navigate to={next} replace />

  const err = loginError(login.error) ?? oidcError(params.get('error'), oidc?.name ?? 'the identity provider')
  const passwordForm = !localOff || showLocal
  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!username.trim() || !password) return
    login.mutate(
      { username: username.trim(), password },
      { onSuccess: () => navigate(next, { replace: true }) },
    )
  }

  return (
    <div className="relative flex min-h-dvh items-center justify-center overflow-hidden bg-background px-4 py-10">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(60rem_30rem_at_50%_-10%,color-mix(in_oklch,var(--primary)_14%,transparent),transparent_70%)]"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 [background-image:linear-gradient(var(--border)_1px,transparent_1px),linear-gradient(90deg,var(--border)_1px,transparent_1px)] [background-size:48px_48px] [mask-image:radial-gradient(ellipse_at_center,black_20%,transparent_70%)] opacity-40"
      />
      <div className="relative w-full max-w-[400px]">
        <div className="mb-7 flex flex-col items-center text-center">
          <Logo size="lg" />
          <h1 className="mt-5 text-xl font-semibold tracking-tight">Sign in to Kerf</h1>
          <p className="mt-1 text-[13px] text-muted-foreground">Kubernetes rightsizing for your fleet</p>
        </div>
        <div
          className="flex flex-col gap-4 rounded-2xl border bg-card p-6 shadow-[0_1px_2px_rgb(0_0_0/0.04),0_12px_32px_-12px_rgb(0_0_0/0.12)]"
          data-testid="login-form"
        >
          {err ? (
            <Callout tone="destructive" icon={LockKeyholeIcon} title={err.title}>
              {err.detail}
            </Callout>
          ) : expired ? (
            <div data-testid="session-expired" role="status">
              <Callout tone="info" icon={ClockIcon} title="Your session ended">
                Sign in again to continue where you were.
              </Callout>
            </div>
          ) : null}
          {oidc ? (
            <>
              <Button asChild className="h-9 w-full" data-testid="oidc-sign-in">
                {/* A full page load: the hub redirects to the provider. */}
                <a href={`${oidc.loginURL}?next=${encodeURIComponent(next)}`}>
                  <LogInIcon /> Sign in with {oidc.name}
                </a>
              </Button>
              {passwordForm ? (
                <div className="flex items-center gap-3 text-xs text-muted-foreground" aria-hidden>
                  <span className="h-px flex-1 bg-border" />
                  or with a password
                  <span className="h-px flex-1 bg-border" />
                </div>
              ) : (
                <button
                  type="button"
                  onClick={() => setShowLocal(true)}
                  className="mx-auto inline-flex items-center gap-1 rounded text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/30"
                  aria-expanded={false}
                  data-testid="break-glass-toggle"
                >
                  <KeyRoundIcon className="size-3.5" /> Administrator sign-in with a password
                  <ChevronDownIcon className="size-3.5" />
                </button>
              )}
            </>
          ) : null}
          {passwordForm ? (
            <form onSubmit={submit} className="flex flex-col gap-4" noValidate data-testid="password-form">
              {localOff ? (
                <p className="text-xs text-muted-foreground">
                  Password sign-in is only for the break-glass administrator, when{' '}
                  {oidc?.name ?? 'the identity provider'} is down.
                </p>
              ) : null}
              <Field
                label="Username"
                htmlFor={`${id}-u`}
                error={touched && !username.trim() ? 'Enter your username.' : undefined}
              >
                <Input
                  id={`${id}-u`}
                  name="username"
                  autoComplete="username"
                  autoCapitalize="none"
                  spellCheck={false}
                  autoFocus={!oidc || localOff}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="h-9"
                  aria-invalid={touched && !username.trim()}
                />
              </Field>
              <Field
                label="Password"
                htmlFor={`${id}-p`}
                error={touched && !password ? 'Enter your password.' : undefined}
              >
                <div className="relative">
                  <Input
                    id={`${id}-p`}
                    name="password"
                    type={show ? 'text' : 'password'}
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    className="h-9 pr-9"
                    aria-invalid={touched && !password}
                  />
                  <button
                    type="button"
                    onClick={() => setShow((s) => !s)}
                    className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-1 text-muted-foreground hover:text-foreground"
                    aria-label={show ? 'Hide password' : 'Show password'}
                  >
                    {show ? <EyeOffIcon className="size-4" /> : <EyeIcon className="size-4" />}
                  </button>
                </div>
              </Field>
              <Button
                type="submit"
                variant={oidc ? 'outline' : 'default'}
                className="mt-1 h-9 w-full"
                disabled={login.isPending}
                data-testid="login-submit"
              >
                {login.isPending ? <Loader2Icon className="animate-spin" /> : null}
                Sign in
              </Button>
              <p className="text-center text-xs text-muted-foreground">
                Forgot your password? Ask an admin to reset it.
              </p>
            </form>
          ) : null}
        </div>
        {__MOCK_API__ ? (
          <div className="mt-6 rounded-xl border border-dashed bg-card/60 p-3.5 text-xs">
            <div className="mb-2 flex items-center gap-1.5 font-medium text-muted-foreground">
              <FlaskConicalIcon className="size-3.5" /> Mock API users · password{' '}
              <code className="font-mono">dev-password</code>
            </div>
            <div className="grid grid-cols-3 gap-1.5">
              {DEMO_USERS.map((u) => (
                <button
                  key={u.username}
                  type="button"
                  onClick={() => {
                    setUsername(u.username)
                    setPassword('dev-password')
                  }}
                  className="rounded-lg border bg-card px-2 py-1.5 text-left transition-colors hover:border-ring/40"
                >
                  <div className="font-mono font-medium">{u.username}</div>
                  <div className="truncate text-[11px] text-muted-foreground">{u.role}</div>
                </button>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </div>
  )
}
