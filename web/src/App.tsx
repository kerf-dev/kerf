import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { lazy, Suspense } from 'react'
import { createBrowserRouter, Navigate, RouterProvider, useParams } from 'react-router'
import { ME_KEY } from '@/api/auth'
import { RequireRole } from '@/components/admin/require-role'
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
const LoginPage = lazy(() => import('@/pages/login'))
const ClusterDetailPage = lazy(() => import('@/pages/cluster-detail'))
const ClusterWizardPage = lazy(() => import('@/pages/cluster-wizard'))
const AutomationPage = lazy(() => import('@/pages/admin/automation'))
const PoliciesPage = lazy(() => import('@/pages/admin/policies'))
const PolicyPage = lazy(() => import('@/pages/admin/policy'))
const DefaultsPage = lazy(() => import('@/pages/admin/defaults'))
const RecommenderPage = lazy(() => import('@/pages/admin/recommender'))
const PricingPage = lazy(() => import('@/pages/admin/pricing'))
const DatasourcesPage = lazy(() => import('@/pages/admin/datasources'))
const AccessPage = lazy(() => import('@/pages/admin/access'))
const AuditPage = lazy(() => import('@/pages/admin/audit'))
const MaintenancePage = lazy(() => import('@/pages/admin/maintenance'))
const NotificationsPage = lazy(() => import('@/pages/admin/notifications'))

/** A 401 on any request means the session ended: re-ask `/v1/me`, which routes to the login if needed. */
function onAuthError(e: unknown, key?: readonly unknown[]) {
  if (e instanceof ApiError && e.status === 401 && key?.[0] !== ME_KEY[0])
    void queryClient.invalidateQueries({ queryKey: ME_KEY })
}

const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({ onError: (e, q) => onAuthError(e, q.queryKey) }),
  mutationCache: new MutationCache({ onError: (e) => onAuthError(e) }),
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: false,
      retry: (n, e) => n < 2 && !(e instanceof ApiError && e.status >= 400 && e.status < 500),
    },
  },
})

const s = (el: React.ReactNode) => <Suspense fallback={<PageFallback />}>{el}</Suspense>

/** The cluster admin views live under /clusters (list, detail tabs, onboarding); keep old links working. */
function ClusterAdminRedirect() {
  const { cluster } = useParams()
  return <Navigate to={cluster ? `/clusters/${encodeURIComponent(cluster)}` : '/clusters'} replace />
}

const admin = (el: React.ReactNode, min: 'operator' | 'admin' = 'admin') =>
  s(<RequireRole min={min}>{el}</RequireRole>)

const router = createBrowserRouter([
  { path: 'login', element: s(<LoginPage />), errorElement: <RouteError /> },
  {
    element: <AppShell />,
    errorElement: <RouteError />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: 'opportunities', element: s(<OpportunitiesPage />) },
      { path: 'workloads/:cluster/:namespace/:kind/:name', element: s(<WorkloadPage />) },
      { path: 'clusters', element: s(<ClustersPage />) },
      { path: 'clusters/new', element: admin(<ClusterWizardPage />) },
      { path: 'clusters/:cluster', element: s(<ClusterDetailPage />) },
      { path: 'admin', element: admin(<AutomationPage />, 'operator') },
      { path: 'admin/clusters', element: <ClusterAdminRedirect /> },
      { path: 'admin/clusters/:cluster', element: <ClusterAdminRedirect /> },
      { path: 'admin/policies', element: admin(<PoliciesPage />, 'operator') },
      { path: 'admin/policies/new', element: admin(<PolicyPage />) },
      { path: 'admin/policies/:policyId', element: admin(<PolicyPage />, 'operator') },
      { path: 'admin/defaults', element: admin(<DefaultsPage />) },
      { path: 'admin/recommender', element: admin(<RecommenderPage />) },
      { path: 'admin/pricing', element: admin(<PricingPage />) },
      { path: 'admin/datasources', element: admin(<DatasourcesPage />) },
      { path: 'admin/access', element: admin(<AccessPage />) },
      { path: 'admin/audit', element: admin(<AuditPage />, 'operator') },
      { path: 'admin/notifications', element: admin(<NotificationsPage />, 'operator') },
      { path: 'admin/maintenance', element: admin(<MaintenancePage />, 'operator') },
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
