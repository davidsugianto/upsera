import { useState, type FormEvent } from 'react'
import { ApiError } from '../api/client'
import { useAddMember, useMembers, useRemoveMember, useUpdateMember } from '../api/queries'
import type { Member, Role } from '../api/types'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Select, TextField } from '../components/Fields'
import { EmptyState, ErrorMessage, Loading, PageHeader } from '../components/ui'
import { useRole } from '../hooks/useRole'
import { useTeamId } from '../hooks/useTeamId'

const roleOptions: { value: Role; label: string }[] = [
  { value: 'viewer', label: 'Viewer' },
  { value: 'editor', label: 'Editor' },
  { value: 'owner', label: 'Owner' },
]

/** MembersPage lists a team's members; owners can add, change roles, and remove them. */
export function MembersPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const members = useMembers(t)
  const isOwner = can('owner')

  if (members.isPending) return <Loading />
  if (members.error) return <ErrorMessage error={members.error} />

  return (
    <div>
      <PageHeader title="Members" />
      {members.data.length === 0 ? (
        <EmptyState>No members yet.</EmptyState>
      ) : (
        <div className="card divide-y divide-line">
          {members.data.map((m) => (
            <MemberRow key={m.user_id} t={t} member={m} canManage={isOwner} />
          ))}
        </div>
      )}
      {isOwner && <AddMemberForm t={t} />}
    </div>
  )
}

function MemberRow({ t, member, canManage }: { t: number; member: Member; canManage: boolean }) {
  const update = useUpdateMember(t)
  const remove = useRemoveMember(t)
  const [confirming, setConfirming] = useState(false)

  return (
    <div className="px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="font-medium">{member.name}</div>
          <div className="text-sm text-muted">{member.email}</div>
        </div>
        {canManage ? (
          <div className="flex items-center gap-2">
            <select
              aria-label={`Role for ${member.email}`}
              className="input w-auto"
              value={member.role}
              disabled={update.isPending}
              onChange={(e) => update.mutate({ userID: member.user_id, role: e.target.value as Role })}
            >
              {roleOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
            <button type="button" className="btn-danger" onClick={() => setConfirming(true)}>
              Remove
            </button>
          </div>
        ) : (
          <span className="text-sm capitalize text-muted">{member.role}</span>
        )}
      </div>
      {update.error && <ErrorMessage error={update.error} />}
      <ConfirmDialog
        open={confirming}
        title={`Remove ${member.name}?`}
        busy={remove.isPending}
        onConfirm={() => remove.mutate(member.user_id, { onSuccess: () => setConfirming(false) })}
        onCancel={() => setConfirming(false)}
      >
        They will lose access to this team.
        {remove.error && <ErrorMessage error={remove.error} />}
      </ConfirmDialog>
    </div>
  )
}

function AddMemberForm({ t }: { t: number }) {
  const add = useAddMember(t)
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('viewer')

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    add.mutate({ email, role }, { onSuccess: () => setEmail('') })
  }

  const notFound = add.error instanceof ApiError && add.error.status === 404

  return (
    <form onSubmit={onSubmit} className="card mt-4 max-w-lg space-y-4 p-5">
      <h2 className="text-sm font-semibold">Add member</h2>
      {add.error && (
        <ErrorMessage
          error={notFound ? 'No user with that email. An instance admin must create the account first.' : add.error}
        />
      )}
      <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
        <TextField label="Email" type="email" value={email} onChange={setEmail} required autoComplete="email" />
        <Select label="Role" value={role} onChange={(v) => setRole(v as Role)} options={roleOptions} />
      </div>
      <button type="submit" className="btn-primary" disabled={add.isPending}>
        Add member
      </button>
    </form>
  )
}
