import type { Status } from '../api/types'

// degraded (up but slow) shares pending's amber but keeps its own label and prints as halftone dots;
// maintenance prints as a diagonal hatch. Up, down and pending are solid.
const bg: Record<Status, string> = {
  up: 'bg-up',
  down: 'bg-down',
  pending: 'bg-pend',
  maintenance: 'bg-maint tone-hatch',
  degraded: 'bg-pend tone-dots',
}
const text: Record<Status, string> = {
  up: 'text-up-ink',
  down: 'text-down-ink',
  pending: 'text-pend-ink',
  maintenance: 'text-maint-ink',
  degraded: 'text-pend-ink',
}
const label: Record<Status, string> = {
  up: 'Up',
  down: 'Down',
  pending: 'Pending',
  maintenance: 'Maintenance',
  degraded: 'Degraded',
}

/** statusBg is the full fill class string for a status, pattern included (null = no data). */
export function statusBg(s: Status | null | undefined): string {
  return s ? bg[s] : 'bg-line'
}

export function StatusDot({ status, className = '' }: { status: Status | null | undefined; className?: string }) {
  return (
    <span
      className={`inline-block size-2.5 shrink-0 rounded-full ${statusBg(status)} ${className}`}
      title={status ? label[status] : 'No data'}
      aria-label={status ? label[status] : 'No data'}
    />
  )
}

export function StatusBadge({ status }: { status: Status | null | undefined }) {
  if (!status) {
    return <span className="inline-flex items-center rounded-full border border-line-strong px-2 py-0.5 text-xs text-muted">No data</span>
  }
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full border border-line-strong bg-surface px-2 py-0.5 text-xs font-medium ${text[status]}`}>
      <StatusDot status={status} />
      {label[status]}
    </span>
  )
}
