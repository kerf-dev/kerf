/**
 * Admin → Notifications against the MSW mock hub: channels with their last delivery, test messages
 * (success and the error path), channel and rule dialogs, the weekly digest, the delivery history
 * and the read-only view.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import NotificationsPage from './notifications'

// The page runs five queries and dialogs; give slow machines room.
vi.setConfig({ testTimeout: 30_000 })

const server = setupServer(...handlers)
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
  // Radix measures some controls; jsdom has no ResizeObserver.
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
          <NotificationsPage />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const model = () => getAdmin().notifications
const channelByName = (name: string) => model().channels.find((c) => c.name === name)!

/** Fills the reason of the open write dialog and confirms. */
async function confirmDialog(dialog: HTMLElement, reason = 'because we said so') {
  fireEvent.change(within(dialog).getByTestId('reason'), { target: { value: reason } })
  fireEvent.click(within(dialog).getByTestId('confirm-write'))
}

describe('Notifications page: channels', () => {
  it('lists channels with type, masked URL, source and last delivery', async () => {
    await signIn('admin')
    renderPage()
    expect(await screen.findByRole('heading', { level: 1, name: 'Notifications' })).toBeInTheDocument()

    const slack = await screen.findByTestId('channel-platform-alerts')
    expect(within(slack).getByText('Slack')).toBeInTheDocument()
    expect(within(slack).getByTestId('channel-url')).toHaveTextContent('https://hooks.slack.com')
    expect(within(slack).getByTestId('last-delivery').querySelector('[data-status]')).toHaveAttribute(
      'data-status',
      'sent',
    )

    expect(
      within(screen.getByTestId('channel-finops-teams')).getByText('Microsoft Teams'),
    ).toBeInTheDocument()
    const teams = screen.getByTestId('channel-finops-teams')
    expect(within(teams).getByTestId('last-delivery').querySelector('[data-status]')).toHaveAttribute(
      'data-status',
      'retrying',
    )
    expect(within(teams).getByTestId('pending-count')).toHaveTextContent('1 queued')
    expect(within(teams).getByTestId('last-error')).toHaveTextContent(/context deadline exceeded/)

    const hook = screen.getByTestId('channel-audit-sink')
    expect(within(hook).getByText('Webhook')).toBeInTheDocument()
    expect(within(hook).getByText('Signed')).toBeInTheDocument()

    const pager = screen.getByTestId('channel-oncall-pager')
    expect(within(pager).getByTestId('last-delivery').querySelector('[data-status]')).toHaveAttribute(
      'data-status',
      'failed',
    )
    expect(within(pager).getByTestId('last-error')).toHaveTextContent(/404 Not Found/)
  })

  it('shows chart-defined channels as read-only', async () => {
    await signIn('admin')
    renderPage()
    const chart = await screen.findByTestId('channel-chart-default')
    expect(within(chart).getByTestId('source-badge')).toHaveTextContent('Chart values')
    expect(within(chart).getByTestId('read-only-note')).toHaveTextContent('Managed by chart values')
    await waitFor(() => expect(within(chart).getByLabelText('Edit chart-default')).toBeDisabled())
    expect(within(chart).getByLabelText('Delete chart-default')).toBeDisabled()
    // A test message is still allowed.
    expect(within(chart).getByTestId('send-test')).toBeEnabled()
  })

  it('Send test shows the latency when the destination accepts the message', async () => {
    await signIn('admin')
    renderPage()
    const row = await screen.findByTestId('channel-platform-alerts')
    await waitFor(() => expect(within(row).getByTestId('send-test')).toBeEnabled())
    fireEvent.click(within(row).getByTestId('send-test'))
    const result = await within(row).findByTestId('test-result')
    expect(result).toHaveAttribute('data-ok', 'true')
    expect(result).toHaveTextContent(/Test message delivered/)
    expect(result).toHaveTextContent(/\d+ ms \(HTTP 200\)/)
    // The test message shows in the history.
    expect(model().deliveries[0]).toMatchObject({ kind: 'test', channelName: 'platform-alerts' })
  })

  it('Send test shows the error the destination answered with', async () => {
    await signIn('admin')
    renderPage()
    const row = await screen.findByTestId('channel-oncall-pager')
    await waitFor(() => expect(within(row).getByTestId('send-test')).toBeEnabled())
    fireEvent.click(within(row).getByTestId('send-test'))
    const result = await within(row).findByTestId('test-result')
    expect(result).toHaveAttribute('data-ok', 'false')
    expect(result).toHaveAttribute('role', 'alert')
    expect(within(result).getByText('Test message failed')).toBeInTheDocument()
    expect(within(result).getByTestId('test-error')).toHaveTextContent(
      'destination answered 404 Not Found (the webhook was removed or revoked)',
    )
    expect(result).toHaveTextContent('HTTP 404')
  })

  it('validates a new channel, then creates it', async () => {
    await signIn('admin')
    renderPage()
    const add = await screen.findByTestId('new-channel')
    await waitFor(() => expect(add).toBeEnabled())
    fireEvent.click(add)
    const dialog = await screen.findByTestId('channel-dialog')

    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    expect(await within(dialog).findAllByRole('alert')).not.toHaveLength(0)
    expect(within(dialog).getByText(/Lowercase letters, digits/)).toBeInTheDocument()
    expect(within(dialog).getByText('Required.')).toBeInTheDocument()
    expect(within(dialog).getByText('Give a reason for the change.')).toBeInTheDocument()
    expect(model().channels.some((c) => c.name === 'ops-webhook')).toBe(false)

    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'ops-webhook' } })
    fireEvent.change(within(dialog).getByLabelText('Type'), { target: { value: 'webhook' } })
    fireEvent.change(within(dialog).getByLabelText('Webhook URL'), {
      target: { value: 'https://hooks.example.com/kerf/very-secret-path' },
    })
    fireEvent.change(within(dialog).getByLabelText(/Signing secret/), { target: { value: 'short' } })
    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    expect(await within(dialog).findByText('At least 16 characters.')).toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText(/Signing secret/), {
      target: { value: 'sixteen-chars-long-secret' },
    })
    await confirmDialog(dialog, 'send pages to ops')
    await waitFor(() => expect(screen.queryByTestId('channel-dialog')).toBeNull())

    const created = channelByName('ops-webhook')
    expect(created).toMatchObject({ type: 'webhook', secretSet: true, enabled: true })
    // The page shows the masked URL only.
    const row = await screen.findByTestId('channel-ops-webhook')
    expect(within(row).getByTestId('channel-url')).toHaveTextContent('https://hooks.example.com')
    expect(row).not.toHaveTextContent('very-secret-path')
    expect(within(row).getByTestId('last-delivery')).toHaveTextContent('No deliveries yet')
  })

  it('keeps the stored URL on edit when the field is left empty and can clear the secret', async () => {
    await signIn('admin')
    renderPage()
    const row = await screen.findByTestId('channel-audit-sink')
    await waitFor(() => expect(within(row).getByLabelText('Edit audit-sink')).toBeEnabled())
    fireEvent.click(within(row).getByLabelText('Edit audit-sink'))
    const dialog = await screen.findByTestId('channel-dialog')
    expect(within(dialog).getByLabelText('Type')).toBeDisabled()
    expect(within(dialog).getByLabelText('Webhook URL')).toHaveValue('')
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /Clear the stored signing secret/ }))
    await confirmDialog(dialog, 'rotate the secret later')
    await waitFor(() => expect(screen.queryByTestId('channel-dialog')).toBeNull())
    expect(channelByName('audit-sink')).toMatchObject({
      secretSet: false,
      url: 'https://siem.example.com',
      version: 2,
    })
  })

  it('warns and blocks creation when the hub has no secrets key', async () => {
    await signIn('admin')
    model().secretsKey = false
    renderPage()
    expect(await screen.findByText('Channels cannot be created yet')).toBeInTheDocument()
    expect(screen.getByTestId('new-channel')).toBeDisabled()
  })

  it('hints that messages will not link to the UI without a base URL', async () => {
    await signIn('admin')
    model().links = false
    renderPage()
    expect(await screen.findByText('Messages will not link to this UI')).toBeInTheDocument()
    expect(screen.getByText('notifications.baseURL')).toBeInTheDocument()
  })

  it('deletes a channel with its version and a reason', async () => {
    await signIn('admin')
    renderPage()
    const row = await screen.findByTestId('channel-oncall-pager')
    // Used by a rule: delete is disabled until it is removed there.
    await waitFor(() => expect(within(row).getByLabelText('Delete oncall-pager')).toBeDisabled())
    const sink = screen.getByTestId('channel-audit-sink')
    await waitFor(() => expect(within(sink).getByLabelText('Delete audit-sink')).toBeEnabled())
    fireEvent.click(within(sink).getByLabelText('Delete audit-sink'))
    const dialog = await screen.findByTestId('delete-channel-dialog')
    await confirmDialog(dialog, 'no longer needed')
    await waitFor(() => expect(screen.queryByTestId('channel-audit-sink')).toBeNull())
    expect(channelByName('audit-sink').deletedAt).toBeTruthy()
  })

  it('explains a channel that became used since the page loaded', async () => {
    await signIn('admin')
    renderPage()
    const sink = await screen.findByTestId('channel-audit-sink')
    await waitFor(() => expect(within(sink).getByLabelText('Delete audit-sink')).toBeEnabled())
    // Someone adds a rule for it elsewhere; this page has not refetched yet.
    model().rules[1]!.channelIds.push(channelByName('audit-sink').id)
    fireEvent.click(within(sink).getByLabelText('Delete audit-sink'))
    const dialog = await screen.findByTestId('delete-channel-dialog')
    await confirmDialog(dialog, 'cleanup')
    expect(await within(dialog).findByText('The channel is still in use')).toBeInTheDocument()
    expect(within(dialog).getByText(/Remove it from the rules and the digest/)).toBeInTheDocument()
    expect(within(dialog).getByText('(channel_in_use)')).toBeInTheDocument()
  })
})

describe('Notifications page: rules', () => {
  it('summarises each rule in one line', async () => {
    await signIn('admin')
    renderPage()
    const rule = await screen.findByTestId('rule-Rollbacks and failures in prod')
    await waitFor(() =>
      expect(within(rule).getByTestId('rule-summary')).toHaveTextContent(
        'Rollbacks and failures in prod-* / any namespace → platform-alerts, oncall-pager',
      ),
    )
    expect(
      within(screen.getByTestId('rule-Applied changes in payments')).getByTestId('rule-summary'),
    ).toHaveTextContent('Applied changes in any cluster / payments, checkout-* → finops-teams')
  })

  it('creates a rule with events, globs and channels', async () => {
    await signIn('admin')
    renderPage()
    const add = await screen.findByTestId('new-rule')
    await waitFor(() => expect(add).toBeEnabled())
    fireEvent.click(add)
    const dialog = await screen.findByTestId('rule-dialog')

    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    expect(await within(dialog).findByText('Pick at least one event.')).toBeInTheDocument()
    // A rule without a channel cannot be saved.
    expect(within(dialog).getByText(/Pick at least one channel/)).toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Blocked in staging' } })
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /Change blocked/ }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /Apply failed/ }))
    const clusters = within(dialog).getByLabelText('Clusters')
    fireEvent.change(clusters, { target: { value: 'staging-*' } })
    fireEvent.keyDown(clusters, { key: 'Enter' })
    fireEvent.change(clusters, { target: { value: 'dev-1, dev-2' } })
    fireEvent.blur(clusters)
    expect(within(dialog).getByLabelText('Remove staging-* from clusters')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Remove dev-2 from clusters')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /platform-alerts/ }))
    await confirmDialog(dialog, 'watch staging')
    await waitFor(() => expect(screen.queryByTestId('rule-dialog')).toBeNull())

    const rule = model().rules.find((r) => r.name === 'Blocked in staging')!
    expect(rule).toMatchObject({
      enabled: true,
      eventTypes: ['failed', 'blocked'],
      clusters: ['staging-*', 'dev-1', 'dev-2'],
      namespaces: [],
      channelIds: [channelByName('platform-alerts').id],
    })
    expect(await screen.findByTestId('rule-Blocked in staging')).toBeInTheDocument()
  })

  it('rejects an invalid glob in the field', async () => {
    await signIn('admin')
    renderPage()
    const add = await screen.findByTestId('new-rule')
    await waitFor(() => expect(add).toBeEnabled())
    fireEvent.click(add)
    const dialog = await screen.findByTestId('rule-dialog')
    const ns = within(dialog).getByLabelText('Namespaces')
    fireEvent.change(ns, { target: { value: 'Not Valid!' } })
    fireEvent.keyDown(ns, { key: 'Enter' })
    expect(await within(dialog).findByText(/"Not": /)).toBeInTheDocument()
  })

  it('edits and deletes a rule', async () => {
    await signIn('admin')
    renderPage()
    const row = await screen.findByTestId('rule-Applied changes in payments')
    fireEvent.click(within(row).getByLabelText('Edit Applied changes in payments'))
    const dialog = await screen.findByTestId('rule-dialog')
    expect(within(dialog).getByLabelText('Name')).toHaveValue('Applied changes in payments')
    expect(within(dialog).getByRole('checkbox', { name: /Change applied/ })).toBeChecked()
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /Enabled/ }))
    await confirmDialog(dialog, 'pause for a while')
    await waitFor(() => expect(screen.queryByTestId('rule-dialog')).toBeNull())
    expect(model().rules[1]).toMatchObject({ enabled: false, version: 2 })

    const again = await screen.findByTestId('rule-Applied changes in payments')
    await waitFor(() => expect(within(again).getByText('Disabled')).toBeInTheDocument())
    fireEvent.click(within(again).getByLabelText('Delete Applied changes in payments'))
    await confirmDialog(await screen.findByTestId('delete-rule-dialog'), 'obsolete')
    await waitFor(() => expect(screen.queryByTestId('rule-Applied changes in payments')).toBeNull())
    expect(model().rules[1]!.deletedAt).toBeTruthy()
  })
})

describe('Notifications page: digest', () => {
  it('shows the schedule and saves changes with a reason', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('digest-card')
    await waitFor(() => expect(within(card).getByLabelText('Day')).toHaveValue('1'))
    expect(within(card).getByLabelText('Time zone')).toHaveValue('Europe/Berlin')
    expect(within(card).getByLabelText('Time')).toHaveValue('08:00')
    expect(within(card).getByTestId('digest-schedule')).toHaveTextContent(/Next: .* · Last: /)
    expect(within(card).getByTestId('save-digest')).toBeDisabled()

    fireEvent.change(within(card).getByLabelText('Day'), { target: { value: '5' } })
    fireEvent.change(within(card).getByLabelText('Time'), { target: { value: '17:30' } })
    fireEvent.change(within(card).getByLabelText('Time zone'), { target: { value: 'Asia/Tokyo' } })
    fireEvent.click(within(card).getByRole('checkbox', { name: /platform-alerts/ }))
    fireEvent.click(within(card).getByTestId('save-digest'))
    await confirmDialog(await screen.findByTestId('digest-dialog'), 'friday wrap-up')
    await waitFor(() => expect(model().digest.version).toBe(4))
    expect(model().digest).toMatchObject({
      enabled: true,
      weekday: 5,
      time: '17:30',
      timezone: 'Asia/Tokyo',
      channelIds: [channelByName('finops-teams').id, channelByName('platform-alerts').id],
    })
  })

  it('does not accept an enabled digest without a channel', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('digest-card')
    await waitFor(() => expect(within(card).getByLabelText('Day')).toHaveValue('1'))
    fireEvent.click(within(card).getByRole('checkbox', { name: /finops-teams/ }))
    fireEvent.click(within(card).getByTestId('save-digest'))
    expect(
      await within(card).findByText(/Pick at least one channel, or turn the digest off/),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('digest-dialog')).toBeNull()
  })

  it('turns the digest off and drops the next send time', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('digest-card')
    await waitFor(() => expect(within(card).getByLabelText('Day')).toHaveValue('1'))
    fireEvent.click(within(card).getByTestId('digest-enabled'))
    fireEvent.click(within(card).getByTestId('save-digest'))
    await confirmDialog(await screen.findByTestId('digest-dialog'), 'quiet period')
    await waitFor(() => expect(model().digest.enabled).toBe(false))
    expect(model().digest.nextSendAt).toBeUndefined()
    await waitFor(() =>
      expect(within(card).getByTestId('digest-schedule')).toHaveTextContent('Not scheduled'),
    )
  })

  it('queues the digest now', async () => {
    await signIn('admin')
    renderPage()
    const card = await screen.findByTestId('digest-card')
    const send = await within(card).findByTestId('send-digest')
    await waitFor(() => expect(send).toBeEnabled())
    fireEvent.click(send)
    await waitFor(() =>
      expect(model().deliveries[0]).toMatchObject({ kind: 'digest', channelName: 'finops-teams' }),
    )
  })
})

describe('Notifications page: deliveries', () => {
  it('lists newest first and filters by status and channel', async () => {
    await signIn('admin')
    renderPage()
    const table = await screen.findByTestId('deliveries-table')
    const rows = () => within(table).getAllByTestId('delivery-row')
    expect(rows()).toHaveLength(model().deliveries.length)
    expect(rows()[0]).toHaveTextContent('finops-teams')
    expect(rows()[0]).toHaveTextContent('Retrying')
    expect(rows()[0]).toHaveTextContent(/context deadline exceeded/)

    fireEvent.change(screen.getByLabelText('Filter by status'), { target: { value: 'failed' } })
    await waitFor(() =>
      expect(within(screen.getByTestId('deliveries-table')).getAllByTestId('delivery-row')).toHaveLength(2),
    )
    fireEvent.change(screen.getByLabelText('Filter by channel'), {
      target: { value: channelByName('platform-alerts').id },
    })
    expect(await screen.findByText('No deliveries match the filter')).toBeInTheDocument()
  })

  it('loads more pages', async () => {
    await signIn('admin')
    const m = model()
    const template = m.deliveries[m.deliveries.length - 1]!
    for (let i = 0; i < 30; i++)
      m.deliveries.push({
        ...template,
        id: `00000000-0000-4000-8000-${String(i).padStart(12, '0')}`,
        summary: `older #${i}`,
      })
    renderPage()
    const table = await screen.findByTestId('deliveries-table')
    expect(within(table).getAllByTestId('delivery-row')).toHaveLength(25)
    fireEvent.click(screen.getByTestId('load-more'))
    await waitFor(() =>
      expect(within(screen.getByTestId('deliveries-table')).getAllByTestId('delivery-row')).toHaveLength(
        m.deliveries.length,
      ),
    )
    expect(screen.queryByTestId('load-more')).toBeNull()
  })
})

describe('Notifications page: read-only users', () => {
  it('shows everything but no write controls to an operator', async () => {
    await signIn('operator')
    renderPage()
    const row = await screen.findByTestId('channel-platform-alerts')
    expect(within(row).getByTestId('last-delivery')).toBeInTheDocument()
    expect(await screen.findByTestId('rule-Rollbacks and failures in prod')).toBeInTheDocument()
    expect(await screen.findByTestId('deliveries-table')).toBeInTheDocument()
    expect(await screen.findByText(/needs the admin role/)).toBeInTheDocument()

    expect(screen.queryByTestId('new-channel')).toBeNull()
    expect(screen.queryByTestId('new-rule')).toBeNull()
    expect(screen.queryByTestId('send-test')).toBeNull()
    expect(screen.queryByTestId('send-digest')).toBeNull()
    expect(screen.queryByTestId('save-digest')).toBeNull()
    expect(screen.queryByLabelText(/^Edit /)).toBeNull()
    expect(screen.queryByLabelText(/^Delete /)).toBeNull()
    const card = screen.getByTestId('digest-card')
    await waitFor(() => expect(within(card).getByLabelText('Day')).toBeDisabled())
    expect(within(card).getByLabelText('Time')).toBeDisabled()
    expect(within(card).getByTestId('digest-channels')).toHaveTextContent('finops-teams')
  })
})
