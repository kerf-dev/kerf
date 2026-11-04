/** Points of a step series where the value differs from the previous sample (the first point excluded). */
export function stepChanges(
  points: readonly (readonly number[])[],
): { t: number; from: number; to: number }[] {
  const out: { t: number; from: number; to: number }[] = []
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1]![1]!
    const b = points[i]![1]!
    if (a !== b) out.push({ t: points[i]![0]!, from: a, to: b })
  }
  return out
}
