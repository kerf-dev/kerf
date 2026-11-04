import { LockIcon, LogInIcon } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import { useAuth } from '@/api/auth'
import type { UserRole } from '@/api/types'
import { EmptyState } from '@/components/states'
import { Button } from '@/components/ui/button'

/** Shows `children` only to signed-in users with at least `min`; explains why otherwise. */
export function RequireRole({ min, children }: { min: UserRole; children: React.ReactNode }) {
  const auth = useAuth()
  const loc = useLocation()
  if (auth.can(min)) return <>{children}</>
  return (
    <EmptyState
      icon={LockIcon}
      title={auth.signedIn ? `This page needs the ${min} role` : 'Sign in to open the admin area'}
      description={
        auth.signedIn
          ? `You are signed in as ${auth.me?.user?.username} (${auth.role}). Ask an admin for access.`
          : 'The admin area changes how the fleet is rightsized, so it needs a signed-in user with the right role.'
      }
      action={
        auth.signedIn ? (
          <Button asChild variant="outline" size="sm">
            <Link to="/">Back to the overview</Link>
          </Button>
        ) : (
          <Button asChild size="sm">
            <Link to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`}>
              <LogInIcon /> Sign in
            </Link>
          </Button>
        )
      }
      className="py-24"
    />
  )
}
