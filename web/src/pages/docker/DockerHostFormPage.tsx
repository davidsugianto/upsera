import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { useDockerHosts, useSaveDockerHost } from '../../api/queries'
import type { DockerHost } from '../../api/types'
import { TextField } from '../../components/Fields'
import { ErrorMessage, Loading, PageHeader, fieldError } from '../../components/ui'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import {
  REDACTED,
  emptyDockerHostForm,
  formToBody,
  hostToForm,
  insecureWarning,
  type DockerHostForm,
} from '../../lib/dockerHostForm'

/** DockerHostFormPage creates or edits a Docker host. */
export function DockerHostFormPage() {
  const t = useTeamId()
  const hostId = useNumberParam('hostId')
  const editing = !Number.isNaN(hostId)
  const hosts = useDockerHosts(t)
  const existing = editing ? hosts.data?.find((h) => h.id === hostId) : undefined

  if (editing && hosts.isPending) return <Loading />
  if (editing && hosts.error) return <ErrorMessage error={hosts.error} />
  if (editing && !existing) {
    return (
      <div>
        <PageHeader title="Docker host not found" />
        <Link to={`/t/${t}/docker-hosts`} className="link">
          Back to Docker hosts
        </Link>
      </div>
    )
  }
  return <HostEditor key={hostId} team={t} hostId={editing ? hostId : undefined} initial={existing} />
}

function HostEditor({ team, hostId, initial }: { team: number; hostId?: number; initial?: DockerHost }) {
  const navigate = useNavigate()
  const save = useSaveDockerHost(team)
  const editing = hostId !== undefined
  const [form, setForm] = useState<DockerHostForm>(() => (initial ? hostToForm(initial) : emptyDockerHostForm))
  const set = <K extends keyof DockerHostForm>(k: K, v: string) => setForm((f) => ({ ...f, [k]: v }))
  const warning = insecureWarning(form)
  const tlsHelp = editing ? `Leave as ${REDACTED} to keep the stored value; clear the box to remove it.` : undefined

  const submit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate(
      { id: hostId, body: formToBody(form, editing) },
      { onSuccess: () => navigate(`/t/${team}/docker-hosts`) },
    )
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title={editing ? 'Edit Docker host' : 'Add Docker host'} />
      <form onSubmit={submit} className="space-y-6">
        <ErrorMessage error={save.error} />
        <section className="card p-4">
          <h2 className="mb-4 text-base font-semibold text-balance">Connection</h2>
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label="Name"
              value={form.name}
              onChange={(v) => set('name', v)}
              required
              error={fieldError(save.error, 'name')}
            />
            <TextField
              className="[&_input]:font-mono"
              label="Endpoint"
              value={form.endpoint}
              onChange={(v) => set('endpoint', v)}
              required
              placeholder="tcp://docker-proxy:2375"
              help="A socket proxy or Docker API: tcp://host:port or https://host:port"
              error={fieldError(save.error, 'endpoint')}
            />
            {warning && (
              <p className="rounded-md border border-pend/50 bg-pend/10 px-3 py-2 text-xs text-pend-ink sm:col-span-2">
                {warning}
              </p>
            )}
          </div>
        </section>
        <section className="card p-4">
          <h2 className="mb-4 text-base font-semibold text-balance">TLS (optional)</h2>
          <div className="grid gap-4">
            <TextField
              label="CA certificate (PEM)"
              value={form.ca}
              onChange={(v) => set('ca', v)}
              multiline
              help={tlsHelp ?? 'Needed when the server uses a private CA.'}
              error={fieldError(save.error, 'tls.ca')}
            />
            <TextField
              label="Client certificate (PEM)"
              value={form.cert}
              onChange={(v) => set('cert', v)}
              multiline
              help={tlsHelp}
              error={fieldError(save.error, 'tls.cert')}
            />
            <TextField
              label="Client key (PEM)"
              value={form.key}
              onChange={(v) => set('key', v)}
              multiline
              help={tlsHelp}
              error={fieldError(save.error, 'tls.key')}
            />
          </div>
        </section>
        <div className="sticky bottom-0 z-10 flex justify-end gap-2 border-t border-line bg-bg py-3">
          <Link to={`/t/${team}/docker-hosts`} className="btn">
            Cancel
          </Link>
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {editing ? 'Save' : 'Create'}
          </button>
        </div>
      </form>
    </div>
  )
}
