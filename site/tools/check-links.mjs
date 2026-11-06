// Checks every link in the site without a network: local files (HTML href/src/srcset, meta URLs on
// https://getkerf.dev, CSS url(), manifest icons, sitemap, robots.txt), in-page #fragments, and
// https://github.com/kerf-dev/kerf/{blob,tree}/main/<path> links against this checkout.
// Usage: node site/tools/check-links.mjs   (exit 1 on any broken link). No dependencies.
import { existsSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const site = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const repo = resolve(site, '..')
const ORIGIN = 'https://getkerf.dev'
const REPO_URL = /^https:\/\/github\.com\/kerf-dev\/kerf\/(?:blob|tree)\/main\/([^#?]+)/

const problems = []
let checked = 0

function idsOf(file) {
  const html = readFileSync(join(site, file), 'utf8')
  return new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]))
}

function sitePath(url) {
  if (url.startsWith(ORIGIN)) url = url.slice(ORIGIN.length) || '/'
  const path = url.split('#')[0].split('?')[0]
  return path === '/' || path === '' ? 'index.html' : path.replace(/^\//, '')
}

function check(from, url) {
  checked++
  const [, fragment] = url.split('#')
  if (REPO_URL.test(url)) {
    const p = url.match(REPO_URL)[1].replace(/\/$/, '')
    if (!existsSync(join(repo, decodeURIComponent(p)))) problems.push(`${from}: ${url} -> no ${p} in this checkout`)
    return
  }
  if (/^(https?:|mailto:)/.test(url) && !url.startsWith(ORIGIN)) return // other external links: not checked offline
  const target = url.startsWith('#') ? from : sitePath(url)
  const file = join(site, target)
  if (!existsSync(file) || !statSync(file).isFile()) {
    problems.push(`${from}: ${url} -> missing site/${target}`)
    return
  }
  if (fragment && target.endsWith('.html') && !idsOf(target).has(fragment)) {
    problems.push(`${from}: ${url} -> no id="${fragment}" in ${target}`)
  }
}

for (const page of ['index.html', '404.html']) {
  const html = readFileSync(join(site, page), 'utf8').replace(/<!--[\s\S]*?-->/g, '')
  for (const [, attr, value] of html.matchAll(/\s(href|src|content)="([^"]+)"/g)) {
    if (attr === 'content' && !value.startsWith(ORIGIN)) continue
    check(page, value)
  }
  for (const [, set] of html.matchAll(/\ssrcset="([^"]+)"/g)) {
    for (const candidate of set.split(',')) check(page, candidate.trim().split(/\s+/)[0])
  }
  for (const [, json] of html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)) {
    JSON.parse(json) // throws on invalid JSON-LD
    // "@id" values are node identifiers, not links.
    for (const [, key, url] of json.matchAll(/"([^"]+)":\s*"(https:\/\/[^"]+)"/g)) if (key !== '@id') check(`${page} (JSON-LD)`, url)
  }
}

const css = readFileSync(join(site, 'styles.css'), 'utf8')
for (const [, url] of css.matchAll(/url\("([^"]+)"\)/g)) check('styles.css', url)

const manifest = JSON.parse(readFileSync(join(site, 'site.webmanifest'), 'utf8'))
for (const icon of manifest.icons) check('site.webmanifest', icon.src)

for (const [, url] of readFileSync(join(site, 'sitemap.xml'), 'utf8').matchAll(/<loc>([^<]+)<\/loc>/g)) check('sitemap.xml', url)
for (const [, url] of readFileSync(join(site, 'robots.txt'), 'utf8').matchAll(/^Sitemap:\s*(\S+)/gm)) check('robots.txt', url)
for (const [, url] of readFileSync(join(site, 'llms.txt'), 'utf8').matchAll(/\]\((https:[^)]+)\)/g)) check('llms.txt', url)

if (readFileSync(join(site, 'CNAME'), 'utf8').trim() !== 'getkerf.dev') problems.push('CNAME: expected getkerf.dev')

if (problems.length) {
  console.error(problems.join('\n'))
  console.error(`${problems.length} broken of ${checked} links`)
  process.exit(1)
}
console.log(`${checked} links checked, none broken`)
