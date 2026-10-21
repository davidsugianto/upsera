import { describe, expect, it } from 'vitest'
import type { SLAReport } from '../../api/types'
import { currentMonth, formatDowntime, formatUptimePct, reportCSV } from './report'

describe('formatDowntime', () => {
  it.each([
    [0, '0m'],
    [30, '<1m'],
    [60, '1m'],
    [5 * 60 + 59, '5m'],
    [3600, '1h 0m'],
    [3600 + 23 * 60 + 10, '1h 23m'],
    [86400 + 2 * 3600, '1d 2h'],
    [3 * 86400, '3d 0h'],
  ])('%i s is %s', (s, want) => {
    expect(formatDowntime(s)).toBe(want)
  })
})

describe('formatUptimePct', () => {
  it('uses two decimals and says when there is no data', () => {
    expect(formatUptimePct(99.98765)).toBe('99.99%')
    expect(formatUptimePct(100)).toBe('100.00%')
    expect(formatUptimePct(undefined)).toBe('no data')
  })
})

describe('currentMonth', () => {
  it('is YYYY-MM of the given day', () => {
    expect(currentMonth(new Date(2026, 0, 31, 23, 59))).toBe('2026-01')
    expect(currentMonth(new Date(2026, 10, 1))).toBe('2026-11')
  })
})

describe('reportCSV', () => {
  it('has a header row and one row per monitor', () => {
    const report: SLAReport = {
      month: '2026-10',
      from: '2026-10-01',
      to: '2026-10-19',
      partial: true,
      rows: [
        {
          monitor_id: 1, monitor_name: 'api, "edge"', checks: 100, uptime_pct: 99.5, slo_target: 99.9, slo_met: false,
          downtime_s: 600, alerts: 2, p95_latency_ms: 120,
        },
        { monitor_id: 2, monitor_name: '=evil()', checks: 0, downtime_s: 0, alerts: 0, p95_latency_ms: 0 },
      ],
    }
    expect(reportCSV(report)).toBe(
      'Monitor,Checks,Uptime %,SLO target %,SLO,Downtime (s),Alerts,p95 latency (ms)\r\n' +
        '"api, ""edge""",100,99.5,99.9,missed,600,2,120\r\n' +
        "'=evil(),0,,,,0,0,0\r\n",
    )
  })
})
