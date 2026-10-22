import type { Heartbeat } from '../api/types'
import { padBeats } from '../lib/beats'
import { statusBg } from './StatusBadge'

function describe(b: Heartbeat): string {
  const parts = [new Date(b.time).toLocaleString(), b.status]
  if (b.status === 'up' || b.status === 'pending' || b.status === 'degraded') parts.push(`${b.latency_ms} ms`)
  if (b.message) parts.push(b.message)
  return parts.join(' · ')
}

/**
 * HeartbeatBar renders the newest `slots` heartbeats (given oldest→newest) as ticks.
 * Ticks print their state: solid for up/down, dots for degraded, hatch for maintenance.
 */
export function HeartbeatBar({ beats, slots, className = '' }: { beats: Heartbeat[]; slots: number; className?: string }) {
  return (
    <div className={`flex h-7 items-stretch gap-[2px] ${className}`} role="img" aria-label="Recent heartbeats">
      {padBeats(beats, slots).map((b, i) => (
        <span
          key={b ? b.time : `empty-${i}`}
          className={`w-1.5 shrink-0 rounded-[2px] ${b ? statusBg(b.status) : 'border border-line bg-surface-2'}`}
          title={b ? describe(b) : 'No data'}
        />
      ))}
    </div>
  )
}
