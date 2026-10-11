import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { useCreateIncident, useIncidents, useStatusPages } from '../../api/queries'
import type { IncidentSeverity, IncidentStatus } from '../../api/types'
import { Checkbox, Select, TextField } from '../../components/Fields'
import { EmptyState, ErrorMessage, Loading, PageHeader, fieldError } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import {
  emptyIncidentForm,
  incidentFormToBody,
  incidentSeverities,
  incidentStatuses,
  validateIncidentForm,
  type IncidentForm,
} from '../../lib/statusPageForm'
import { IncidentCard } from './IncidentCard'

/** IncidentsPage manages one status page's incidents: drafts, active and resolved. */
export function IncidentsPage() {
  const t = useTeamId()
  const pageId = useNumberParam('pageId')
  const { can } = useRole(t)
  const pages = useStatusPages(t)
  const incidents = useIncidents(t, pageId)
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

  const all = incidents.data ?? []
  const drafts = all.filter((i) => i.draft)
  const active = all.filter((i) => !i.draft && i.status !== 'resolved')
  const resolved = all.filter((i) => !i.draft && i.status === 'resolved')
  const groups = [
    { title: 'Drafts', list: drafts },
    { title: 'Active', list: active },
    { title: 'Resolved', list: resolved },
  ]

  return (
    <div className="max-w-3xl">
      <PageHeader title={page ? `Incidents · ${page.title}` : 'Incidents'}>
        <Link to={`/t/${t}/status-pages/${pageId}/subscribers`} className="btn">
          Subscribers
        </Link>
        <Link to={`/t/${t}/status-pages`} className="btn">
          All status pages
        </Link>
      </PageHeader>
      <p className="mb-4 text-sm text-muted">
        Publishing an incident shows it on the public page and notifies confirmed subscribers; so does every update you
        post to a published incident. Drafts are private and notify no one.
      </p>
      {can('editor') && <NewIncident team={t} pageId={pageId} />}
      {(incidents.isPending || pages.isPending) && <Loading />}
      <ErrorMessage error={incidents.error ?? pages.error} />
      {incidents.data && all.length === 0 && <EmptyState>No incidents yet.</EmptyState>}
      <div className="mt-6 space-y-6">
        {groups
          .filter((g) => g.list.length > 0)
          .map((g) => (
            <section key={g.title} aria-label={g.title}>
              <h2 className="mb-2 text-sm font-semibold tracking-wide text-muted uppercase">
                {g.title} ({g.list.length})
              </h2>
              <div className="space-y-3">
                {g.list.map((i) => (
                  <IncidentCard key={i.id} team={t} pageId={pageId} incident={i} canEdit={can('editor')} />
                ))}
              </div>
            </section>
          ))}
      </div>
    </div>
  )
}

function NewIncident({ team, pageId }: { team: number; pageId: number }) {
  const create = useCreateIncident(team, pageId)
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState<IncidentForm>(emptyIncidentForm)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const set = <K extends keyof IncidentForm>(k: K, v: IncidentForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  if (!open) {
    return (
      <button type="button" className="btn-primary" onClick={() => setOpen(true)}>
        New incident
      </button>
    )
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    create.reset()
    const found = validateIncidentForm(form)
    setErrors(found)
    if (Object.keys(found).length > 0) return
    create.mutate(incidentFormToBody(form), {
      onSuccess: () => {
        setForm(emptyIncidentForm)
        setOpen(false)
      },
    })
  }

  return (
    <form onSubmit={submit} className="card space-y-4 p-4" aria-label="New incident">
      <h2 className="text-base font-semibold">New incident</h2>
      <ErrorMessage error={create.error} />
      <TextField
        label="Title"
        value={form.title}
        onChange={(v) => set('title', v)}
        required
        error={errors.title ?? fieldError(create.error, 'title')}
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <Select
          label="Severity"
          value={form.severity}
          onChange={(v) => set('severity', v as IncidentSeverity)}
          options={incidentSeverities}
        />
        <Select
          label="Status"
          value={form.status}
          onChange={(v) => set('status', v as IncidentStatus)}
          options={incidentStatuses}
        />
      </div>
      <TextField
        label="Message"
        value={form.body}
        onChange={(v) => set('body', v)}
        multiline
        error={errors.body ?? fieldError(create.error, 'body')}
        help="Optional first entry on the incident’s timeline. Visitors see it."
      />
      <Checkbox
        label="Save as draft"
        checked={form.draft}
        onChange={(v) => set('draft', v)}
        help="A draft is private and notifies no one. Without it, the incident is published and subscribers are notified right away."
      />
      <div className="flex gap-2">
        <button type="submit" className="btn-primary" disabled={create.isPending}>
          {create.isPending ? 'Saving…' : form.draft ? 'Save draft' : 'Publish incident'}
        </button>
        <button
          type="button"
          className="btn"
          onClick={() => {
            create.reset()
            setErrors({})
            setOpen(false)
          }}
        >
          Cancel
        </button>
      </div>
    </form>
  )
}
