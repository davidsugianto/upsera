import { useState } from 'react'
import { Link } from 'react-router'
import { useDeleteMaintenance, useMaintenanceWindows } from '../../api/queries'
import type { MaintenanceWindow } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { EmptyState, ErrorMessage, Loading, PageHeader, formatTime } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'

const recurrenceLabel: Record<MaintenanceWindow['recurrence'], string> = {
  none: 'One-time',
  daily: 'Daily',
  weekly: 'Weekly',
}

/** MaintenancePage lists a team's maintenance windows. */
export function MaintenancePage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const windows = useMaintenanceWindows(t)
  const del = useDeleteMaintenance(t)
  const [confirming, setConfirming] = useState<MaintenanceWindow | null>(null)

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
        <div className="card divide-y divide-line">
          {list.map((w) => {
            const count = (w.monitor_ids ?? []).length
            return (
              <div key={w.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                <div>
                  <div className="font-medium">{w.name}</div>
                  <div className="text-sm text-muted">
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
              </div>
            )
          })}
        </div>
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
