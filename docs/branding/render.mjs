// Renders the raster brand assets from their sources with the web UI's Playwright Chromium:
//   social-preview.png  1280 x 640, from social-preview.html (GitHub social preview)
//   hero-light.png, hero-dark.png  the README hero, the top of docs/screenshots/overview-*.png
//   kerf-avatar.png  512 x 512, the mark on a light tile (GitHub organization, Artifact Hub)
// Usage: make web-install && node docs/branding/render.mjs   (refresh docs/screenshots first: make web-screenshots)
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const require = createRequire(join(here, '../../web/package.json'))
const { chromium } = require('@playwright/test')

const page = (html) => `<!doctype html><html><head><style>*{margin:0}html,body{overflow:hidden}</style></head><body>${html}</body></html>`
const browser = await chromium.launch()

async function shot(out, width, height, load) {
  const p = await browser.newPage({ viewport: { width, height } })
  await load(p)
  await p.waitForLoadState('networkidle')
  await p.evaluate(() => document.fonts.ready)
  await p.screenshot({ path: join(here, out) })
  await p.close()
  console.log(`wrote docs/branding/${out} (${width}x${height})`)
}

await shot('social-preview.png', 1280, 640, (p) => p.goto(pathToFileURL(join(here, 'social-preview.html')).href))

for (const theme of ['light', 'dark']) {
  const src = pathToFileURL(join(here, `../screenshots/overview-${theme}.png`)).href
  await shot(`hero-${theme}.png`, 1440, 900, async (p) => {
    await p.goto(pathToFileURL(join(here, 'social-preview.html')).href) // a file:// origin, so the screenshot loads
    await p.setContent(page(`<img src="${src}" style="display:block;width:1440px">`))
  })
}

await shot('kerf-avatar.png', 512, 512, async (p) => {
  await p.goto(pathToFileURL(join(here, 'social-preview.html')).href)
  const mark = pathToFileURL(join(here, 'kerf-mark-on-light.svg')).href
  await p.setContent(
    page(`<div style="width:512px;height:512px;display:grid;place-items:center;background:#f9fafb">
      <img src="${mark}" style="width:320px;height:320px"></div>`),
  )
})

await browser.close()
