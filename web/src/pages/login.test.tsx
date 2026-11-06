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
})
