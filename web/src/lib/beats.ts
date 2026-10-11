import type { Heartbeat } from '../api/types'

/** compareRegions orders region names: "local" first, then alphabetically. */
export function compareRegions(a: string, b: string): number {
  if (a === b) return 0
  if (a === 'local') return -1
  if (b === 'local') return 1
  return a < b ? -1 : 1
}

/** groupByRegion splits heartbeats by region (a missing region is "local"), keeping each group's order. */
export function groupByRegion(beats: Heartbeat[]): { region: string; beats: Heartbeat[] }[] {
  const byRegion = new Map<string, Heartbeat[]>()
  for (const b of beats) {
    const region = b.region || 'local'
    const list = byRegion.get(region)
    if (list) list.push(b)
    else byRegion.set(region, [b])
  }
  return [...byRegion.entries()]
    .sort(([a], [b]) => compareRegions(a, b))
    .map(([region, list]) => ({ region, beats: list }))
}

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
