import { useState } from 'react'
import { Link } from 'react-router'
import { ApiError } from '../../api/client'
import { useChannels, useDeletePolicy, usePolicies } from '../../api/queries'
import type { Channel, EscalationPolicy } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { EmptyState, ErrorMessage, Loading, PageHeader, errorText, formatDuration } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'

function stepSummary(policy: EscalationPolicy, channels: Channel[]): string {
  const names: Record<number, string> = Object.fromEntries(channels.map((c) => [c.id, c.name]))
  const steps = policy.steps ?? []
  if (steps.length === 0) return 'No steps'
  const label = (ids: number[] | null) => (ids ?? []).map((id) => names[id] ?? `#${id}`).join(', ') || 'nobody'
  let out = label(steps[0]!.channel_ids)
  for (let i = 1; i < steps.length; i++) {
    out += ` → after ${formatDuration(steps[i - 1]!.delay_s)} → ${label(steps[i]!.channel_ids)}`
  }
  return out
}

/** PoliciesPage lists a team's escalation policies. */
export function PoliciesPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const policies = usePolicies(t)
  const channels = useChannels(t)
  const deletePolicy = useDeletePolicy(t)
  const [confirming, setConfirming] = useState<EscalationPolicy | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const confirmDelete = () => {
    if (!confirming) return
    setDeleteError(null)
    deletePolicy.mutate(confirming.id, {
      onSuccess: () => setConfirming(null),
      onError: (err) =>
        setDeleteError(
          err instanceof ApiError && err.status === 409
            ? `${err.detail} — a monitor still uses this policy.`
            : errorText(err),
        ),
    })
  }

  return (
    <div>
      <PageHeader title="Escalation policies">
        {can('editor') && (
          <Link to="new" className="btn-primary">
            Add policy
          </Link>
        )}
      </PageHeader>
      {(policies.isPending || channels.isPending) && <Loading />}
      <ErrorMessage error={policies.error ?? channels.error} />
      {policies.data && policies.data.length === 0 && (
        <EmptyState>
          No escalation policies yet.{' '}
          {can('editor') && (
            <Link to="new" className="link">
              Add one
            </Link>
          )}
        </EmptyState>
      )}
      {policies.data && channels.data && policies.data.length > 0 && (
        <div className="card divide-y divide-line overflow-hidden p-0">
          {policies.data.map((p) => (
            <div key={p.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <div className="font-medium">{p.name}</div>
                <div className="mt-0.5 text-xs text-muted">{stepSummary(p, channels.data)}</div>
              </div>
              {can('editor') && (
                <div className="flex shrink-0 items-center gap-2">
                  <Link to={`${p.id}/edit`} className="btn">
                    Edit
                  </Link>
                  <button
                    type="button"
                    className="btn-danger"
                    onClick={() => {
                      setDeleteError(null)
                      setConfirming(p)
                    }}
                  >
                    Delete
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      <ConfirmDialog
        open={confirming !== null}
        title={`Delete ${confirming?.name ?? ''}?`}
        busy={deletePolicy.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setConfirming(null)}
      >
        <p>Deleting fails if a monitor still uses this policy.</p>
        {deleteError && (
          <p role="alert" className="mt-2 text-down">
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}
