import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { useLogin, useMe, useSetupStatus } from '../api/queries'
import { Logo } from '../components/Layout'
import { ErrorMessage, Loading } from '../components/ui'
import { TextField } from '../components/Fields'

export function LoginPage() {
  const status = useSetupStatus()
  const me = useMe()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const login = useLogin()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')

  if (status.isPending || me.isPending) return <Loading />
  if (status.error || me.error) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorMessage error={status.error ?? me.error} />
      </div>
    )
  }
  if (status.data.needed) return <Navigate to="/setup" replace />
  if (me.data) return <Navigate to="/" replace />

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    login.mutate(
      { email, password },
      {
        onSuccess: () => {
          const next = params.get('next')
          navigate(next && next.startsWith('/') && !next.startsWith('//') ? next : '/')
        },
      },
    )
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-4 py-10">
      <div className="mb-6 flex justify-center">
        <Logo />
      </div>
      <form onSubmit={onSubmit} className="card space-y-4 p-6">
        <h1 className="text-lg font-semibold">Log in</h1>
        <ErrorMessage error={login.error} />
        <TextField label="Email" type="email" value={email} onChange={setEmail} required autoComplete="email" />
        <TextField
          label="Password"
          type="password"
          value={password}
          onChange={setPassword}
          required
          autoComplete="current-password"
        />
        <button type="submit" className="btn-primary w-full" disabled={login.isPending}>
          {login.isPending ? 'Signing in…' : 'Log in'}
        </button>
      </form>
    </div>
  )
}
