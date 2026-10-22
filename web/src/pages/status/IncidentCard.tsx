import { useState, type FormEvent } from 'react'
import {
  useAddIncidentUpdate,
  useDeleteIncident,
  useEditIncident,
  usePublishIncident,
} from '../../api/queries'
import type { Incident, IncidentSeverity, IncidentStatus } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Select, TextField } from '../../components/Fields'
import { statusBg } from '../../components/StatusBadge'
import { ErrorMessage, errorText, fieldError, formatTime } from '../../components/ui'
import {
  emptyIncidentForm,
  incidentSeverities,
  incidentStatuses,
  statusLabel,
  validateIncidentForm,
  validateIncidentUpdate,
} from '../../lib/statusPageForm'

const severityStyle: Record<IncidentSeverity, string> = {
  minor: 'border-line-strong text-pend-ink',
  major: 'border-down/50 text-down-ink',
  critical: 'border-down bg-down/10 font-semibold text-down-ink',
}

// Incident state: a mark (fill + pattern) plus the word, text in the -ink variant.
const stateMark: Record<IncidentStatus, { fill: string; ink: string }> = {
  investigating: { fill: statusBg('down'), ink: 'text-down-ink' },
  identified: { fill: statusBg('pending'), ink: 'text-pend-ink' },
  monitoring: { fill: statusBg('degraded'), ink: 'text-pend-ink' },
  resolved: { fill: statusBg('up'), ink: 'text-up-ink' },
}

/** IncidentCard shows one incident with its timeline and, for editors, its actions. */
export function IncidentCard({ team, pageId, incident, canEdit }: { team: number; pageId: number; incident: Incident; canEdit: boolean }) {
  const [mode, setMode] = useState<'view' | 'update' | 'edit'>('view')
  const [confirm, setConfirm] = useState<'publish' | 'delete' | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const publish = usePublishIncident(team, pageId)
  const remove = useDeleteIncident(team, pageId)
  const updates = incident.updates ?? []

  const closeConfirm = () => {
    setConfirm(null)
    setActionError(null)
  }
  const doConfirm = () => {
    const opts = { onSuccess: closeConfirm, onError: (err: unknown) => setActionError(errorText(err)) }
    setActionError(null)
    if (confirm === 'publish') publish.mutate(incident.id, opts)
    else if (confirm === 'delete') remove.mutate(incident.id, opts)
  }

  return (
    <article className="card p-4" aria-label={incident.title}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-base font-semibold text-balance">{incident.title}</h3>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs">
            {incident.draft && <span className="rounded-full border border-line-strong px-2 py-0.5 text-muted">Draft</span>}
            <span className={`rounded-full border px-2 py-0.5 ${severityStyle[incident.severity]}`}>
              {incident.severity}
            </span>
            <span
              className={`inline-flex items-center gap-1.5 rounded-full border border-line-strong px-2 py-0.5 font-medium ${stateMark[incident.status].ink}`}
            >
              <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${stateMark[incident.status].fill}`} />
              {statusLabel(incident.status)}
            </span>
            <span className="text-muted">Started {formatTime(incident.started_at)}</span>
            {incident.resolved_at && <span className="text-muted">Resolved {formatTime(incident.resolved_at)}</span>}
          </div>
          {incident.auto && (
            <p className="mt-2 inline-block rounded-md border border-line px-2 py-1 text-xs text-muted">
              {incident.draft ? 'Auto-detected, review before publishing' : 'Auto-detected from an alert'}
            </p>
          )}
        </div>
        {canEdit && mode === 'view' && (
          <div className="flex flex-wrap items-center gap-2">
            {incident.draft && (
              <button type="button" className="btn-primary" onClick={() => setConfirm('publish')}>
                Publish
              </button>
            )}
            <button type="button" className="btn" onClick={() => setMode('update')}>
              Post update
            </button>
            <button type="button" className="btn" onClick={() => setMode('edit')}>
              Edit
            </button>
            <button type="button" className="btn-danger" onClick={() => setConfirm('delete')}>
              Delete
            </button>
          </div>
        )}
      </div>

      {updates.length > 0 ? (
        <ol className="mt-4 divide-y divide-line border-y border-line">
          {updates.map((u) => (
            <li key={u.id} className="py-2 text-sm">
              <div className="flex flex-wrap items-center gap-x-2 text-xs text-muted tabular-nums">
                <span className="inline-flex items-center gap-1.5 font-medium text-ink">
                  <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${stateMark[u.status].fill}`} />
                  {statusLabel(u.status)}
                </span>
                <span aria-hidden="true">·</span>
                <span>{formatTime(u.created_at)}</span>
              </div>
              <p className="mt-0.5 max-w-[70ch] whitespace-pre-wrap">{u.body}</p>
            </li>
          ))}
        </ol>
      ) : (
        <p className="mt-3 text-sm text-muted">No timeline entries yet.</p>
      )}

      {mode === 'update' && (
        <UpdateForm team={team} pageId={pageId} incident={incident} onDone={() => setMode('view')} />
      )}
      {mode === 'edit' && <EditForm team={team} pageId={pageId} incident={incident} onDone={() => setMode('view')} />}

      <ConfirmDialog
        open={confirm === 'publish'}
        title={`Publish “${incident.title}”?`}
        confirmLabel="Publish"
        danger={false}
        busy={publish.isPending}
        onConfirm={doConfirm}
        onCancel={closeConfirm}
      >
        <p>
          The incident becomes visible on the public page and every confirmed subscriber is notified. Check the
          title and messages first.
        </p>
        {actionError && confirm === 'publish' && (
          <p role="alert" className="mt-2 text-down-ink">
            {actionError}
          </p>
        )}
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === 'delete'}
        title={`Delete “${incident.title}”?`}
        busy={remove.isPending}
        onConfirm={doConfirm}
        onCancel={closeConfirm}
      >
        <p>
          {incident.draft
            ? 'The draft and its timeline are deleted.'
            : 'The incident disappears from the public page and its history. Subscribers who were already notified keep their message.'}
        </p>
        {actionError && confirm === 'delete' && (
          <p role="alert" className="mt-2 text-down-ink">
            {actionError}
          </p>
        )}
      </ConfirmDialog>
    </article>
  )
}

function UpdateForm({ team, pageId, incident, onDone }: { team: number; pageId: number; incident: Incident; onDone: () => void }) {
  const add = useAddIncidentUpdate(team, pageId)
  const [status, setStatus] = useState<IncidentStatus>(incident.status)
  const [body, setBody] = useState('')
  const [bodyError, setBodyError] = useState('')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    add.reset()
    const err = validateIncidentUpdate(body)
    setBodyError(err)
    if (err) return
    add.mutate({ id: incident.id, body: { status, body: body.trim() } }, { onSuccess: onDone })
  }

  return (
    <form onSubmit={submit} className="mt-4 space-y-3 rounded-md border border-line bg-surface-2 p-3" aria-label="Post an update">
      <ErrorMessage error={add.error} />
      <Select
        label="Status"
        value={status}
        onChange={(v) => setStatus(v as IncidentStatus)}
        options={incidentStatuses}
        help={
          incident.draft
            ? 'Drafts stay private until published.'
            : 'Choosing Resolved marks the incident resolved. Subscribers are notified of each update.'
        }
      />
      <TextField
        label="Message"
        value={body}
        onChange={setBody}
        multiline
        required
        error={bodyError || fieldError(add.error, 'body')}
      />
      <div className="flex gap-2">
        <button type="submit" className="btn-primary" disabled={add.isPending}>
          {add.isPending ? 'Posting…' : 'Post update'}
        </button>
        <button type="button" className="btn" onClick={onDone}>
          Cancel
        </button>
      </div>
    </form>
  )
}

function EditForm({ team, pageId, incident, onDone }: { team: number; pageId: number; incident: Incident; onDone: () => void }) {
  const edit = useEditIncident(team, pageId)
  const [title, setTitle] = useState(incident.title)
  const [severity, setSeverity] = useState<IncidentSeverity>(incident.severity)
  const [titleError, setTitleError] = useState('')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    edit.reset()
    const err = validateIncidentForm({ ...emptyIncidentForm, title }).title ?? ''
    setTitleError(err)
    if (err) return
    edit.mutate({ id: incident.id, body: { title: title.trim(), severity } }, { onSuccess: onDone })
  }

  return (
    <form onSubmit={submit} className="mt-4 space-y-3 rounded-md border border-line bg-surface-2 p-3" aria-label="Edit incident">
      <ErrorMessage error={edit.error} />
      <TextField label="Title" value={title} onChange={setTitle} required error={titleError || fieldError(edit.error, 'title')} />
      <Select
        label="Severity"
        value={severity}
        onChange={(v) => setSeverity(v as IncidentSeverity)}
        options={incidentSeverities}
      />
      <div className="flex gap-2">
        <button type="submit" className="btn-primary" disabled={edit.isPending}>
          {edit.isPending ? 'Saving…' : 'Save'}
        </button>
        <button type="button" className="btn" onClick={onDone}>
          Cancel
        </button>
      </div>
    </form>
  )
}
