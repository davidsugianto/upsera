import { useState } from 'react'
import { Link } from 'react-router'
import { useDeleteStatusPage, useStatusPages } from '../../api/queries'
import type { StatusPage } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { EmptyState, ErrorMessage, Loading, PageHeader, errorText } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'

/** StatusPagesPage lists a team's public status pages. */
export function StatusPagesPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const pages = useStatusPages(t)
  const deletePage = useDeleteStatusPage(t)
  const [confirming, setConfirming] = useState<StatusPage | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const confirmDelete = () => {
    if (!confirming) return
    setDeleteError(null)
    deletePage.mutate(confirming.id, {
      onSuccess: () => setConfirming(null),
      onError: (err) => setDeleteError(errorText(err)),
    })
  }

  return (
    <div>
      <PageHeader title="Status pages">
        {can('editor') && (
          <Link to="new" className="btn-primary">
            Create status page
          </Link>
        )}
      </PageHeader>
      <p className="mb-4 max-w-2xl text-sm text-muted">
        Public pages that show the health of the monitors you choose, with incident updates for your customers.
      </p>
      {pages.isPending && <Loading />}
      <ErrorMessage error={pages.error} />
      {pages.data && pages.data.length === 0 && (
        <EmptyState>
          No status pages yet.{' '}
          {can('editor') && (
            <Link to="new" className="link">
              Create one
            </Link>
          )}
        </EmptyState>
      )}
      {pages.data && pages.data.length > 0 && (
        <ul className="card divide-y divide-line overflow-hidden p-0">
          {pages.data.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-base font-semibold">{p.title}</span>
                  {p.published ? (
                    <span className="rounded-full border border-up/50 px-2 py-0.5 text-xs text-up-ink"><span aria-hidden="true">● </span>Published</span>
                  ) : (
                    <span className="rounded-full border border-line-strong px-2 py-0.5 text-xs text-muted"><span aria-hidden="true">○ </span>Draft</span>
                  )}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted">
                  <a href={p.url} target="_blank" rel="noopener" className="link break-all font-mono">
                    {p.url}
                  </a>
                  {p.custom_domain && <span>Custom domain: {p.custom_domain}</span>}
                  <span>
                    {p.items?.length ?? 0} monitor{(p.items?.length ?? 0) === 1 ? '' : 's'}
                  </span>
                  {!p.published && <span>Not public until published</span>}
                </div>
              </div>
              <div className="flex shrink-0 flex-wrap items-center gap-2">
                <Link to={`${p.id}/incidents`} className="btn">
                  Incidents
                </Link>
                <Link to={`${p.id}/subscribers`} className="btn">
                  Subscribers
                </Link>
                {can('editor') && (
                  <>
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
                  </>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={confirming !== null}
        title={`Delete ${confirming?.title ?? ''}?`}
        busy={deletePage.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setConfirming(null)}
      >
        <p>
          The public page goes offline immediately, and its incidents and subscribers are deleted. This cannot be
          undone.
        </p>
        {deleteError && (
          <p role="alert" className="mt-2 text-down-ink">
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}
