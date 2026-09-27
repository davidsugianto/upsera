import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import {
  useAdminSettings,
  useAdminUsers,
  useCreateAdminUser,
  useMe,
  useSaveAdminSettings,
} from '../api/queries'
import type { Settings } from '../api/types'
import { Layout } from '../components/Layout'
import { TextField, NumberField, Checkbox } from '../components/Fields'
import { ErrorMessage, Loading, PageHeader, fieldError, formatTime } from '../components/ui'

/** AdminPage manages instance-wide users and settings; only reachable by admins. */
export function AdminPage() {
  const me = useMe()
  if (me.isPending) return <Loading />
  if (me.error) {
    return (
      <div className="mx-auto max-w-md p-8">
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
    <section>
      <h2 className="mb-3 text-base font-semibold">Users</h2>
      {users.isPending ? (
        <Loading />
      ) : users.error ? (
        <ErrorMessage error={users.error} />
      ) : users.data.length === 0 ? (
        <p className="text-sm text-muted">No users yet.</p>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-muted">
            <tr>
              <th className="pb-2 font-normal">Email</th>
              <th className="pb-2 font-normal">Name</th>
              <th className="pb-2 font-normal">Role</th>
              <th className="pb-2 font-normal">Created</th>
            </tr>
          </thead>
          <tbody>
            {users.data.map((u) => (
              <tr key={u.id} className="border-t border-line">
                <td className="py-2">{u.email}</td>
                <td className="py-2">{u.name}</td>
                <td className="py-2">
                  {u.is_admin ? (
                    <span className="rounded-full bg-accent-soft px-2 py-0.5 text-xs text-accent-ink">Admin</span>
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                </td>
                <td className="py-2 text-muted">{formatTime(u.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <form onSubmit={onSubmit} className="card mt-4 max-w-md space-y-3 p-4">
        <h3 className="text-sm font-semibold">Create user</h3>
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
    <section>
      <h2 className="mb-3 text-base font-semibold">Settings</h2>
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
  const [clientError, setClientError] = useState<string | null>(null)

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    save.reset()
    if (!Number.isNaN(retentionDays) && (!Number.isInteger(retentionDays) || retentionDays < 3)) {
      setClientError('Retention days must be a whole number of at least 3, or blank for the default.')
      return
    }
    setClientError(null)
    save.mutate({
      block_private_targets: blockPrivate,
      ...(Number.isNaN(retentionDays) ? {} : { retention_days: retentionDays }),
    })
  }

  return (
    <form onSubmit={onSubmit} className="card max-w-md space-y-3 p-4">
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
      <div className="flex items-center gap-2">
        <button type="submit" className="btn-primary" disabled={save.isPending}>
          {save.isPending ? 'Saving…' : 'Save'}
        </button>
        {save.isSuccess && !save.isPending && <span className="text-sm text-muted">Saved</span>}
      </div>
    </form>
  )
}
