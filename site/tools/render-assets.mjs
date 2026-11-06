// Renders the website's generated assets into site/ from the brand sources, with the web UI's Playwright Chromium:
//   assets/shots/<name>-<theme>-<width>.webp  the top 1440 x 900 of docs/screenshots/<name>-<theme>.png
//   icons: favicon.ico (32), icon-192.png, icon-512.png, apple-touch-icon.png (180, on a light tile)
//   social-preview.png  copy of docs/branding/social-preview.png (Open Graph and Twitter card)
//   fonts/  Geist and Geist Mono (latin, variable weight) from @fontsource-variable, with the OFL licence
// Usage: make web-install && node site/tools/render-assets.mjs   (KERF_WEB_DIR=<path to web/> to use another install)
// The outputs are committed; rerun after docs/screenshots or the logo change.
import { createRequire } from 'node:module'
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const site = resolve(here, '..')
const repo = resolve(site, '..')
const webDir = process.env.KERF_WEB_DIR ? resolve(process.env.KERF_WEB_DIR) : join(repo, 'web')
const require = createRequire(join(webDir, 'package.json'))
const { chromium } = require('@playwright/test')

const shots = ['overview', 'opportunities', 'workload-rolled-back', 'cluster-readiness', 'admin-policy-editor']
const widths = { overview: [1440, 720], default: [1200, 640] }
const out = (p) => { mkdirSync(dirname(join(site, p)), { recursive: true }); return join(site, p) }

const browser = await chromium.launch()
const page = await browser.newPage()

// Crop the top 1440 x 900 of a screenshot, scale it to `width` and encode WebP in the page.
async function webp(src, width, quality = 0.8) {
  const b64 = await page.evaluate(async ({ src, width, quality }) => {
    const img = new Image()
    img.src = src
    await img.decode()
    const h = Math.round((width * 900) / 1440)
    const c = document.createElement('canvas')
    c.width = width
    c.height = h
    const ctx = c.getContext('2d')
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(img, 0, 0, 1440, 900, 0, 0, width, h)
    const blob = await new Promise((r) => c.toBlob(r, 'image/webp', quality))
    const buf = new Uint8Array(await blob.arrayBuffer())
    let s = ''
    for (const b of buf) s += String.fromCharCode(b)
    return btoa(s)
  }, { src, width, quality })
  return Buffer.from(b64, 'base64')
}

for (const name of shots) {
  for (const theme of ['light', 'dark']) {
    // A data: URL, so the canvas stays untainted (a file:// image can't be exported).
    const src = `data:image/png;base64,${readFileSync(join(repo, `docs/screenshots/${name}-${theme}.png`)).toString('base64')}`
    for (const w of widths[name] ?? widths.default) {
      const file = `assets/shots/${name}-${theme}-${w}.webp`
      const buf = await webp(src, w)
      writeFileSync(out(file), buf)
      console.log(`wrote site/${file} (${(buf.length / 1024).toFixed(0)} KiB)`)
    }
  }
}

// Icons from the mark. Transparent PNGs for the manifest; the Apple touch icon sits on a light tile.
const mark = readFileSync(join(repo, 'docs/branding/kerf-mark-on-light.svg'), 'utf8')
async function icon(size, { tile = false } = {}) {
  const p = await browser.newPage({ viewport: { width: size, height: size } })
  const pad = tile ? Math.round(size * 0.16) : Math.round(size * 0.06)
  await p.setContent(`<!doctype html><style>*{margin:0}html,body{width:${size}px;height:${size}px;overflow:hidden;background:${tile ? '#f9fafb' : 'transparent'}}
    div{box-sizing:border-box;width:100%;height:100%;padding:${pad}px}svg{width:100%;height:100%;display:block}</style><div>${mark}</div>`)
  const buf = await p.screenshot({ omitBackground: !tile })
  await p.close()
  return buf
}
for (const [file, size, opts] of [['icon-192.png', 192], ['icon-512.png', 512], ['apple-touch-icon.png', 180, { tile: true }]]) {
  writeFileSync(out(file), await icon(size, opts))
  console.log(`wrote site/${file}`)
}
// favicon.ico: one 32 x 32 PNG in an ICO container (every current browser reads PNG-in-ICO).
const png32 = await icon(32)
const ico = Buffer.alloc(22)
ico.writeUInt16LE(0, 0); ico.writeUInt16LE(1, 2); ico.writeUInt16LE(1, 4) // reserved, type icon, 1 image
ico.writeUInt8(32, 6); ico.writeUInt8(32, 7); ico.writeUInt8(0, 8); ico.writeUInt8(0, 9) // 32 x 32, no palette
ico.writeUInt16LE(1, 10); ico.writeUInt16LE(32, 12) // planes, bits per pixel
ico.writeUInt32LE(png32.length, 14); ico.writeUInt32LE(22, 18) // size, offset
writeFileSync(out('favicon.ico'), Buffer.concat([ico, png32]))
console.log('wrote site/favicon.ico')

await browser.close()

copyFileSync(join(repo, 'docs/branding/social-preview.png'), out('social-preview.png'))
console.log('wrote site/social-preview.png')

const fonts = join(webDir, 'node_modules/@fontsource-variable')
for (const [pkg, file] of [['geist', 'geist-latin-wght-normal.woff2'], ['geist-mono', 'geist-mono-latin-wght-normal.woff2']]) {
  copyFileSync(join(fonts, pkg, 'files', file), out(`fonts/${file}`))
  console.log(`wrote site/fonts/${file}`)
}
copyFileSync(join(fonts, 'geist', 'LICENSE'), out('fonts/OFL.txt'))
console.log('wrote site/fonts/OFL.txt')
