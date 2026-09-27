import { useState } from 'react'
import { Link } from 'react-router'
import { ApiError } from '../../api/client'
import { useChannels, useDeleteChannel, useTestChannel } from '../../api/queries'
import type { Channel } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { EmptyState, ErrorMessage, Loading, PageHeader, errorText } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'
import { channelTypes } from '../../lib/channelForm'

/** ChannelsPage lists a team's notification channels, with test and delete actions. */
export function ChannelsPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const channels = useChannels(t)
  const testChannel = useTestChannel(t)
  const deleteChannel = useDeleteChannel(t)
  const [testResults, setTestResults] = useState<Record<number, string>>({})
  const [confirming, setConfirming] = useState<Channel | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const test = (c: Channel) => {
    setTestResults((r) => ({ ...r, [c.id]: '…' }))
    testChannel.mutate(c.id, {
      onSuccess: () => setTestResults((r) => ({ ...r, [c.id]: 'Sent' })),
      onError: (err) => setTestResults((r) => ({ ...r, [c.id]: errorText(err) })),
    })
  }

  const confirmDelete = () => {
    if (!confirming) return
    setDeleteError(null)
    deleteChannel.mutate(confirming.id, {
      onSuccess: () => setConfirming(null),
      onError: (err) =>
        setDeleteError(
          err instanceof ApiError && err.status === 409
            ? `${err.detail} — it may still be used by a monitor or an escalation policy.`
            : errorText(err),
        ),
    })
  }

  return (
    <div>
      <PageHeader title="Channels">
        {can('editor') && (
          <Link to="new" className="btn-primary">
            Add channel
          </Link>
        )}
      </PageHeader>
      {channels.isPending && <Loading />}
      <ErrorMessage error={channels.error} />
      {channels.data && channels.data.length === 0 && (
        <EmptyState>
          No channels yet.{' '}
          {can('editor') && (
            <Link to="new" className="link">
              Add one
            </Link>
          )}
        </EmptyState>
      )}
      {channels.data && channels.data.length > 0 && (
        <div className="card divide-y divide-line overflow-hidden p-0">
          {channels.data.map((c) => (
            <div key={c.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{c.name}</span>
                  <span className="text-xs text-muted">
                    {channelTypes.find((ct) => ct.value === c.type)?.label ?? c.type}
                  </span>
                  {c.is_default && (
                    <span className="rounded bg-accent-soft px-1.5 py-0.5 text-xs text-accent-ink">Default</span>
                  )}
                </div>
                {c.config_error && <p className="mt-1 text-xs text-down">{c.config_error}</p>}
                {testResults[c.id] && <p className="mt-1 text-xs text-muted">{testResults[c.id]}</p>}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <button
                  type="button"
                  className="btn"
                  onClick={() => test(c)}
                  disabled={testChannel.isPending}
                >
                  Test
                </button>
                {can('editor') && (
                  <>
                    <Link to={`${c.id}/edit`} className="btn">
                      Edit
                    </Link>
                    <button
                      type="button"
                      className="btn-danger"
                      onClick={() => {
                        setDeleteError(null)
                        setConfirming(c)
                      }}
                    >
                      Delete
                    </button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
      <ConfirmDialog
        open={confirming !== null}
        title={`Delete ${confirming?.name ?? ''}?`}
        busy={deleteChannel.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setConfirming(null)}
      >
        <p>Deleting a channel fails if a monitor or escalation policy still uses it.</p>
        {deleteError && (
          <p role="alert" className="mt-2 text-down">
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}
