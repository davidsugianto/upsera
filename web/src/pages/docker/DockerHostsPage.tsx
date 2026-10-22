import { useState } from 'react'
import { Link } from 'react-router'
import { ApiError } from '../../api/client'
import { useDeleteDockerHost, useDockerHosts, useTestDockerHost } from '../../api/queries'
import type { DockerHost } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { EmptyState, ErrorMessage, Loading, PageHeader, errorText } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'
import { insecureWarning } from '../../lib/dockerHostForm'

/** DockerHostsPage lists a team's Docker hosts, with connection test and delete actions. */
export function DockerHostsPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const hosts = useDockerHosts(t)
  const testHost = useTestDockerHost(t)
  const deleteHost = useDeleteDockerHost(t)
  const [testResults, setTestResults] = useState<Record<number, string>>({})
  const [confirming, setConfirming] = useState<DockerHost | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const test = (h: DockerHost) => {
    setTestResults((r) => ({ ...r, [h.id]: '…' }))
    testHost.mutate(h.id, {
      onSuccess: () => setTestResults((r) => ({ ...r, [h.id]: 'Connected' })),
      onError: (err) => setTestResults((r) => ({ ...r, [h.id]: errorText(err) })),
    })
  }

  const confirmDelete = () => {
    if (!confirming) return
    setDeleteError(null)
    deleteHost.mutate(confirming.id, {
      onSuccess: () => setConfirming(null),
      onError: (err) =>
        setDeleteError(
          err instanceof ApiError && err.status === 409
            ? `${err.detail} — remove or edit its monitors first.`
            : errorText(err),
        ),
    })
  }

  return (
    <div>
      <PageHeader title="Docker hosts">
        {can('editor') && (
          <Link to="new" className="btn-primary">
            Add Docker host
          </Link>
        )}
      </PageHeader>
      {hosts.isPending && <Loading />}
      <ErrorMessage error={hosts.error} />
      {hosts.data && hosts.data.length === 0 && (
        <EmptyState>
          No Docker hosts yet.{' '}
          {can('editor') && (
            <Link to="new" className="link">
              Add one
            </Link>
          )}
        </EmptyState>
      )}
      {hosts.data && hosts.data.length > 0 && (
        <div className="card divide-y divide-line overflow-hidden">
          {hosts.data.map((h) => (
            <div key={h.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{h.name}</span>
                  <span className="font-mono text-xs text-muted">{h.endpoint}</span>
                  {h.tls.ca || h.tls.cert || h.tls.key ? (
                    <span className="rounded border border-line-strong px-1.5 py-0.5 text-xs text-muted">TLS</span>
                  ) : null}
                </div>
                {h.config_error && <p className="mt-1 text-xs text-down-ink">{h.config_error}</p>}
                {insecureWarning({
                  endpoint: h.endpoint,
                  ca: h.tls.ca,
                  cert: h.tls.cert,
                  key: h.tls.key,
                }) && <p className="mt-1 text-xs text-muted">No TLS</p>}
                {testResults[h.id] && <p className="mt-1 text-xs text-muted">{testResults[h.id]}</p>}
              </div>
              {can('editor') && (
                <div className="flex shrink-0 items-center gap-2">
                  <button type="button" className="btn" onClick={() => test(h)} disabled={testHost.isPending}>
                    Test
                  </button>
                  <Link to={`${h.id}/edit`} className="btn">
                    Edit
                  </Link>
                  <button
                    type="button"
                    className="btn-danger"
                    onClick={() => {
                      setDeleteError(null)
                      setConfirming(h)
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
        busy={deleteHost.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setConfirming(null)}
      >
        <p>Deleting a Docker host fails while a monitor still watches one of its containers.</p>
        {deleteError && (
          <p role="alert" className="mt-2 text-down-ink">
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}
