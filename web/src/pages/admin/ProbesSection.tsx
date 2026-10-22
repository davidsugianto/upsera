import { useState, type FormEvent } from 'react'
import { ApiError } from '../../api/client'
import { useAdminProbes, useCreateProbe, useDeleteProbe, useProbeRegistrationToken } from '../../api/queries'
import type { Probe } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { TextField } from '../../components/Fields'
import { CopyButton, ErrorMessage, Loading, errorText, fieldError, formatTime } from '../../components/ui'
import { useNow } from '../../hooks/useNow'
import { formatAge, probeRunCommand, probeStatusLine, validateProbeForm } from '../../lib/probes'

/** A registration token is shown once, right after it is issued. */
type Issued = { probeName: string; region: string; token: string; expires: string }

/** ProbesSection manages the remote probes that run monitor checks from other regions. */
export function ProbesSection() {
  const probes = useAdminProbes()
  const create = useCreateProbe()
  const reissue = useProbeRegistrationToken()
  const del = useDeleteProbe()
  const [name, setName] = useState('')
  const [region, setRegion] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [issued, setIssued] = useState<Issued | null>(null)
  const [renewing, setRenewing] = useState<Probe | null>(null)
  const [deleting, setDeleting] = useState<Probe | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  function onCreate(e: FormEvent) {
    e.preventDefault()
    const found = validateProbeForm({ name, region })
    setErrors(found)
    if (Object.keys(found).length > 0) return
    create.mutate(
      { name: name.trim(), region: region.trim() },
      {
        onSuccess: (r) => {
          setIssued({
            probeName: r.probe.name,
            region: r.probe.region,
            token: r.registration_token,
            expires: r.registration_expires_at,
          })
          setName('')
          setRegion('')
        },
      },
    )
  }

  function confirmRenew() {
    if (!renewing) return
    const p = renewing
    reissue.mutate(p.id, {
      onSuccess: (r) => {
        setIssued({ probeName: p.name, region: p.region, token: r.registration_token, expires: r.registration_expires_at })
        setRenewing(null)
      },
    })
  }

  function confirmDelete() {
    if (!deleting) return
    setDeleteError(null)
    del.mutate(deleting.id, {
      onSuccess: () => setDeleting(null),
      onError: (err) =>
        setDeleteError(
          err instanceof ApiError && err.status === 409
            ? `${err.detail} — remove region "${deleting.region}" from those monitors first.`
            : errorText(err),
        ),
    })
  }

  return (
    <section className="space-y-4">
      <div className="space-y-1">
      <h2 className="text-base font-semibold">Probes</h2>
      <p className="max-w-2xl text-xs text-muted">
        A probe runs checks from another region and reports back. Each region has one probe; monitors pick the regions
        they run in. The built-in <code>local</code> probe is the server itself.
      </p>
      </div>

      {issued && <IssuedToken issued={issued} onDismiss={() => setIssued(null)} />}

      {probes.isPending ? (
        <Loading />
      ) : probes.error ? (
        <ErrorMessage error={probes.error} />
      ) : (
        <ul className="card divide-y divide-line overflow-hidden">
          {probes.data.map((p) => (
            <ProbeRow
              key={p.id}
              probe={p}
              onRenew={() => {
                reissue.reset()
                setRenewing(p)
              }}
              onDelete={() => {
                setDeleteError(null)
                setDeleting(p)
              }}
            />
          ))}
        </ul>
      )}

      <form onSubmit={onCreate} className="card max-w-md space-y-3 p-4" aria-label="Add a probe">
        <h3 className="text-base font-semibold">Add probe</h3>
        <ErrorMessage error={create.error} />
        <TextField
          label="Name"
          value={name}
          onChange={setName}
          placeholder="singapore-1"
          autoComplete="off"
          error={fieldError(create.error, 'name', errors)}
        />
        <TextField
          label="Region"
          value={region}
          onChange={setRegion}
          placeholder="sg-1"
          autoComplete="off"
          help="The label monitors use to pick this probe. Lowercase letters, digits and dashes."
          error={fieldError(create.error, 'region', errors)}
        />
        <button type="submit" className="btn-primary" disabled={create.isPending}>
          {create.isPending ? 'Creating…' : 'Create probe'}
        </button>
      </form>

      <ConfirmDialog
        open={renewing !== null}
        title={`New registration token for ${renewing?.name ?? ''}?`}
        confirmLabel="Issue token"
        danger
        busy={reissue.isPending}
        onConfirm={confirmRenew}
        onCancel={() => setRenewing(null)}
      >
        <p>
          This revokes the probe&apos;s current credential. It stops reporting right away and stays silent until it
          registers again with the new token (valid for 24 hours, single use).
        </p>
        {reissue.error && (
          <p role="alert" className="mt-2 text-down-ink">
            {errorText(reissue.error)}
          </p>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={deleting !== null}
        title={`Delete ${deleting?.name ?? ''}?`}
        busy={del.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setDeleting(null)}
      >
        <p>
          The probe&apos;s credential stops working and every check result it reported, including the
          per-region latency history, is deleted with it. Deleting fails while a monitor still runs in region{' '}
          <code>{deleting?.region}</code>.
        </p>
        {deleteError && (
          <p role="alert" className="mt-2 text-down-ink">
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}

function ProbeRow({ probe: p, onRenew, onDelete }: { probe: Probe; onRenew: () => void; onDelete: () => void }) {
  const now = useNow()
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
      <div className="min-w-0 text-sm">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{p.name}</span>
          <span className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-xs">{p.region}</span>
          {p.is_local && <span className="rounded bg-accent-soft px-1.5 py-0.5 text-xs text-accent-ink">Built in</span>}
          <OnlineBadge probe={p} />
        </div>
        <p className="mt-1 text-xs text-muted">
          {p.is_local ? (
            'Runs checks on this server.'
          ) : (
            <>
              {p.registered ? (
                <>
                  Last seen {p.last_seen_at ? `${formatAge(now - Date.parse(p.last_seen_at))} (${formatTime(p.last_seen_at)})` : 'never'}
                  {p.version && <> · version {p.version}</>}
                </>
              ) : (
                probeStatusLine(p, now)
              )}
            </>
          )}
        </p>
      </div>
      {!p.is_local && (
        <div className="flex shrink-0 items-center gap-2">
          <button type="button" className="btn" onClick={onRenew}>
            New registration token
          </button>
          <button type="button" className="btn-danger" onClick={onDelete}>
            Delete
          </button>
        </div>
      )}
    </li>
  )
}

function OnlineBadge({ probe: p }: { probe: Probe }) {
  if (!p.is_local && !p.registered) {
    return <span className="rounded-full border border-line px-2 py-0.5 text-xs text-muted">Not registered</span>
  }
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border border-line px-2 py-0.5 text-xs font-medium ${
        p.online ? 'text-up-ink' : 'text-down-ink'
      }`}
    >
      <span className={`inline-block size-2 rounded-full ${p.online ? 'bg-up' : 'bg-down'}`} aria-hidden="true" />
      {p.online ? 'Online' : 'Offline'}
    </span>
  )
}

/** IssuedToken shows a registration token and the docker command that uses it; it cannot be shown again. */
function IssuedToken({ issued, onDismiss }: { issued: Issued; onDismiss: () => void }) {
  const command = probeRunCommand(window.location.origin, issued.token)
  return (
    <div role="status" className="card space-y-3 border-line-strong p-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-base font-semibold">
            Registration token for {issued.probeName} ({issued.region})
          </h3>
          <p className="mt-1 text-xs text-muted">
            Copy it now: it is shown only once. It is single use and expires {formatTime(issued.expires)}.
          </p>
        </div>
        <button type="button" className="btn" onClick={onDismiss}>
          Done
        </button>
      </div>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto rounded-md border border-line bg-surface-2 px-2 py-1 font-mono text-xs">
          {issued.token}
        </code>
        <CopyButton text={issued.token} label="Copy token" />
      </div>
      <div>
        <div className="mb-1 flex items-center justify-between">
          <span className="text-xs font-medium">Run this on the probe host</span>
          <CopyButton text={command} label="Copy command" />
        </div>
        <pre className="overflow-x-auto rounded-md border border-line bg-surface-2 p-3 font-mono text-xs">{command}</pre>
        <p className="mt-1 text-xs text-muted">
          The <code>upsera-probe</code> volume keeps the probe&apos;s credential, so the token is only needed on the
          first start.
        </p>
      </div>
    </div>
  )
}
