import { useRouteError } from 'react-router'
import { ErrorState } from '@/components/states'
import { Skeleton } from '@/components/ui/skeleton'

export function RouteError() {
  const err = useRouteError()
  return (
    <div className="flex min-h-dvh items-center justify-center p-6">
      <ErrorState
        error={err instanceof Error ? err : new Error('Unexpected error')}
        onRetry={() => window.location.reload()}
      />
    </div>
  )
}

export function PageFallback() {
  return (
    <div className="space-y-6" aria-busy="true" aria-label="Loading">
      <Skeleton className="h-8 w-56" />
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-32 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-96 rounded-xl" />
    </div>
  )
}
