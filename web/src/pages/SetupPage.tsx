import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router'
import { useSetup, useSetupStatus } from '../api/queries'
import { Logo } from '../components/Layout'
import { ErrorMessage, Loading, fieldError } from '../components/ui'
import { TextField } from '../components/Fields'

/** SetupPage creates the instance's first admin and team, once, before login exists. */
export function SetupPage() {
  const status = useSetupStatus()
  const navigate = useNavigate()
  const setup = useSetup()
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [teamName, setTeamName] = useState('')

  if (status.isPending) return <Loading />
  if (status.error) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorMessage error={status.error} />
      </div>
    )
  }
  if (!status.data.needed) return <Navigate to="/login" replace />

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setup.mutate(
      { email, name, password, team_name: teamName },
      { onSuccess: () => navigate('/') },
    )
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-4 py-10">
      <div className="mb-6 flex justify-center">
        <Logo />
      </div>
      <form onSubmit={onSubmit} className="card space-y-4 p-6">
        <h1 className="text-lg font-semibold">Set up Upsera</h1>
        <p className="text-sm text-muted">Create the first admin account and your team.</p>
        <ErrorMessage error={setup.error} />
        <TextField
          label="Email"
          type="email"
          value={email}
          onChange={setEmail}
          required
          autoComplete="email"
          error={fieldError(setup.error, 'email')}
        />
        <TextField
          label="Name"
          value={name}
          onChange={setName}
          required
          autoComplete="name"
          error={fieldError(setup.error, 'name')}
        />
        <TextField
          label="Password"
          type="password"
          value={password}
          onChange={setPassword}
          required
          autoComplete="new-password"
          help="At least 8 characters."
          error={fieldError(setup.error, 'password')}
        />
        <TextField
          label="Team name"
          value={teamName}
          onChange={setTeamName}
          required
          autoComplete="organization"
          error={fieldError(setup.error, 'team_name')}
        />
        <button type="submit" className="btn-primary w-full" disabled={setup.isPending}>
          {setup.isPending ? 'Creating…' : 'Create admin & team'}
        </button>
      </form>
    </div>
  )
}
