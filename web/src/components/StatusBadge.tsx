import type { Status } from '../api/types'

const bg: Record<Status, string> = { up: 'bg-up', down: 'bg-down', pending: 'bg-pend', maintenance: 'bg-maint' }
const text: Record<Status, string> = { up: 'text-up', down: 'text-down', pending: 'text-pend', maintenance: 'text-maint' }
const label: Record<Status, string> = { up: 'Up', down: 'Down', pending: 'Pending', maintenance: 'Maintenance' }

/** statusBg is the background utility for a status (null = no data). */
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
    return <span className="rounded-full border border-line px-2 py-0.5 text-xs text-muted">No data</span>
  }
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full border border-line px-2 py-0.5 text-xs font-medium ${text[status]}`}>
      <StatusDot status={status} />
      {label[status]}
    </span>
  )
}
