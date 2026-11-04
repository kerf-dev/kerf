/**
 * A small YAML emitter for JSON-shaped values (policy export, diffs). Not a general YAML library:
 * it only needs to produce what `kubectl apply` reads back as the same JSON.
 */
type Json = string | number | boolean | null | undefined | Json[] | { [k: string]: Json }

const PLAIN = /^[A-Za-z0-9_./][A-Za-z0-9_ ./*@+-]*$/
const RESERVED = /^(true|false|yes|no|on|off|null|~|y|n)$/i
const NUMERIC = /^[-+]?(\d[\d_]*)?(\.\d+)?([eE][-+]?\d+)?$|^0x[0-9a-f]+$|^0o[0-7]+$/i

function scalar(v: string | number | boolean | null): string {
  if (v === null) return 'null'
  if (typeof v !== 'string') return String(v)
  if (v === '' || !PLAIN.test(v) || RESERVED.test(v) || NUMERIC.test(v) || v !== v.trim() || / #|: /.test(v))
    return JSON.stringify(v)
  return v
}

const isObj = (v: Json): v is { [k: string]: Json } => !!v && typeof v === 'object' && !Array.isArray(v)

function emit(v: Json, indent: number, out: string[], prefix: string) {
  const pad = ' '.repeat(indent)
  if (Array.isArray(v)) {
    if (!v.length) return void out.push(`${prefix}[]`)
    if (prefix) out.push(prefix.trimEnd())
    for (const item of v) {
      if (isObj(item) && Object.keys(item).length) {
        const lines: string[] = []
        emitObject(item, indent + 2, lines)
        out.push(`${pad}- ${lines[0]!.trimStart()}`, ...lines.slice(1))
      } else emit(item, indent + 2, out, `${pad}- `)
    }
    return
  }
  if (isObj(v)) {
    const keys = Object.keys(v).filter((k) => v[k] !== undefined)
    if (!keys.length) return void out.push(`${prefix}{}`)
    if (prefix) out.push(prefix.trimEnd())
    emitObject(v, indent, out)
    return
  }
  if (typeof v === 'string' && v.includes('\n')) {
    out.push(`${prefix}|${v.endsWith('\n') ? '' : '-'}`)
    for (const line of v.replace(/\n$/, '').split('\n')) out.push(line ? `${pad}  ${line}` : '')
    return
  }
  out.push(`${prefix}${scalar(v ?? null)}`)
}

function emitObject(o: { [k: string]: Json }, indent: number, out: string[]) {
  const pad = ' '.repeat(indent)
  for (const [k, val] of Object.entries(o)) {
    if (val === undefined) continue
    const key = `${pad}${scalar(k)}:`
    if (
      (Array.isArray(val) && val.length) ||
      (isObj(val) && Object.keys(val).some((x) => val[x] !== undefined))
    ) {
      out.push(key)
      if (Array.isArray(val)) emit(val, indent, out, '')
      else emitObject(val, indent + 2, out)
    } else emit(val, indent + 2, out, `${key} `)
  }
}

export function toYaml(v: unknown): string {
  const out: string[] = []
  emit(v as Json, 0, out, '')
  return out.join('\n') + '\n'
}
