import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router'
import { useCreateTeam, useLogout, useMe } from '../api/queries'
import { Logo } from '../components/Layout'
import { ErrorMessage, Loading } from '../components/ui'
import { TextField } from '../components/Fields'

/** NoTeamPage lets a signed-in user without any team create one. */
export function NoTeamPage() {
  const me = useMe()
  const navigate = useNavigate()
  const logout = useLogout()
  const create = useCreateTeam()
  const [name, setName] = useState('')

  if (me.isPending) return <Loading />
  if (me.error) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorMessage error={me.error} />
      </div>
    )
  }
  if (!me.data) return <Navigate to="/login" replace />
  if ((me.data.teams ?? []).length > 0) return <Navigate to="/" replace />

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    create.mutate(name, { onSuccess: (team) => navigate(`/t/${team.id}`) })
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-4 py-10">
      <div className="mb-6 flex justify-center">
        <Logo />
      </div>
      <form onSubmit={onSubmit} className="card space-y-4 p-6">
        <h1 className="text-lg font-semibold">Create a team</h1>
        <p className="text-sm text-muted">
          You're not a member of any team yet. Create one, or ask a team owner to add you by email from that team's
          Members page.
        </p>
        <ErrorMessage error={create.error} />
        <TextField label="Team name" value={name} onChange={setName} required autoComplete="organization" />
        <button type="submit" className="btn-primary w-full" disabled={create.isPending}>
          {create.isPending ? 'Creating…' : 'Create team'}
        </button>
      </form>
      <button
        type="button"
        className="btn mt-4 w-full"
        onClick={() => logout.mutate(undefined, { onSuccess: () => navigate('/login') })}
      >
        Log out
      </button>
    </div>
  )
}
