import { getResponse } from 'msw'
import { handlers } from './handlers'

/**
 * Starts the mock API. Prefers the MSW service worker (requests show up in DevTools' network tab);
 * where service workers are unavailable (some embedded browsers, non-secure origins) it falls back to
 * patching `fetch` in the page with the same handlers.
 */
export async function startMocks() {
  try {
    if (!('serviceWorker' in navigator)) throw new Error('no navigator.serviceWorker')
    if (readTransport() === 'fetch') throw new Error('service worker failed earlier in this browser')
    const { worker } = await import('./browser')
    await Promise.race([
      worker.start({ onUnhandledRequest: 'bypass', quiet: true }),
      new Promise((_, reject) => setTimeout(() => reject(new Error('service worker start timed out')), 1500)),
    ])
    return 'service-worker'
  } catch (e) {
    writeTransport('fetch')
    console.info(
      '[mock api] service worker unavailable, patching fetch instead:',
      e instanceof Error ? e.message.split('\n')[0] : e,
    )
    const original = window.fetch.bind(window)
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init)
      if (new URL(request.url).pathname.startsWith('/v1/')) {
        const res = await getResponse(handlers, request)
        if (res) return res
      }
      return original(request)
    }
    return 'fetch'
  }
}

const TRANSPORT_KEY = 'ro-mock-transport'
function readTransport() {
  try {
    return localStorage.getItem(TRANSPORT_KEY)
  } catch {
    return null
  }
}
function writeTransport(v: string) {
  try {
    localStorage.setItem(TRANSPORT_KEY, v)
  } catch {
    /* ignore */
  }
}
