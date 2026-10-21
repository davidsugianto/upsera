import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SLAReport } from '../../api/types'
import { ReportsPage } from './ReportsPage'

const reportState: { data?: SLAReport; isPending: boolean; error: unknown; month?: string } = { isPending: false, error: null }
vi.mock('../../api/queries', () => ({
  useSLAReport: (_t: number, month: string) => {
    reportState.month = month
    return reportState
  },
}))
const download = vi.fn()
vi.mock('../../lib/csv', async (orig) => ({ ...(await orig<typeof import('../../lib/csv')>()), downloadCSV: (...a: unknown[]) => download(...a) }))


const report: SLAReport = {
  month: '2026-09',
  from: '2026-09-01',
  to: '2026-09-30',
  partial: false,
  rows: [
    { monitor_id: 1, monitor_name: 'api', checks: 1000, uptime_pct: 99.95, slo_target: 99.9, slo_met: true, downtime_s: 0, alerts: 0, p95_latency_ms: 80 },
    { monitor_id: 2, monitor_name: 'db', checks: 500, uptime_pct: 98.123, slo_target: 99.9, slo_met: false, downtime_s: 5000, alerts: 3, p95_latency_ms: 12 },
    { monitor_id: 3, monitor_name: 'paused', checks: 0, downtime_s: 0, alerts: 0, p95_latency_ms: 0 },
  ],
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/t/7/reports']}>
      <Routes>
        <Route path="/t/:teamId/reports" element={<ReportsPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('ReportsPage', () => {
  beforeEach(() => {
    reportState.data = report
    reportState.isPending = false
    reportState.error = null
    download.mockReset()
  })
  afterEach(cleanup)

  it('renders one row per monitor with met/missed and no data', () => {
    renderPage()
    const table = screen.getByRole('table', { name: /SLA report/ })
    const rows = within(table).getAllByRole('row')
    expect(rows).toHaveLength(4) // header + 3
    const api = within(rows[1]!)
    expect(api.getByText('api')).toBeInTheDocument()
    expect(api.getByText('99.95%')).toBeInTheDocument()
    expect(api.getByText('Met')).toBeInTheDocument()
    const db = within(rows[2]!)
    expect(db.getByText('98.12%')).toBeInTheDocument()
    expect(db.getByText('Missed')).toBeInTheDocument()
    expect(db.getByText('1h 23m')).toBeInTheDocument()
    const paused = within(rows[3]!)
    expect(paused.getByText('no data')).toBeInTheDocument()
    expect(paused.queryByText(/Met|Missed/)).not.toBeInTheDocument()
    expect(screen.queryByText(/partial month/i)).not.toBeInTheDocument()
  })

  it('defaults to the current month and switches months', () => {
    renderPage()
    const picker = screen.getByLabelText('Month') as HTMLInputElement
    const now = new Date()
    expect(picker.value).toBe(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`)
    fireEvent.change(picker, { target: { value: '2026-09' } })
    expect(reportState.month).toBe('2026-09')
  })

  it('says when the month is partial', () => {
    reportState.data = { ...report, partial: true, to: '2026-09-15' }
    renderPage()
    expect(screen.getByText(/partial month/i)).toBeInTheDocument()
  })

  it('downloads the CSV', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Download CSV' }))
    expect(download).toHaveBeenCalledTimes(1)
    const [name, content] = download.mock.calls[0] as [string, string]
    expect(name).toBe('sla-2026-09.csv')
    expect(content.split('\r\n')[0]).toBe('Monitor,Checks,Uptime %,SLO target %,SLO,Downtime (s),Alerts,p95 latency (ms)')
    expect(content).toContain('db,500,98.123,99.9,missed,5000,3,12')
  })

  it('shows errors', () => {
    reportState.data = undefined
    reportState.error = new Error('boom')
    renderPage()
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
  })
})
