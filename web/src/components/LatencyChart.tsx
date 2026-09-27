import { useEffect, useRef } from 'react'
import uPlot from 'uplot'
import type { Heartbeat } from '../api/types'

const height = 220

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/** LatencyChart plots heartbeats (oldest→newest); down and maintenance points are gaps. */
export function LatencyChart({ beats }: { beats: Heartbeat[] }) {
  const wrap = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const xs = beats.map((b) => Date.parse(b.time) / 1000)
    const ys = beats.map((b) => (b.status === 'down' || b.status === 'maintenance' ? null : b.latency_ms))
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
          {
            label: 'Latency',
            stroke: cssVar('--accent') || '#C2185B',
            width: 1.5,
            spanGaps: false,
            points: { show: false },
          },
        ],
      },
      [xs, ys],
      el,
    )
    const ro = new ResizeObserver(() => plot.setSize({ width: el.clientWidth, height }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot.destroy()
    }
  }, [beats])

  return <div ref={wrap} className="w-full" style={{ minHeight: height }} />
}
