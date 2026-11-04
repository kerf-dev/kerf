/** Small deterministic PRNG helpers so the mock fleet looks the same on every load. */

export type Rng = () => number

export function mulberry32(seed: number): Rng {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export function hash(s: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

export const pick = <T>(r: Rng, xs: readonly T[]): T => xs[Math.floor(r() * xs.length)]!

export function weighted<T>(r: Rng, xs: readonly (readonly [T, number])[]): T {
  const total = xs.reduce((s, [, w]) => s + w, 0)
  let x = r() * total
  for (const [v, w] of xs) {
    x -= w
    if (x <= 0) return v
  }
  return xs[xs.length - 1]![0]
}

export const between = (r: Rng, lo: number, hi: number) => lo + r() * (hi - lo)

export function uuid(r: Rng): string {
  const h = () =>
    Math.floor(r() * 0x10000)
      .toString(16)
      .padStart(4, '0')
  const variant = ((Math.floor(r() * 4) + 8) as number).toString(16)
  return `${h()}${h()}-${h()}-4${h().slice(1)}-${variant}${h().slice(1)}-${h()}${h()}${h()}`
}
