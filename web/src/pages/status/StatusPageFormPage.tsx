import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { ApiError } from '../../api/client'
import { useMe, useMonitors, useSaveStatusPage, useStatusPages } from '../../api/queries'
import type { StatusPage, Theme } from '../../api/types'
import { Checkbox, Select, TextField } from '../../components/Fields'
import { ErrorMessage, Loading, PageHeader } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import {
  MAX_TEXT,
  emptyStatusPageForm,
  formToBody,
  pageToForm,
  publicUrl,
  slugify,
  themes,
  validateStatusPageForm,
  type StatusPageForm,
} from '../../lib/statusPageForm'
import { StatusPageBuilder } from './StatusPageBuilder'

/** StatusPageFormPage creates or edits a status page. */
export function StatusPageFormPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const pageId = useNumberParam('pageId')
  const editing = !Number.isNaN(pageId)
  const pages = useStatusPages(t)
  const existing = editing ? pages.data?.find((p) => p.id === pageId) : undefined

  if (!can('editor')) {
    return (
      <div>
        <PageHeader title="Status page" />
        <p className="text-sm text-muted">Only editors can create or change status pages.</p>
        <Link to={`/t/${t}/status-pages`} className="link">
          Back to status pages
        </Link>
      </div>
    )
  }
  if (editing && pages.isPending) return <Loading />
  if (editing && pages.error) return <ErrorMessage error={pages.error} />
  if (editing && !existing) {
    return (
      <div>
        <PageHeader title="Status page not found" />
        <Link to={`/t/${t}/status-pages`} className="link">
          Back to status pages
        </Link>
      </div>
    )
  }
  // Keyed by id so each page starts from fresh state; later refetches don't overwrite edits.
  return <PageEditor key={editing ? pageId : 'new'} team={t} pageId={editing ? pageId : undefined} initial={existing} />
}

function PageEditor({ team, pageId, initial }: { team: number; pageId?: number; initial?: StatusPage }) {
  const navigate = useNavigate()
  const save = useSaveStatusPage(team)
  const isAdmin = useMe().data?.user.is_admin === true
  const monitors = useMonitors(team)
  const editing = pageId !== undefined
  const [form, setForm] = useState<StatusPageForm>(() => (initial ? pageToForm(initial) : emptyStatusPageForm()))
  const [slugTouched, setSlugTouched] = useState(editing)
  const [errors, setErrors] = useState<Record<string, string>>({})
  // Local (client) errors win over the server's for the same field. The server may write array
  // positions as `items[2].monitor_id`; the builder looks them up as `items.2.monitor_id`.
  const serverErrors: Record<string, string> = {}
  if (save.error instanceof ApiError) {
    for (const [k, v] of Object.entries(save.error.fields)) serverErrors[k.replace(/\[(\d+)\]/g, '.$1')] = v
  }
  const errs: Record<string, string> = { ...serverErrors, ...errors }

  const set = <K extends keyof StatusPageForm>(k: K, v: StatusPageForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const setTitle = (title: string) =>
    setForm((f) => ({ ...f, title, slug: slugTouched ? f.slug : slugify(title) }))

  const url = publicUrl(form, window.location.origin)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    save.reset()
    const found = validateStatusPageForm(form, window.location.hostname)
    setErrors(found)
    if (Object.keys(found).length > 0) return
    save.mutate({ id: pageId, body: formToBody(form) }, { onSuccess: () => navigate(`/t/${team}/status-pages`) })
  }

  return (
    <div className="max-w-3xl">
      <PageHeader title={editing ? 'Edit status page' : 'Create status page'} />
      <form onSubmit={submit} className="space-y-6">
        <div className="card space-y-4 p-5">
          <ErrorMessage error={save.error} />
          <TextField
            label="Title"
            value={form.title}
            onChange={setTitle}
            required
            error={errs.title}
            help="Shown at the top of the public page."
          />
          <TextField
            label="Slug"
            value={form.slug}
            onChange={(v) => {
              setSlugTouched(true)
              set('slug', v)
            }}
            required
            error={errs.slug}
            help={
              url ? (
                <>
                  Public address: <span className="font-mono">{url}</span>
                </>
              ) : (
                'Lower-case letters, digits and hyphens.'
              )
            }
          />
          <TextField
            label="Description"
            value={form.description}
            onChange={(v) => set('description', v)}
            multiline
            error={errs.description}
            help={`Optional, up to ${MAX_TEXT.description} characters.`}
          />
          <TextField
            label="Logo URL"
            type="url"
            value={form.logoUrl}
            onChange={(v) => set('logoUrl', v)}
            placeholder="https://example.com/logo.png"
            error={errs.logo_url}
            help="Optional. Must be an https:// address."
          />
          <Select
            label="Theme"
            value={form.theme}
            onChange={(v) => set('theme', v as Theme)}
            options={themes}
            error={errs.theme}
          />
          {isAdmin ? (
            <TextField
              label="Custom domain"
              value={form.customDomain}
              onChange={(v) => set('customDomain', v)}
              placeholder="status.example.com"
              error={errs.custom_domain}
              help="Optional. Also serve the page on your own host name; point its DNS at this server."
            />
          ) : (
            <div>
              <p className="mb-1 text-sm font-medium">Custom domain</p>
              <p className="text-sm">{form.customDomain || 'None'}</p>
              <p className="mt-1 text-xs text-muted">Only an instance admin can set or change a custom domain.</p>
            </div>
          )}
          <Checkbox
            label="Published"
            checked={form.published}
            onChange={(v) => set('published', v)}
            help="Only published pages are visible to the public. Leave unchecked to keep it a draft."
          />
        </div>

        <section className="card space-y-3 p-5" aria-labelledby="builder-heading">
          <div>
            <h2 id="builder-heading" className="text-base font-semibold">
              Monitors on this page
            </h2>
            <p className="mt-1 text-sm text-muted">
              Visitors see the <strong className="font-medium text-ink">public name</strong> you set here. It starts
              as the monitor’s own name (and an empty name falls back to it), so rename any monitor whose name is
              internal, such as a hostname, an IP address or a customer. Targets, URLs and check messages are never
              shown. Sections group monitors under a heading; sections with no monitors are not saved.
            </p>
          </div>
          <ErrorMessage error={monitors.error} />
          {monitors.isPending ? (
            <Loading />
          ) : (
            <StatusPageBuilder
              sections={form.sections}
              monitors={monitors.data ?? []}
              errors={errs}
              onChange={(sections) => set('sections', sections)}
            />
          )}
          {errs.items && <p className="text-xs text-down">{errs.items}</p>}
        </section>

        <div className="flex gap-2">
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {save.isPending ? 'Saving…' : editing ? 'Save' : 'Create'}
          </button>
          <Link to={`/t/${team}/status-pages`} className="btn">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  )
}
