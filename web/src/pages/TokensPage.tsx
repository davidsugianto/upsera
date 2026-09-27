import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useCreateToken, useRevokeToken, useTokens } from '../api/queries'
import type { CreatedToken, Token } from '../api/types'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Select, TextField } from '../components/Fields'
import { CopyButton, EmptyState, ErrorMessage, Loading, PageHeader, formatTime } from '../components/ui'
import { useRole } from '../hooks/useRole'
import { useTeamId } from '../hooks/useTeamId'

const scopeOptions = [
  { value: 'read', label: 'Read' },
  { value: 'write', label: 'Write' },
]

/** TokensPage lets team owners issue and revoke API tokens. */
export function TokensPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const tokens = useTokens(t)

  if (!can('owner')) {
    return (
      <div>
        <PageHeader title="API tokens" />
        <EmptyState>Only team owners can manage API tokens.</EmptyState>
      </div>
    )
  }

  if (tokens.isPending) return <Loading />
  if (tokens.error) return <ErrorMessage error={tokens.error} />

  return (
    <div>
      <PageHeader title="API tokens" />
      {tokens.data.length === 0 ? (
        <EmptyState>No tokens yet.</EmptyState>
      ) : (
        <div className="card divide-y divide-line">
          {tokens.data.map((tok) => (
            <TokenRow key={tok.id} t={t} token={tok} />
          ))}
        </div>
      )}
      <CreateTokenForm t={t} />
    </div>
  )
}

function TokenRow({ t, token }: { t: number; token: Token }) {
  const revoke = useRevokeToken(t)
  const [confirming, setConfirming] = useState(false)

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
      <div>
        <div className="font-medium">{token.name}</div>
        <div className="text-sm text-muted">
          {token.scope} · created {formatTime(token.created_at)} · last used {formatTime(token.last_used_at)} · expires{' '}
          {token.expires_at ? formatTime(token.expires_at) : 'never'}
        </div>
      </div>
      <button type="button" className="btn-danger" onClick={() => setConfirming(true)}>
        Revoke
      </button>
      <ConfirmDialog
        open={confirming}
        title={`Revoke "${token.name}"?`}
        confirmLabel="Revoke"
        busy={revoke.isPending}
        onConfirm={() => revoke.mutate(token.id, { onSuccess: () => setConfirming(false) })}
        onCancel={() => setConfirming(false)}
      >
        Anything using this token will stop working immediately.
        {revoke.error && <ErrorMessage error={revoke.error} />}
      </ConfirmDialog>
    </div>
  )
}

function CreateTokenForm({ t }: { t: number }) {
  const create = useCreateToken(t)
  const [name, setName] = useState('')
  const [scope, setScope] = useState<'read' | 'write'>('read')
  const [expiresAt, setExpiresAt] = useState('')
  const [created, setCreated] = useState<CreatedToken | null>(null)

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const body: { name: string; scope: 'read' | 'write'; expires_at?: string } = { name, scope }
    if (expiresAt) body.expires_at = new Date(`${expiresAt}T23:59:59`).toISOString()
    create.mutate(body, {
      onSuccess: (tok) => {
        setCreated(tok)
        setName('')
        setExpiresAt('')
      },
    })
  }

  return (
    <>
      <form onSubmit={onSubmit} className="card mt-4 max-w-lg space-y-4 p-5">
        <h2 className="text-sm font-semibold">Create token</h2>
        <ErrorMessage error={create.error} />
        <TextField label="Name" value={name} onChange={setName} required />
        <div className="grid gap-4 sm:grid-cols-2">
          <Select label="Scope" value={scope} onChange={(v) => setScope(v as 'read' | 'write')} options={scopeOptions} />
          <TextField
            label="Expires"
            type="date"
            value={expiresAt}
            onChange={setExpiresAt}
            help="Leave blank for a token that never expires."
          />
        </div>
        <button type="submit" className="btn-primary" disabled={create.isPending}>
          Create token
        </button>
      </form>
      <TokenCreatedDialog token={created} onClose={() => setCreated(null)} />
    </>
  )
}

function TokenCreatedDialog({ token, onClose }: { token: CreatedToken | null; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (token && !d.open) d.showModal?.()
    if (!token && d.open) d.close?.()
  }, [token])

  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault()
        onClose()
      }}
      className="m-auto w-full max-w-md rounded-lg border border-line bg-surface p-0 shadow-xl"
    >
      <div className="p-5">
        <h2 className="text-base font-semibold">Token created</h2>
        <p className="mt-2 text-sm text-muted">You won't see this again. Copy it now and store it securely.</p>
        {token && (
          <div className="mt-3 flex items-center gap-2">
            <code className="flex-1 overflow-x-auto rounded-md border border-line bg-bg px-2.5 py-1.5 text-xs">
              {token.token}
            </code>
            <CopyButton text={token.token} />
          </div>
        )}
        <div className="mt-5 flex justify-end">
          <button type="button" className="btn-primary" onClick={onClose}>
            Done
          </button>
        </div>
      </div>
    </dialog>
  )
}
