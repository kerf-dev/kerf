/**
 * Admin → Metrics sources against the MSW mock hub, for a Dynatrace datasource: the form (environment
 * URL, token source, ActiveGate URL, validation, request body) and the Test result with its metric
 * mapping.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import DatasourcesPage from './datasources'

vi.setConfig({ testTimeout: 30_000 })

const server = setupServer(...handlers)
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  resetDb()
})
afterEach(cleanup)

async function signIn(username: string) {
  const r = await fetch(`${window.location.origin}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password: 'dev-password' }),
  })
  expect(r.status).toBe(200)
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>
          <DatasourcesPage />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

/** The JSON bodies of the datasource create requests the page sends. */
function captureCreates() {
  const bodies: Record<string, unknown>[] = []
  server.events.on('request:start', ({ request }) => {
    if (request.method === 'POST' && new URL(request.url).pathname === '/v1/admin/datasources')
      void request
        .clone()
        .json()
        .then((b) => bodies.push(b as Record<string, unknown>))
  })
  return bodies
}

async function openNewDynatraceForm() {
  await signIn('admin')
  renderPage()
  fireEvent.click(await screen.findByTestId('new-datasource'))
  const dialog = await screen.findByTestId('datasource-dialog')
  fireEvent.change(within(dialog).getByLabelText('Type'), { target: { value: 'dynatrace' } })
  return dialog
}

describe('Datasource form: Dynatrace', () => {
  it('shows the environment URL, a fixed platform token, the token source and the ActiveGate URL', async () => {
    const dialog = await openNewDynatraceForm()
    expect(within(dialog).getByLabelText('Environment URL')).toBeInTheDocument()
    expect(within(dialog).queryByLabelText('Query URL')).not.toBeInTheDocument()
    expect(within(dialog).getByText('Platform token')).toBeInTheDocument()
    expect(within(dialog).queryByLabelText('Authentication')).not.toBeInTheDocument()
    const source = within(dialog).getByLabelText('Token source')
    expect(source).toHaveValue('secret')
    expect(within(dialog).getByLabelText('Secret name')).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/ActiveGate URL/)).toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/Max points/)).not.toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/Extra headers/)).not.toBeInTheDocument()
    expect(within(dialog).queryByText(/api\/v1\/query is appended/)).not.toBeInTheDocument()

    fireEvent.change(source, { target: { value: 'paste' } })
    expect(within(dialog).getByLabelText('Platform token')).toHaveAttribute('type', 'password')
    expect(within(dialog).queryByLabelText('Secret name')).not.toBeInTheDocument()
  })

  it('validates the name of the Secret and the ActiveGate URL', async () => {
    const dialog = await openNewDynatraceForm()
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'dt-staging' } })
    fireEvent.change(within(dialog).getByLabelText('Environment URL'), {
      target: { value: 'https://abc12345.apps.dynatrace.com' },
    })
    fireEvent.change(within(dialog).getByLabelText('Secret name'), { target: { value: 'Not A Label' } })
    fireEvent.change(within(dialog).getByLabelText(/ActiveGate URL/), { target: { value: 'not a url' } })
    fireEvent.change(within(dialog).getByTestId('reason'), { target: { value: 'new environment' } })
    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    expect(await within(dialog).findByText(/Lowercase letters, digits and "-"/)).toBeInTheDocument()
    expect(within(dialog).getByText('An http(s) URL.')).toBeInTheDocument()
    expect(getAdmin().datasources.some((d) => d.name === 'dt-staging')).toBe(false)
  })

  it('creates the datasource with a Secret and an ActiveGate URL', async () => {
    const bodies = captureCreates()
    const dialog = await openNewDynatraceForm()
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'dt-staging' } })
    fireEvent.change(within(dialog).getByLabelText('Environment URL'), {
      target: { value: 'https://abc12345.apps.dynatrace.com' },
    })
    fireEvent.change(within(dialog).getByLabelText('Secret name'), { target: { value: 'dynatrace-staging' } })
    fireEvent.change(within(dialog).getByLabelText(/ActiveGate URL/), {
      target: { value: 'https://activegate.example.internal:9999' },
    })
    fireEvent.change(within(dialog).getByTestId('reason'), { target: { value: 'new environment' } })
    fireEvent.click(within(dialog).getByTestId('confirm-write'))

    const card = await screen.findByTestId('datasource-dt-staging')
    expect(within(card).getByText('Dynatrace')).toBeInTheDocument()
    expect(card).toHaveTextContent('token from Secret dynatrace-staging')
    expect(card).toHaveTextContent('https://activegate.example.internal:9999')
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toMatchObject({
      name: 'dt-staging',
      type: 'dynatrace',
      url: 'https://abc12345.apps.dynatrace.com',
      auth: { type: 'bearer', tokenSecret: 'dynatrace-staging' },
      dynatrace: { activeGateURL: 'https://activegate.example.internal:9999' },
    })
    expect(bodies[0]).not.toHaveProperty('maxPoints')
    expect(bodies[0]!.auth).not.toHaveProperty('token')
  })

  it('creates the datasource with a pasted token', async () => {
    const bodies = captureCreates()
    const dialog = await openNewDynatraceForm()
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'dt-pasted' } })
    fireEvent.change(within(dialog).getByLabelText('Environment URL'), {
      target: { value: 'https://abc12345.apps.dynatrace.com' },
    })
    fireEvent.change(within(dialog).getByLabelText('Token source'), { target: { value: 'paste' } })
    fireEvent.change(within(dialog).getByLabelText('Platform token'), {
      target: { value: 'dt0s16.SYNTHETIC' },
    })
    fireEvent.change(within(dialog).getByTestId('reason'), { target: { value: 'new environment' } })
    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toMatchObject({
      type: 'dynatrace',
      auth: { type: 'bearer', token: 'dt0s16.SYNTHETIC' },
    })
    expect(bodies[0]).not.toHaveProperty('dynatrace')
    expect(bodies[0]!.auth).not.toHaveProperty('tokenSecret')
  })

  it('pre-fills the Secret name when editing', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('datasource-dynatrace-prod')
    fireEvent.click(within(card).getByLabelText('Edit dynatrace-prod'))
    const dialog = await screen.findByTestId('datasource-dialog')
    expect(within(dialog).getByLabelText('Token source')).toHaveValue('secret')
    expect(within(dialog).getByLabelText('Secret name')).toHaveValue('dynatrace')
    expect(within(dialog).getByLabelText('Environment URL')).toHaveValue(
      'https://abc12345.apps.dynatrace.com',
    )
  })

  it('offers the Secret for bearer auth on other types too', async () => {
    await signIn('admin')
    renderPage()
    fireEvent.click(await screen.findByTestId('new-datasource'))
    const dialog = await screen.findByTestId('datasource-dialog')
    fireEvent.change(within(dialog).getByLabelText('Authentication'), { target: { value: 'bearer' } })
    expect(within(dialog).getByLabelText('Token source')).toHaveValue('paste')
    fireEvent.change(within(dialog).getByLabelText('Token source'), { target: { value: 'secret' } })
    expect(within(dialog).getByLabelText('Secret name')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Query URL')).toBeInTheDocument()
  })
})

describe('Datasource Test: metric mapping', () => {
  it('names each missing metric and lists the inputs Dynatrace cannot provide', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('datasource-dynatrace-prod')
    expect(card).toHaveTextContent('token from Secret dynatrace')
    fireEvent.click(within(card).getByRole('button', { name: /Test/ }))

    const mapping = await within(card).findByTestId('metric-mapping')
    expect(within(mapping).getByRole('heading', { name: 'Metric mapping' })).toBeInTheDocument()
    const missing = within(mapping).getByTestId('metric-mapping-missing')
    expect(missing).toHaveTextContent('Missing:')
    expect(missing).toHaveTextContent('dt.kubernetes.container.restarts')
    expect(missing).toHaveTextContent('dt.kubernetes.container.oom_kills')
    const unavailable = within(mapping).getByTestId('metric-mapping-unavailable')
    expect(unavailable).toHaveTextContent('kube_horizontalpodautoscaler_*')
    expect(unavailable).toHaveTextContent('DCGM_FI_DEV_*')
    expect(unavailable).toHaveTextContent('kube_pod_init_container_*')

    const table = within(mapping).getByRole('table')
    for (const h of ['Kerf input', 'Dynatrace metric', 'Used for', 'Status', 'Series'])
      expect(within(table).getByRole('columnheader', { name: h })).toBeInTheDocument()
    const rows = within(table).getAllByRole('row')
    const cpu = rows.find((r) => r.textContent?.includes('container_cpu_usage_seconds_total'))!
    expect(cpu).toHaveAttribute('data-status', 'found')
    expect(within(cpu).getByText('dt.kubernetes.container.cpu_usage')).toBeInTheDocument()
    const hpa = rows.find((r) => r.textContent?.includes('kube_horizontalpodautoscaler_*'))!
    expect(hpa).toHaveAttribute('data-status', 'notAvailable')
    expect(within(hpa).getByText('not available')).toBeInTheDocument()
    expect(within(hpa).getAllByText('—')).toHaveLength(2)
  })

  it('shows no mapping for a Prometheus-compatible datasource', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('datasource-local')
    fireEvent.click(within(card).getByRole('button', { name: /Test/ }))
    await within(card).findByText(/Reachable|Problems found/)
    expect(within(card).queryByTestId('metric-mapping')).not.toBeInTheDocument()
  })
})
