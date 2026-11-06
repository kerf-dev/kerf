import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CopyAsMenu } from '@/components/copy-as'
import { Toaster } from '@/components/ui/toast'
import type { PatchWorkload } from '@/lib/patch/patch'

const withValues: PatchWorkload = {
  kind: 'Deployment',
  namespace: 'payments',
  name: 'api',
  containers: [{ name: 'app', recommended: { requests: { cpu: '250m', memory: '512Mi' } } }],
}
const without: PatchWorkload = { kind: 'DaemonSet', namespace: 'kube-system', name: 'agent', containers: [] }

let writeText: ReturnType<typeof vi.fn>

beforeEach(() => {
  writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
})
afterEach(cleanup)

function renderMenu(workloads: PatchWorkload[]) {
  return render(
    <>
      <CopyAsMenu workloads={workloads} />
      <Toaster />
    </>,
  )
}

/** Radix opens a menu on Enter, like a keyboard user would. */
function openMenu() {
  const trigger = screen.getByTestId('copy-as')
  trigger.focus()
  fireEvent.keyDown(trigger, { key: 'Enter' })
  return screen.findByTestId('copy-as-menu')
}

describe('CopyAsMenu', () => {
  it('lists the four formats and copies the chosen one with a toast', async () => {
    renderMenu([withValues])
    const menu = await openMenu()
    expect(
      within(menu)
        .getAllByRole('menuitem')
        .map((i) => i.textContent),
    ).toEqual([
      expect.stringContaining('Strategic merge patch'),
      expect.stringContaining('kubectl patch'),
      expect.stringContaining('Kustomize patch'),
      expect.stringContaining('Helm values'),
      expect.stringContaining('Preview'),
    ])
    fireEvent.click(screen.getByTestId('copy-as-kubectl'))
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    expect(writeText.mock.calls[0]![0]).toMatch(/^# Deployment payments\/api\nkubectl patch deployment 'api'/)
    expect(await screen.findByText('Copied as kubectl patch')).toBeInTheDocument()
  })

  it('is disabled when nothing has a recommendation', () => {
    renderMenu([without])
    expect(screen.getByTestId('copy-as')).toBeDisabled()
  })

  it('skips workloads without values in a bulk copy and says so', async () => {
    renderMenu([withValues, without])
    await openMenu()
    fireEvent.click(screen.getByTestId('copy-as-patch'))
    await waitFor(() => expect(writeText).toHaveBeenCalled())
    expect(writeText.mock.calls[0]![0]).not.toContain('kube-system')
    expect(await screen.findByText('1 workload (1 without a recommendation skipped)')).toBeInTheDocument()
  })

  it('previews each format and copies from the dialog', async () => {
    renderMenu([withValues])
    await openMenu()
    fireEvent.click(screen.getByTestId('copy-as-preview'))
    const out = await screen.findByTestId('copy-as-output')
    expect(out).toHaveTextContent('kind: Deployment')
    fireEvent.click(screen.getByRole('radio', { name: 'Helm values' }))
    expect(screen.getByTestId('copy-as-output')).toHaveTextContent(/app:\s+resources:/)
    fireEvent.click(screen.getByTestId('copy-as-copy'))
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    expect(writeText.mock.calls[0]![0]).toContain('app:\n  resources:')
  })

  it('falls back to the preview when the clipboard is refused', async () => {
    writeText.mockRejectedValue(new Error('denied'))
    renderMenu([withValues])
    await openMenu()
    fireEvent.click(screen.getByTestId('copy-as-patch'))
    expect(await screen.findByTestId('copy-as-output')).toBeInTheDocument()
    expect(await screen.findByText('Could not copy to the clipboard')).toBeInTheDocument()
  })
})
