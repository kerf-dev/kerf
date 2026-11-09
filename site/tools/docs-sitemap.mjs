// Writes <site>/docs/sitemap.xml, the one documentation sitemap that robots.txt advertises.
// mike publishes a sitemap per version (/docs/dev/, /docs/0.1/, ...) and none at /docs/. This takes the version that
// the `latest` alias points at (mike's versions.json), so the sitemap lists only the pages of the current release:
// never dev (noindex), never an older minor. The per-build <lastmod> (the build date, the same for every page)
// carries no information, so it is dropped.
// Usage: node site/tools/docs-sitemap.mjs <assembled-site-dir>     (no dependencies)
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const root = resolve(process.argv[2] ?? '_site')
const docs = join(root, 'docs')
const versions = JSON.parse(readFileSync(join(docs, 'versions.json'), 'utf8'))
const current = versions.find((v) => v.aliases.includes('latest')) ?? versions.find((v) => v.version !== 'dev')
if (!current) throw new Error('docs/versions.json lists no released version to advertise')

const source = join(docs, current.version, 'sitemap.xml')
if (!existsSync(source)) throw new Error(`${source} does not exist`)
const urls = [...readFileSync(source, 'utf8').matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1])
const prefix = `https://getkerf.dev/docs/${current.version}/`
const stray = urls.filter((u) => !u.startsWith(prefix))
if (!urls.length || stray.length) throw new Error(`unexpected URLs in ${source}: ${stray.join(', ') || 'none found'}`)

const xml = ['<?xml version="1.0" encoding="UTF-8"?>', '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">',
  ...urls.map((u) => `  <url><loc>${u}</loc></url>`), '</urlset>', ''].join('\n')
writeFileSync(join(docs, 'sitemap.xml'), xml)
console.log(`docs/sitemap.xml: ${urls.length} URLs of version ${current.version}`)
