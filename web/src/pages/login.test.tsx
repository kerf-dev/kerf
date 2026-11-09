/** The sign-in page against the MSW mock hub. */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { handlers, resetDb } from '@/mocks/handlers'
import LoginPage from './login'

const server = setupServer(...handlers)
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  resetDb()
  document.title = 'Kerf'
})
afterEach(cleanup)

const renderLogin = (path = '/login') =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[path]}>
        <LoginPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )

describe('Sign-in page', () => {
  it('sets its tab title and gives the app its title back when it goes away', async () => {
    const { unmount } = renderLogin()
    expect(await screen.findByTestId('login-form')).toBeInTheDocument()
    expect(document.title).toBe('Sign in · Kerf')
    unmount()
    expect(document.title).toBe('Kerf')
  })

  it('offers the identity provider above the password form and explains its errors', async () => {
    localStorage.setItem('ro-mock-oidc', 'on')
    renderLogin('/login?next=%2Fclusters&error=oidc_denied')
    const sso = await screen.findByTestId('oidc-sign-in')
    expect(sso).toHaveTextContent('Sign in with Okta')
    expect(sso).toHaveAttribute('href', '/v1/auth/oidc/login?next=%2Fclusters')
    expect(screen.getByText('No access to Kerf')).toBeInTheDocument()
    expect(screen.getByTestId('password-form')).toBeInTheDocument()
  })

  it('offers the SAML identity provider and names it in its errors', async () => {
    localStorage.setItem('ro-mock-saml', 'on')
    renderLogin('/login?next=%2Fclusters&error=saml_denied')
    const sso = await screen.findByTestId('saml-sign-in')
    expect(sso).toHaveTextContent('Sign in with Microsoft Entra ID')
    expect(sso).toHaveAttribute('href', '/v1/auth/saml/login?next=%2Fclusters')
    expect(screen.queryByTestId('oidc-sign-in')).not.toBeInTheDocument()
    expect(screen.getByText('No access to Kerf')).toBeInTheDocument()
    expect(screen.getByText(/Microsoft Entra ID signed you in/)).toBeInTheDocument()
    expect(screen.getByTestId('password-form')).toBeInTheDocument()
  })

  it('offers both identity providers, OpenID Connect first', async () => {
    localStorage.setItem('ro-mock-oidc', 'on')
    localStorage.setItem('ro-mock-saml', 'on')
    renderLogin('/login?error=saml_unavailable')
    const oidc = await screen.findByTestId('oidc-sign-in')
    const saml = screen.getByTestId('saml-sign-in')
    expect(oidc.compareDocumentPosition(saml) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getByText('Cannot reach Microsoft Entra ID')).toBeInTheDocument()
  })

  it('hides the password form behind the break-glass toggle when only SAML signs in', async () => {
    localStorage.setItem('ro-mock-saml', 'only')
    renderLogin()
    expect(await screen.findByTestId('saml-sign-in')).toBeInTheDocument()
    expect(screen.queryByTestId('password-form')).not.toBeInTheDocument()
    expect(screen.getByTestId('break-glass-toggle')).toBeInTheDocument()
  })
})
