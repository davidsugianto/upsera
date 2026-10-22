import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import {
  useAdminSettings,
  useAdminUsers,
  useCreateAdminUser,
  useMe,
  useSaveAdminSettings,
  useTestSmtp,
} from '../api/queries'
import type { Settings, SettingsUpdate } from '../api/types'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Layout } from '../components/Layout'
import { ProbesSection } from './admin/ProbesSection'
import { TextField, NumberField, Checkbox, Select } from '../components/Fields'
import { ErrorMessage, Loading, PageHeader, errorText, fieldError, formatTime } from '../components/ui'
import {
  SMTP_MASK,
  smtpChanged,
  smtpFormToBody,
  smtpTlsOptions,
  smtpToForm,
  validateSmtpForm,
  type SmtpForm,
  type SmtpTls,
} from '../lib/statusPageForm'

/** AdminPage manages instance-wide users and settings; only reachable by admins. */
export function AdminPage() {
  const me = useMe()
  if (me.isPending) return <Loading />
  if (me.error) {
    return (
      <div className="mx-auto max-w-sm px-4 py-10">
        <ErrorMessage error={me.error} />
      </div>
    )
  }
  if (!me.data?.user.is_admin) return <Navigate to="/" replace />

  return (
    <Layout>
      <PageHeader title="Admin" />
      <div className="space-y-8">
        <UsersSection />
        <ProbesSection />
        <SettingsSection />
      </div>
    </Layout>
  )
}

function UsersSection() {
  const users = useAdminUsers()
  const create = useCreateAdminUser()
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [isAdmin, setIsAdmin] = useState(false)

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    create.mutate(
      { email, name, password, is_admin: isAdmin },
      {
        onSuccess: () => {
          setEmail('')
          setName('')
          setPassword('')
          setIsAdmin(false)
        },
      },
    )
  }

  return (
    <section className="space-y-4">
      <h2 className="text-base font-semibold">Users</h2>
      {users.isPending ? (
        <Loading />
      ) : users.error ? (
        <ErrorMessage error={users.error} />
      ) : users.data.length === 0 ? (
        <p className="text-sm text-muted">No users yet.</p>
      ) : (
        <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-surface-2">
            <tr>
              <th scope="col" className="th">Email</th>
              <th scope="col" className="th">Name</th>
              <th scope="col" className="th">Role</th>
              <th scope="col" className="th">Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {users.data.map((u) => (
              <tr key={u.id}>
                <td className="px-3 py-2">{u.email}</td>
                <td className="px-3 py-2">{u.name}</td>
                <td className="px-3 py-2">
                  {u.is_admin ? (
                    <span className="rounded-full bg-accent-soft px-2 py-0.5 text-xs text-accent-ink">Admin</span>
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                </td>
                <td className="px-3 py-2 text-muted">{formatTime(u.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}

      <form onSubmit={onSubmit} className="card max-w-md space-y-3 p-4">
        <h3 className="text-base font-semibold">Create user</h3>
        <ErrorMessage error={create.error} />
        <TextField
          label="Email"
          type="email"
          value={email}
          onChange={setEmail}
          required
          autoComplete="off"
          error={fieldError(create.error, 'email')}
        />
        <TextField
          label="Name"
          value={name}
          onChange={setName}
          required
          autoComplete="off"
          error={fieldError(create.error, 'name')}
        />
        <TextField
          label="Password"
          type="password"
          value={password}
          onChange={setPassword}
          required
          autoComplete="new-password"
          error={fieldError(create.error, 'password')}
        />
        <Checkbox label="Instance admin" checked={isAdmin} onChange={setIsAdmin} />
        <button type="submit" className="btn-primary" disabled={create.isPending}>
          {create.isPending ? 'Creating…' : 'Create user'}
        </button>
      </form>
    </section>
  )
}

function SettingsSection() {
  const settings = useAdminSettings()
  return (
    <section className="space-y-4">
      <h2 className="text-base font-semibold">Settings</h2>
      {settings.isPending ? (
        <Loading />
      ) : settings.error ? (
        <ErrorMessage error={settings.error} />
      ) : (
        <SettingsForm settings={settings.data} />
      )}
    </section>
  )
}

function SettingsForm({ settings }: { settings: Settings }) {
  const save = useSaveAdminSettings()
  const [blockPrivate, setBlockPrivate] = useState(settings.block_private_targets)
  const [retentionDays, setRetentionDays] = useState(settings.retention_days ?? NaN)
  const initialSmtp = smtpToForm(settings.smtp)
  const [smtp, setSmtp] = useState<SmtpForm>(initialSmtp)
  const [smtpErrors, setSmtpErrors] = useState<Record<string, string>>({})
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [clientError, setClientError] = useState<string | null>(null)
  const setSmtpField = <K extends keyof SmtpForm>(k: K, v: SmtpForm[K]) => setSmtp((f) => ({ ...f, [k]: v }))
  const smtpDirty = smtpChanged(initialSmtp, smtp)

  /** submit saves the general settings; `smtp` is omitted unless it was edited (or `removeSmtp` is set). */
  function submit(removeSmtp: boolean) {
    save.reset()
    if (!Number.isNaN(retentionDays) && (!Number.isInteger(retentionDays) || retentionDays < 3)) {
      setClientError('Retention days must be a whole number of at least 3, or blank for the default.')
      return false
    }
    setClientError(null)
    const body: SettingsUpdate = {
      block_private_targets: blockPrivate,
      ...(Number.isNaN(retentionDays) ? {} : { retention_days: retentionDays }),
    }
    if (removeSmtp) {
      body.smtp = null
    } else if (smtpDirty) {
      const found = validateSmtpForm(smtp)
      setSmtpErrors(found)
      if (Object.keys(found).length > 0) return false
      body.smtp = smtpFormToBody(smtp)
    }
    setSmtpErrors({})
    // Reload the form from the server's answer: the password comes back masked.
    save.mutate(body, {
      onSuccess: (saved) => {
        setSmtp(smtpToForm(saved.smtp))
        setConfirmRemove(false)
      },
    })
    return true
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    submit(false)
  }

  return (
    <>
      <form onSubmit={onSubmit} className="card max-w-xl space-y-3 p-4">
        {clientError ? (
          <ErrorMessage error={clientError} />
        ) : (
          save.error && <ErrorMessage error={save.error} />
        )}
        <Checkbox
          label="Block private targets"
          checked={blockPrivate}
          onChange={setBlockPrivate}
          help="Refuse monitors that resolve to private, loopback or link-local addresses."
        />
        <NumberField
          label="Retention days"
          value={retentionDays}
          onChange={setRetentionDays}
          min={3}
          help="Blank keeps the server's default."
          error={fieldError(save.error, 'retention_days')}
        />

        <fieldset className="space-y-3 border-t border-line pt-4">
          <legend className="text-sm font-semibold">Email (SMTP) for status page subscribers</legend>
          <p className="text-xs text-muted">
            Upsera sends subscription confirmations and incident notices through this mail server. Without it, email
            sign-up on public status pages is unavailable.
          </p>
          {settings.smtp_error && (
            <p role="alert" className="rounded-md border border-down/50 bg-down/10 px-3 py-2 text-sm text-down-ink">
              {settings.smtp_error}. Enter the settings again and save to replace them.
            </p>
          )}
          <div className="grid gap-3 sm:grid-cols-[1fr_8rem]">
            <TextField
              label="Host"
              value={smtp.host}
              onChange={(v) => setSmtpField('host', v)}
              placeholder="smtp.example.com"
              autoComplete="off"
              error={smtpErrors.host}
            />
            <TextField
              label="Port"
              value={smtp.port}
              onChange={(v) => setSmtpField('port', v)}
              autoComplete="off"
              error={smtpErrors.port}
            />
          </div>
          <TextField
            label="Username"
            value={smtp.username}
            onChange={(v) => setSmtpField('username', v)}
            autoComplete="off"
            help="Leave blank if the server needs no login."
          />
          <TextField
            label="Password"
            type="password"
            value={smtp.password}
            onChange={(v) => setSmtpField('password', v)}
            autoComplete="new-password"
            help={
              smtp.password === SMTP_MASK
                ? `A password is stored. Leave ${SMTP_MASK} to keep it, or clear the box to remove it.`
                : 'Type a new password to replace the stored one.'
            }
          />
          <TextField
            label="From"
            value={smtp.from}
            onChange={(v) => setSmtpField('from', v)}
            placeholder="Upsera <status@example.com>"
            autoComplete="off"
            error={smtpErrors.from}
          />
          <Select
            label="Encryption"
            value={smtp.tls}
            onChange={(v) => setSmtpField('tls', v as SmtpTls)}
            options={smtpTlsOptions}
            error={smtpErrors.tls}
          />
        </fieldset>

        <div className="flex flex-wrap items-center gap-2">
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
          {settings.smtp && (
            <button type="button" className="btn-danger" disabled={save.isPending} onClick={() => setConfirmRemove(true)}>
              Remove SMTP settings
            </button>
          )}
          {save.isSuccess && !save.isPending && <span className="text-sm text-muted">Saved</span>}
        </div>
      </form>
      <ConfirmDialog
        open={confirmRemove}
        title="Remove SMTP settings?"
        confirmLabel="Remove"
        busy={save.isPending}
        onConfirm={() => {
          if (!submit(true)) setConfirmRemove(false)
        }}
        onCancel={() => setConfirmRemove(false)}
      >
        <p>Status page email subscriptions and notices stop working until a mail server is configured again.</p>
        {save.error && (
          <p role="alert" className="mt-2 text-down-ink">
            {errorText(save.error)}
          </p>
        )}
      </ConfirmDialog>
      <SmtpTest configured={settings.smtp !== null} unsaved={smtpDirty} />
    </>
  )
}

/** SmtpTest sends a test message through the *saved* SMTP settings. */
function SmtpTest({ configured, unsaved }: { configured: boolean; unsaved: boolean }) {
  const test = useTestSmtp()
  const [to, setTo] = useState('')

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    test.reset()
    if (to.trim() !== '') test.mutate(to.trim())
  }

  return (
    <form onSubmit={onSubmit} className="card max-w-xl space-y-3 p-4" aria-label="Send a test email">
      <h3 className="text-base font-semibold">Send test email</h3>
      <p className="text-xs text-muted">
        Uses the saved settings{unsaved ? ' — save your changes first' : ''}.
        {!configured && ' Nothing is configured yet.'}
      </p>
      <TextField label="Send to" type="email" value={to} onChange={setTo} required autoComplete="email" />
      {test.error && <ErrorMessage error={test.error} />}
      {test.isSuccess && !test.isPending && (
        <p role="status" className="text-sm text-up-ink">
          Test email sent to {to.trim()}.
        </p>
      )}
      <button type="submit" className="btn" disabled={test.isPending || !configured}>
        {test.isPending ? 'Sending…' : 'Send test email'}
      </button>
    </form>
  )
}
