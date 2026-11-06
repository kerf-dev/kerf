// Checks every link of the assembled website (getkerf.dev from site/, and the documentation under /docs/) without
// a network: local files and #fragments in every HTML page, https://getkerf.dev/... URLs, and
// https://github.com/kerf-dev/kerf/{blob,tree}/<ref>/<path> links against this checkout (ref main) or the git
// tag (any other ref). Other external links are not checked offline.
// Usage: node tools/docs/check-links.mjs <assembled-site-dir>   (exit 1 on any broken link). No dependencies.
import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const root = resolve(process.argv[2] ?? '_site')
if (!existsSync(root)) {
  console.error(`usage: node tools/docs/check-links.mjs <assembled-site-dir> (no such directory: ${root})`)
  process.exit(2)
}
const ORIGIN = 'https://getkerf.dev'
const REPO_URL = /^https:\/\/github\.com\/kerf-dev\/kerf\/(blob|tree)\/([^/]+)\/([^#?]*)/

function* htmlFiles(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) yield* htmlFiles(p)
    else if (name.endsWith('.html')) yield p
  }
}

const idCache = new Map()
function idsOf(file) {
  if (!idCache.has(file)) {
    const html = readFileSync(file, 'utf8')
    idCache.set(file, new Set([...html.matchAll(/\s(?:id|name)="([^"]+)"/g)].map((m) => m[1])))
  }
  return idCache.get(file)
}

const gitCache = new Map()
function inGit(ref, path) {
  const key = `${ref}:${path}`
  if (!gitCache.has(key)) {
    let ok = true
    try {
      execFileSync('git', ['cat-file', '-e', key], { cwd: repo, stdio: 'ignore' })
    } catch {
      ok = false
    }
    gitCache.set(key, ok)
  }
  return gitCache.get(key)
}

function targetFile(urlPath, fromFile) {
  let p = decodeURIComponent(urlPath)
  const base = p.startsWith('/') ? root : dirname(fromFile)
  const abs = resolve(base, p.replace(/^\//, ''))
  if (abs !== root && !abs.startsWith(root + sep)) return null
  for (const candidate of [abs, join(abs, 'index.html'), `${abs}.html`]) {
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate
  }
  return null
}

const problems = []
let checked = 0

function check(from, url) {
  checked++
  if (/^(mailto:|tel:|javascript:|data:)/.test(url)) return
  const gh = url.match(REPO_URL)
  if (gh) {
    const [, , ref, path] = gh
    const p = decodeURIComponent(path).replace(/\/$/, '')
    const ok = ref === 'main' ? existsSync(join(repo, p)) : inGit(ref, p)
    if (!ok) problems.push(`${relative(root, from)}: ${url} -> no ${p} at ${ref}`)
    return
  }
  if (/^https?:\/\//.test(url) && !url.startsWith(ORIGIN)) return
  if (url.startsWith('//')) return
  if (url.startsWith(ORIGIN)) url = url.slice(ORIGIN.length) || '/'
  const [rest, fragment] = url.split('#')
  const [pathPart] = rest.split('?')
  const target = pathPart === '' ? from : targetFile(pathPart, from)
  if (!target) {
    problems.push(`${relative(root, from)}: ${url} -> not found in the site`)
    return
  }
  if (fragment && target.endsWith('.html') && !idsOf(target).has(decodeURIComponent(fragment))) {
    problems.push(`${relative(root, from)}: ${url} -> no id="${fragment}" in ${relative(root, target)}`)
  }
}

let pages = 0
for (const file of htmlFiles(root)) {
  // GitHub Pages serves only the 404 page at the root of the site; the ones MkDocs writes per version are never shown.
  if (/\/docs\/[^/]+\/404\.html$/.test(file.split(sep).join('/'))) continue
  pages++
  const html = readFileSync(file, 'utf8').replace(/<!--[\s\S]*?-->/g, '').replace(/<(script|style)[\s\S]*?<\/\1>/g, '')
  for (const [, attr, value] of html.matchAll(/\s(href|src)="([^"]+)"/g)) {
    // Navigation of the version selector is filled in by script; the redirect pages of aliases point at siblings.
    check(file, value.replace(/&amp;/g, '&'))
  }
}

if (problems.length) {
  console.error([...new Set(problems)].join('\n'))
  console.error(`${new Set(problems).size} broken of ${checked} links in ${pages} pages`)
  process.exit(1)
}
console.log(`${checked} links in ${pages} pages: all resolve`)
