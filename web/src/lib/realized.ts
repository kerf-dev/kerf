const dateOnly = new Intl.DateTimeFormat('en-GB', {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
  timeZone: 'UTC',
})

/** "Realized since 27 Oct 2026": the label of every realized amount (never "Estimated"). */
export function realizedLabel(since: string | undefined) {
  return since ? `Realized since ${dateOnly.format(Date.parse(since))}` : 'Realized'
}
