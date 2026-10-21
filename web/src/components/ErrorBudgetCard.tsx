import { useEffect, useMemo, useRef } from 'react'
import uPlot from 'uplot'
import type { SLODetail, SLOFiring, SLOPoint } from '../api/types'

const chartHeight = 180

function cssVar(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback
}

function pct(v: number, digits = 0): string {
  return `${(v * 100).toFixed(digits)}%`
}

function burn(v: number | undefined): string {
  return v === undefined ? '—' : `${v.toFixed(1)}×`
}

const firingLabel: Record<Exclude<SLOFiring, 'none'>, string> = { fast: 'Burning fast', slow: 'Burning slow' }

/**
 * ErrorBudgetCard shows a monitor's SLO: target and window, SLI, the error
 * budget used and left (exhausted at 100% and beyond), the 1h / 6h burn rates,
 * which burn-rate alert is firing, and the budget burndown over the window.
 */
export function ErrorBudgetCard({ slo }: { slo: SLODetail }) {
  const { slo: s, status } = slo
  const consumed = status.budget_consumed
  const exhausted = consumed !== undefined && consumed >= 1
  const series = slo.series ?? []

  return (
    <section className="card p-4" aria-labelledby="error-budget-title">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 id="error-budget-title" className="text-sm font-semibold">
            Error budget
          </h2>
          <p className="text-xs text-muted">{`${s.target}% over ${s.window_days} days`}</p>
        </div>
        {status.firing !== 'none' && (
          <span className="inline-flex items-center gap-1 rounded-full border border-down/50 bg-down/10 px-2 py-0.5 text-xs font-medium text-down">
            <span aria-hidden="true">▲</span>
            <span>{firingLabel[status.firing]}</span>
          </span>
        )}
      </div>

      {status.sli === undefined || consumed === undefined ? (
        <p className="py-4 text-sm text-muted">No checks in the window yet, so there is no SLI to show.</p>
      ) : (
        <>
          <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
            <Stat label="SLI">{pct(status.sli, 3)}</Stat>
            <Stat label="Checks">{`${status.good.toLocaleString()} good of ${status.valid.toLocaleString()}`}</Stat>
            <Stat label="Burn rate 1h">
              <span data-burn="1h">{burn(status.burn_rate_1h)}</span>
            </Stat>
            <Stat label="Burn rate 6h">
              <span data-burn="6h">{burn(status.burn_rate_6h)}</span>
            </Stat>
          </dl>

          <div className="mt-4">
            <div className="mb-1 flex flex-wrap items-baseline justify-between gap-2 text-sm">
              <span className="font-medium">
                {`${pct(consumed)} used`}
                {exhausted && (
                  <span className="ml-2 rounded-full bg-down/10 px-2 py-0.5 text-xs font-medium text-down">Exhausted</span>
                )}
              </span>
              <span className="text-muted">{`${pct(Math.max(0, 1 - consumed))} remaining`}</span>
            </div>
            <div
              role="progressbar"
              aria-label="Error budget used"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(Math.min(consumed, 1) * 100)}
              aria-valuetext={`${pct(consumed)} used${exhausted ? ', exhausted' : ''}`}
              className="h-2 overflow-hidden rounded-full bg-line"
            >
              <div
                className={`h-full rounded-full ${exhausted ? 'bg-down' : consumed >= 0.75 ? 'bg-pend' : 'bg-up'}`}
                style={{ width: `${Math.min(consumed, 1) * 100}%` }}
              />
            </div>
          </div>

          {series.length > 0 && (
            <div className="mt-5">
              <h3 className="mb-2 text-xs font-medium text-muted">Budget used over the window (cumulative, % of budget)</h3>
              <BudgetBurndown points={series} />
              <details className="mt-2 text-xs">
                <summary className="cursor-pointer text-muted">Daily values</summary>
                <table className="mt-2 w-full">
                  <thead>
                    <tr className="text-left text-muted">
                      <th scope="col" className="py-1 font-medium">Day</th>
                      <th scope="col" className="py-1 text-right font-medium">SLI</th>
                      <th scope="col" className="py-1 text-right font-medium">Budget used</th>
                    </tr>
                  </thead>
                  <tbody>
                    {series.map((p) => (
                      <tr key={p.day} className="border-t border-line">
                        <td className="py-1">{p.day}</td>
                        <td className="py-1 text-right tabular-nums">{p.sli === undefined ? '—' : pct(p.sli, 3)}</td>
                        <td className="py-1 text-right tabular-nums">{pct(p.budget_consumed)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </details>
            </div>
          )}
        </>
      )}
    </section>
  )
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="mt-0.5 text-lg font-semibold tabular-nums">{children}</dd>
    </div>
  )
}

/**
 * BudgetBurndown plots the cumulative budget used per day (percent of the
 * budget) as one 2px line, with a dashed rule at 100% (exhausted). Themed and
 * resized like LatencyChart; the cursor shows each day's value.
 */
function BudgetBurndown({ points }: { points: SLOPoint[] }) {
  const wrap = useRef<HTMLDivElement>(null)
  const data = useMemo(
    (): uPlot.AlignedData => [
      points.map((p) => Date.parse(`${p.day}T00:00:00`) / 1000),
      points.map((p) => p.budget_consumed * 100),
      points.map(() => 100),
    ],
    [points],
  )

  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const muted = cssVar('--muted', '#8E9C94')
    const line = cssVar('--line', '#1F2833')
    const axis = { stroke: muted, grid: { stroke: line, width: 1 }, ticks: { stroke: line, width: 1 } }
    const plot = new uPlot(
      {
        width: el.clientWidth || 600,
        height: chartHeight,
        legend: { show: true, live: true },
        cursor: { drag: { x: false, y: false } },
        scales: {
          x: { time: true },
          y: { range: (_u, _min, max) => [0, Math.max(110, (max ?? 0) * 1.1)] },
        },
        axes: [axis, { ...axis, values: (_u, v) => v.map((x) => `${x}%`), size: 56 }],
        series: [
          { label: 'Day', value: (_u, v) => (v == null ? '—' : new Date(v * 1000).toLocaleDateString()) },
          {
            label: 'Budget used',
            stroke: cssVar('--accent', '#C2185B'),
            width: 2,
            points: { show: true, size: 6 },
            value: (_u, v) => (v == null ? '—' : `${v.toFixed(0)}%`),
          },
          {
            label: 'Exhausted',
            stroke: cssVar('--down', '#DC3545'),
            width: 1,
            dash: [4, 4],
            points: { show: false },
            value: () => '100%',
          },
        ],
      },
      data,
      el,
    )
    const ro = new ResizeObserver(() => plot.setSize({ width: el.clientWidth, height: chartHeight }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot.destroy()
    }
  }, [data])

  return <div ref={wrap} className="w-full" style={{ minHeight: chartHeight }} />
}
