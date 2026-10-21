import type { SLAReport } from '../../api/types'
import { toCSV } from '../../lib/csv'

/** formatDowntime renders seconds of downtime as "0m", "<1m", "23m", "1h 23m" or "2d 3h". */
export function formatDowntime(s: number): string {
  if (s <= 0) return '0m'
  if (s < 60) return '<1m'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** formatUptimePct renders an uptime percentage with two decimals ("no data" when absent). */
export function formatUptimePct(p: number | undefined): string {
  return p === undefined ? 'no data' : `${p.toFixed(2)}%`
}

/** currentMonth is YYYY-MM of d in the browser's time zone. */
export function currentMonth(d = new Date()): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

const header = ['Monitor', 'Checks', 'Uptime %', 'SLO target %', 'SLO', 'Downtime (s)', 'Alerts', 'p95 latency (ms)']

/** reportCSV is the report as CSV: a header row, then one row per monitor with the raw numbers. */
export function reportCSV(r: SLAReport): string {
  return toCSV([
    header,
    ...(r.rows ?? []).map((row) => [
      row.monitor_name,
      row.checks,
      row.uptime_pct,
      row.slo_target,
      row.slo_met === undefined ? '' : row.slo_met ? 'met' : 'missed',
      row.downtime_s,
      row.alerts,
      row.p95_latency_ms,
    ]),
  ])
}
