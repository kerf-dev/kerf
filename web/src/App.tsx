import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { lazy, Suspense } from 'react'
import { createBrowserRouter, RouterProvider } from 'react-router'
import { AppShell } from '@/components/layout/app-shell'
import { ThemeProvider } from '@/components/theme-provider'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError } from '@/api/client'
import { RouteError, PageFallback } from '@/pages/route-error'
import { OverviewPage } from '@/pages/overview'

const OpportunitiesPage = lazy(() => import('@/pages/opportunities'))
const WorkloadPage = lazy(() => import('@/pages/workload'))
const ClustersPage = lazy(() => import('@/pages/clusters'))
const ActivityPage = lazy(() => import('@/pages/activity'))
const NotFoundPage = lazy(() => import('@/pages/not-found'))

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: false,
      retry: (n, e) => n < 2 && !(e instanceof ApiError && e.status >= 400 && e.status < 500),
    },
  },
})

const s = (el: React.ReactNode) => <Suspense fallback={<PageFallback />}>{el}</Suspense>

const router = createBrowserRouter([
  {
    element: <AppShell />,
    errorElement: <RouteError />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: 'opportunities', element: s(<OpportunitiesPage />) },
      { path: 'workloads/:cluster/:namespace/:kind/:name', element: s(<WorkloadPage />) },
      { path: 'clusters', element: s(<ClustersPage />) },
      { path: 'activity', element: s(<ActivityPage />) },
      { path: '*', element: s(<NotFoundPage />) },
    ],
  },
])

export function App() {
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delayDuration={250}>
          <RouterProvider router={router} />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
