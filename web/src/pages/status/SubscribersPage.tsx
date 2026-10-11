import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { useAddWebhookSubscriber, useDeleteSubscriber, useStatusPages, useSubscribers } from '../../api/queries'
import type { Subscriber } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { TextField } from '../../components/Fields'
import { EmptyState, ErrorMessage, Loading, PageHeader, errorText, fieldError, formatTime } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import { validateWebhookUrl } from '../../lib/statusPageForm'

/** SubscribersPage lists a status page's subscribers and lets editors add webhooks. */
export function SubscribersPage() {
  const t = useTeamId()
  const pageId = useNumberParam('pageId')
  const { can } = useRole(t)
  const pages = useStatusPages(t)
  const subs = useSubscribers(t, pageId)
  const remove = useDeleteSubscriber(t, pageId)
  const [confirming, setConfirming] = useState<Subscriber | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const page = pages.data?.find((p) => p.id === pageId)

  if (pages.data && !page) {
    return (
      <div>
        <PageHeader title="Status page not found" />
        <Link to={`/t/${t}/status-pages`} className="link">
          Back to status pages
        </Link>
      </div>
    )
  }

  const confirmDelete = () => {
    if (!confirming) return
    setDeleteError(null)
    remove.mutate(confirming.id, {
      onSuccess: () => setConfirming(null),
      onError: (err) => setDeleteError(errorText(err)),
    })
  }

  return (
    <div className="max-w-3xl">
      <PageHeader title={page ? `Subscribers · ${page.title}` : 'Subscribers'}>
        <Link to={`/t/${t}/status-pages/${pageId}/incidents`} className="btn">
          Incidents
        </Link>
        <Link to={`/t/${t}/status-pages`} className="btn">
          All status pages
        </Link>
      </PageHeader>
      <p className="mb-4 text-sm text-muted">
        Visitors subscribe by email on the public page and must confirm their address first. Webhooks are added here and
        receive a JSON POST for each incident notice. Only confirmed subscribers are notified.
      </p>
      {(subs.isPending || pages.isPending) && <Loading />}
      <ErrorMessage error={subs.error ?? pages.error} />
      {subs.data && subs.data.length === 0 && <EmptyState>No subscribers yet.</EmptyState>}
      {subs.data && subs.data.length > 0 && (
        <div className="card overflow-x-auto p-0">
          <table className="w-full text-sm">
            <thead className="text-left text-muted">
              <tr>
                <th className="px-4 py-2 font-normal">Kind</th>
                <th className="px-4 py-2 font-normal">Target</th>
                <th className="px-4 py-2 font-normal">Confirmed</th>
                <th className="px-4 py-2 font-normal">Added</th>
                {can('editor') && (
                  <th className="px-4 py-2 font-normal">
                    <span className="sr-only">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {subs.data.map((s) => (
                <tr key={s.id} className="border-t border-line">
                  <td className="px-4 py-2 capitalize">{s.kind}</td>
                  <td className="px-4 py-2 font-mono text-xs break-all">{s.target}</td>
                  <td className="px-4 py-2">
                    {s.confirmed ? (
                      'Yes'
                    ) : (
                      <span className="text-muted" title="Waiting for the subscriber to click the confirmation link">
                        Pending
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-2 text-muted">{formatTime(s.created_at)}</td>
                  {can('editor') && (
                    <td className="px-4 py-2 text-right">
                      <button
                        type="button"
                        className="btn-danger"
                        aria-label={`Delete subscriber ${s.target}`}
                        onClick={() => {
                          setDeleteError(null)
                          setConfirming(s)
                        }}
                      >
                        Delete
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {can('editor') && <AddWebhook team={t} pageId={pageId} />}
      <ConfirmDialog
        open={confirming !== null}
        title="Delete this subscriber?"
        busy={remove.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setConfirming(null)}
      >
        <p>
          {confirming?.target} stops receiving incident notices. An email subscriber can subscribe again from the
          public page.
        </p>
        {deleteError && (
          <p role="alert" className="mt-2 text-down">
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}

function AddWebhook({ team, pageId }: { team: number; pageId: number }) {
  const add = useAddWebhookSubscriber(team, pageId)
  const [url, setUrl] = useState('')
  const [urlError, setUrlError] = useState('')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    add.reset()
    const err = validateWebhookUrl(url)
    setUrlError(err)
    if (err) return
    add.mutate({ url: url.trim() }, { onSuccess: () => setUrl('') })
  }

  return (
    <form onSubmit={submit} className="card mt-6 max-w-lg space-y-3 p-4" aria-label="Add a webhook">
      <h2 className="text-base font-semibold">Add a webhook</h2>
      <ErrorMessage error={add.error} />
      <TextField
        label="Webhook URL"
        type="url"
        value={url}
        onChange={setUrl}
        required
        placeholder="https://hooks.example.com/…"
        autoComplete="off"
        error={urlError || fieldError(add.error, 'url')}
        help="An http:// or https:// address without a user name or password. Only the host is shown in this list because the URL may contain a secret."
      />
      <button type="submit" className="btn-primary" disabled={add.isPending}>
        {add.isPending ? 'Adding…' : 'Add webhook'}
      </button>
    </form>
  )
}
