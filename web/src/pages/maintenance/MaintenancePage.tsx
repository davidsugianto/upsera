import { useState } from 'react'
import { Link } from 'react-router'
import { useDeleteMaintenance, useMaintenanceWindows } from '../../api/queries'
import type { MaintenanceWindow } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { EmptyState, ErrorMessage, Loading, PageHeader, formatTime } from '../../components/ui'
import { useNow } from '../../hooks/useNow'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'
import { phaseOf, type Phase } from '../../lib/maintenance'

const recurrenceLabel: Record<MaintenanceWindow['recurrence'], string> = {
  none: 'One-time',
  daily: 'Daily',
  weekly: 'Weekly',
}

const phaseLabel: Record<Phase, string> = { active: 'Active', scheduled: 'Scheduled', past: 'Past' }

/** PhaseMark pairs a word with a mark: hatched cobalt = active, outlined cobalt = scheduled, plain grey = past. */
function PhaseMark({ phase }: { phase: Phase }) {
  const mark =
    phase === 'active'
      ? 'bg-maint tone-hatch'
      : phase === 'scheduled'
        ? 'border border-maint'
        : 'border border-line-strong bg-surface-2'
  const ink = phase === 'past' ? 'text-muted' : 'text-maint-ink'
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full border border-line-strong px-2 py-0.5 text-xs font-medium ${ink}`}>
      <span aria-hidden="true" className={`size-2.5 shrink-0 rounded-full ${mark}`} />
      {phaseLabel[phase]}
    </span>
  )
}

/** MaintenancePage lists a team's maintenance windows. */
export function MaintenancePage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const windows = useMaintenanceWindows(t)
  const del = useDeleteMaintenance(t)
  const [confirming, setConfirming] = useState<MaintenanceWindow | null>(null)
  const now = useNow()

  if (windows.isPending) return <Loading />
  if (windows.error) return <ErrorMessage error={windows.error} />

  const list = windows.data

  return (
    <div>
      <PageHeader title="Maintenance windows">
        {can('editor') && (
          <Link to="new" className="btn-primary">
            Add window
          </Link>
        )}
      </PageHeader>

      {list.length === 0 ? (
        <EmptyState>
          No maintenance windows yet.{' '}
          {can('editor') && (
            <Link to="new" className="link">
              Create one
            </Link>
          )}
        </EmptyState>
      ) : (
        <ul className="card divide-y divide-line overflow-hidden">
          {list.map((w) => {
            const count = (w.monitor_ids ?? []).length
            const phase = phaseOf(w, now)
            return (
              <li key={w.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-base font-semibold">{w.name}</span>
                    {phase && <PhaseMark phase={phase} />}
                  </div>
                  <div className="mt-1 text-xs text-muted tabular-nums">
                    {formatTime(w.starts_at)} – {formatTime(w.ends_at)} · {recurrenceLabel[w.recurrence]} · {count}{' '}
                    monitor{count === 1 ? '' : 's'}
                  </div>
                </div>
                {can('editor') && (
                  <div className="flex items-center gap-2">
                    <Link to={`${w.id}/edit`} className="btn">
                      Edit
                    </Link>
                    <button type="button" className="btn-danger" onClick={() => setConfirming(w)}>
                      Delete
                    </button>
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}

      <ConfirmDialog
        open={!!confirming}
        title={`Delete "${confirming?.name}"?`}
        busy={del.isPending}
        onConfirm={() => {
          if (!confirming) return
          del.mutate(confirming.id, { onSuccess: () => setConfirming(null) })
        }}
        onCancel={() => setConfirming(null)}
      >
        This cannot be undone.
        {del.error && <ErrorMessage error={del.error} />}
      </ConfirmDialog>
    </div>
  )
}
