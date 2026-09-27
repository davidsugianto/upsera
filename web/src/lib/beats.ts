/** padBeats keeps the newest n of beats (oldest→newest) and left-pads with null to length n. */
export function padBeats<T>(beats: T[], n: number): (T | null)[] {
  const kept = beats.slice(Math.max(0, beats.length - n))
  return [...Array<null>(n - kept.length).fill(null), ...kept]
}

/** formatUptime renders an uptime percentage: null → '—', 100 → '100%', else 2 decimals. */
export function formatUptime(p: number | null | undefined): string {
  if (p === null || p === undefined) return '—'
  if (p >= 100) return '100%'
  const s = p.toFixed(2)
  return s === '100.00' ? '99.99%' : `${s}%`
}
