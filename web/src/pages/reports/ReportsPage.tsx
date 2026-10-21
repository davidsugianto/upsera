import { useId, useState } from 'react'
import { useSLAReport } from '../../api/queries'
import type { SLARow } from '../../api/types'
import { EmptyState, ErrorMessage, Loading, PageHeader } from '../../components/ui'
import { useTeamId } from '../../hooks/useTeamId'
import { downloadCSV } from '../../lib/csv'
import { currentMonth, formatDowntime, formatUptimePct, reportCSV } from './report'

/** ReportsPage is the monthly SLA report: one row per monitor, printable and exportable as CSV. */
export function ReportsPage() {
  const t = useTeamId()
  const monthId = useId()
  const [month, setMonth] = useState(currentMonth())
  const report = useSLAReport(t, month)
  const r = report.data

  return (
    <div className="space-y-4">
      <PageHeader title="SLA report">
        <div className="flex items-center gap-2 print:hidden">
          <label htmlFor={monthId} className="text-sm font-medium">
            Month
          </label>
          <input
            id={monthId}
            type="month"
            className="input w-auto"
            value={month}
            onChange={(e) => e.target.value && setMonth(e.target.value)}
          />
          <button
            type="button"
            className="btn"
            disabled={!r}
            onClick={() => r && downloadCSV(`sla-${r.month}.csv`, reportCSV(r))}
          >
            Download CSV
          </button>
          <button type="button" className="btn" onClick={() => window.print()}>
            Print
          </button>
        </div>
      </PageHeader>

      <ErrorMessage error={report.error} />
      {report.isPending && <Loading />}
      {r && (
        <>
          {r.partial && (
            <p className="rounded-md border border-pend/40 bg-pend/10 px-3 py-2 text-sm">
              Partial month: {r.from} to {r.to} so far. The numbers change until the month ends.
            </p>
          )}
          {(r.rows ?? []).length === 0 ? (
            <EmptyState>No monitors yet.</EmptyState>
          ) : (
            <div className="card overflow-x-auto">
              <table className="report-table w-full text-sm">
                <caption className="px-4 py-3 text-left text-sm font-semibold">
                  SLA report for {r.month} ({r.from} to {r.to})
                </caption>
                <thead>
                  <tr className="border-y border-line text-left text-xs text-muted">
                    <th scope="col" className="px-4 py-2 font-medium">Monitor</th>
                    <th scope="col" className="px-4 py-2 text-right font-medium">Checks</th>
                    <th scope="col" className="px-4 py-2 text-right font-medium">Uptime</th>
                    <th scope="col" className="px-4 py-2 font-medium">SLO</th>
                    <th scope="col" className="px-4 py-2 text-right font-medium">Downtime</th>
                    <th scope="col" className="px-4 py-2 text-right font-medium">Alerts</th>
                    <th scope="col" className="px-4 py-2 text-right font-medium">p95</th>
                  </tr>
                </thead>
                <tbody>
                  {(r.rows ?? []).map((row) => (
                    <ReportRow key={row.monitor_id} row={row} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  )
}

function ReportRow({ row }: { row: SLARow }) {
  return (
    <tr className="border-b border-line last:border-0">
      <th scope="row" className="px-4 py-2 text-left font-medium">
        {row.monitor_name}
      </th>
      <td className="px-4 py-2 text-right tabular-nums">{row.checks.toLocaleString()}</td>
      <td className={`px-4 py-2 text-right tabular-nums ${row.uptime_pct === undefined ? 'text-muted' : ''}`}>
        {formatUptimePct(row.uptime_pct)}
      </td>
      <td className="px-4 py-2 whitespace-nowrap">
        {row.slo_target !== undefined && (
          <span className="inline-flex items-center gap-2">
            <span className="tabular-nums">{row.slo_target}%</span>
            {row.slo_met !== undefined && <MetBadge met={row.slo_met} />}
          </span>
        )}
      </td>
      <td className="px-4 py-2 text-right whitespace-nowrap tabular-nums">{formatDowntime(row.downtime_s)}</td>
      <td className="px-4 py-2 text-right tabular-nums">{row.alerts}</td>
      <td className="px-4 py-2 text-right whitespace-nowrap tabular-nums">
        {row.checks > 0 ? `${row.p95_latency_ms} ms` : '—'}
      </td>
    </tr>
  )
}

/** MetBadge says met or missed in words and a symbol, not by colour alone. */
function MetBadge({ met }: { met: boolean }) {
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium ${
        met ? 'border-up/50 text-ink' : 'border-down/50 text-down'
      }`}
    >
      <span aria-hidden="true">{met ? '✓' : '✗'}</span>
      <span>{met ? 'Met' : 'Missed'}</span>
    </span>
  )
}
