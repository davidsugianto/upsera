import { useEffect, useMemo, useRef } from 'react'
import uPlot from 'uplot'
import type { Heartbeat } from '../api/types'
import { groupByRegion } from '../lib/beats'

const height = 220

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/** Line colors by region index: theme variables first, fixed hues after. */
const palette = ['var(--accent)', 'var(--maint)', 'var(--pend)', '#8E5CF7', '#14B8A6', '#E8590C', '#6B7280']
const fallback = ['#C2185B', '#5B7CFA', '#F8A306', '#8E5CF7', '#14B8A6', '#E8590C', '#6B7280']

function regionColor(i: number): string {
  const c = palette[i % palette.length] ?? ''
  const name = /^var\((--[\w-]+)\)$/.exec(c)?.[1]
  return name ? cssVar(name) || (fallback[i % fallback.length] ?? '') : c
}

/**
 * LatencyChart plots heartbeats (oldest→newest); down and maintenance points are gaps
 * (degraded points are plotted: they are the slow responses).
 * With heartbeats from several regions it draws one line per region.
 */
export function LatencyChart({ beats }: { beats: Heartbeat[] }) {
  const wrap = useRef<HTMLDivElement>(null)
  const groups = useMemo(() => groupByRegion(beats), [beats])
  const multi = groups.length > 1

  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const tables = (groups.length > 0 ? groups : [{ region: '', beats: [] as Heartbeat[] }]).map(
      (g): uPlot.AlignedData => [
        g.beats.map((b) => Date.parse(b.time) / 1000),
        g.beats.map((b) => (b.status === 'down' || b.status === 'maintenance' ? null : b.latency_ms)),
      ],
    )
    // join aligns the regions onto one shared time axis, leaving nulls where a region has no point.
    const data = tables.length === 1 ? tables[0] : uPlot.join(tables)
    const muted = cssVar('--muted') || '#8E9C94'
    const line = cssVar('--line') || '#1F2833'
    const axis = { stroke: muted, grid: { stroke: line, width: 1 }, ticks: { stroke: line, width: 1 } }
    const plot = new uPlot(
      {
        width: el.clientWidth || 600,
        height,
        legend: { show: false },
        cursor: { drag: { x: false, y: false } },
        scales: { x: { time: true }, y: { range: (_u, _min, max) => [0, Math.max(10, (max ?? 0) * 1.15)] } },
        axes: [axis, { ...axis, values: (_u, v) => v.map((x) => `${x} ms`), size: 64 }],
        series: [
          {},
          ...tables.map((_, i): uPlot.Series => ({
            label: groups[i]?.region || 'Latency',
            stroke: regionColor(i),
            width: 1.5,
            spanGaps: false,
            points: { show: false },
          })),
        ],
      },
      data,
      el,
    )
    const ro = new ResizeObserver(() => plot.setSize({ width: el.clientWidth, height }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot.destroy()
    }
  }, [groups])

  return (
    <>
      <div ref={wrap} className="w-full" style={{ minHeight: height }} />
      {multi && (
        <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted" aria-label="Regions">
          {groups.map((g, i) => (
            <li key={g.region} className="inline-flex items-center gap-1.5">
              <span
                className="inline-block h-0.5 w-4 rounded"
                style={{ background: palette[i % palette.length] }}
                aria-hidden="true"
              />
              {g.region}
            </li>
          ))}
        </ul>
      )}
    </>
  )
}
