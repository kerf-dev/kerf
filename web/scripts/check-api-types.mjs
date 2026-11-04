// Fails if src/api/schema.d.ts is out of date with api/openapi/hub.yaml (run `npm run gen:api`).
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

const out = path.join(mkdtempSync(path.join(tmpdir(), 'ro-web-')), 'schema.d.ts')
execFileSync('npx', ['openapi-typescript', '../api/openapi/hub.yaml', '-o', out], { stdio: 'ignore' })
execFileSync('npx', ['prettier', '--write', out, '--config', '.prettierrc.json', '--log-level', 'warn'], {
  stdio: 'inherit',
})
if (readFileSync(out, 'utf8') !== readFileSync('src/api/schema.d.ts', 'utf8')) {
  console.error('src/api/schema.d.ts is stale: run `npm run gen:api`')
  process.exit(1)
}
console.log('src/api/schema.d.ts is up to date')
