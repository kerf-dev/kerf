/// <reference types="vitest/config" />
import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

// Modes:
//   mock  (`npm run dev`, `make web-dev`)       – MSW serves the API in the browser, no hub needed.
//   live  (`npm run dev:live`, `make web-dev-live`) – /v1 is proxied to a real hub (default
//         http://localhost:18080, the WO-04 NodePort). RO_HUB_TOKEN, if set, is added as the bearer
//         token by the dev proxy, mirroring what nginx does in production; it never reaches the browser.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const hub = env.RO_HUB_URL || 'http://localhost:18080'
  const token = env.RO_HUB_TOKEN
  return {
    plugins: [react(), tailwindcss()],
    resolve: { alias: { '@': path.resolve(__dirname, './src') } },
    define: { __MOCK_API__: JSON.stringify(mode === 'mock') },
    server: {
      port: 5173,
      strictPort: false,
      proxy:
        mode === 'live'
          ? {
              '/v1/': {
                target: hub,
                changeOrigin: true,
                headers: token ? { Authorization: `Bearer ${token}` } : undefined,
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
