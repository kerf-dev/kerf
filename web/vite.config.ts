/// <reference types="vitest/config" />
import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

// Modes:
//   mock  (`npm run dev`, `make web-dev`)       – MSW serves the API in the browser, no hub needed.
//   live  (`npm run dev:live`, `make web-dev-live`) – /v1 is proxied to a real hub (default
//         http://localhost:18080, the dev NodePort). Like nginx in production: only the `kerf_session`
//         cookie goes through, never a bearer token, so every page needs a sign-in.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const hub = env.KERF_HUB_URL || 'http://localhost:18080'
  return {
    plugins: [react(), tailwindcss()],
    resolve: { alias: { '@': path.resolve(__dirname, './src') } },
    define: {
      __MOCK_API__: JSON.stringify(mode === 'mock'),
      // The UI's own version on Admin → Maintenance (WO-33): `make release` builds the image with
      // --build-arg VERSION, which the Dockerfile passes as KERF_VERSION.
      __KERF_VERSION__: JSON.stringify(env.KERF_VERSION || 'dev'),
    },
    server: {
      port: 5173,
      strictPort: false,
      proxy:
        mode === 'live'
          ? {
              '/v1/': {
                target: hub,
                changeOrigin: true,
                configure: (proxy) => {
                  proxy.on('proxyReq', (req) => req.removeHeader('authorization'))
                },
              },
            }
          : undefined,
    },
    build: {
      sourcemap: false,
      chunkSizeWarningLimit: 900,
      rollupOptions: {
        output: {
          manualChunks: {
            echarts: ['echarts/core', 'echarts/charts', 'echarts/components', 'echarts/renderers'],
            react: ['react', 'react-dom', 'react-router'],
          },
        },
      },
    },
    test: {
      globals: true,
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      css: false,
    },
  }
})
