import { useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { useCreateTeam, useLogout, useMe } from '../api/queries'
import { useRole } from '../hooks/useRole'
import { saveTheme, storedTheme, type Theme } from '../lib/theme'
import { ErrorBanner } from './ErrorBanner'
import { ErrorMessage } from './ui'

export function Logo() {
  return (
    <span className="flex items-center gap-2 text-base font-semibold tracking-tight">
      <span className="inline-block size-3 rounded-full bg-accent" aria-hidden />
      Upsera
    </span>
  )
}

/** Layout is the signed-in shell: sidebar navigation, team switcher and user menu. */
export function Layout({ teamId, children }: { teamId?: number; children: ReactNode }) {
  const { data: me } = useMe()
  const { can } = useRole(teamId ?? -1)
  const [navOpen, setNavOpen] = useState(false)
  const { pathname } = useLocation()
  const base = teamId !== undefined ? `/t/${teamId}` : ''
  const link = ({ isActive }: { isActive: boolean }) =>
    `block rounded-md px-3 py-1.5 text-sm ${isActive ? 'bg-accent-soft font-medium text-accent-ink' : 'text-ink hover:bg-line/60'}`

  return (
    <div className="min-h-screen">
      <ErrorBanner />
      <div className="flex">
        <aside
          className={`${navOpen ? 'block' : 'hidden'} fixed inset-y-0 left-0 z-20 w-60 shrink-0 border-r border-line bg-surface p-4 md:sticky md:top-0 md:block md:h-screen`}
        >
          <div className="mb-5 flex items-center justify-between">
            <Logo />
            <button type="button" className="btn md:hidden" onClick={() => setNavOpen(false)} aria-label="Close menu">
              ×
            </button>
          </div>
          {me && <TeamSwitcher teamId={teamId} />}
          <nav className="mt-4 space-y-0.5" onClick={() => setNavOpen(false)}>
            {teamId !== undefined && (
              <>
                <NavLink
                  to={base}
                  end
                  className={({ isActive }) => link({ isActive: isActive || pathname.startsWith(`${base}/monitors/`) })}
                >
                  Monitors
                </NavLink>
                <NavLink to={`${base}/channels`} className={link}>
                  Channels
                </NavLink>
                <NavLink to={`${base}/policies`} className={link}>
                  Escalation policies
                </NavLink>
                <NavLink to={`${base}/maintenance`} className={link}>
                  Maintenance
                </NavLink>
                <NavLink to={`${base}/members`} className={link}>
                  Members
                </NavLink>
                {can('owner') && (
                  <NavLink to={`${base}/tokens`} className={link}>
                    API tokens
                  </NavLink>
                )}
              </>
            )}
            {me?.user.is_admin && (
              <NavLink to="/admin" className={link}>
                Admin
              </NavLink>
            )}
          </nav>
        </aside>
        <div className="min-w-0 flex-1">
          <header className="flex items-center justify-between gap-3 border-b border-line px-4 py-2.5 md:px-8">
            <button type="button" className="btn md:hidden" onClick={() => setNavOpen(true)} aria-label="Open menu">
              ☰
            </button>
            <div className="flex-1" />
            {me && <UserMenu />}
          </header>
          <main className="mx-auto max-w-6xl px-4 py-6 md:px-8">{children}</main>
        </div>
      </div>
    </div>
  )
}

function TeamSwitcher({ teamId }: { teamId?: number }) {
  const { data: me } = useMe()
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const create = useCreateTeam()
  const teams = me?.teams ?? []

  return (
    <div>
      <label className="mb-1 block text-xs font-medium tracking-wide text-muted uppercase" htmlFor="team-switcher">
        Team
      </label>
      <select
        id="team-switcher"
        className="input"
        value={teamId ?? ''}
        onChange={(e) => {
          if (e.target.value === '__new') setCreating(true)
          else navigate(`/t/${e.target.value}`)
        }}
      >
        {teamId === undefined && <option value="">Choose a team…</option>}
        {teams.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
        <option value="__new">+ New team…</option>
      </select>
      {creating && (
        <form
          className="mt-2 space-y-2"
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(name.trim(), {
              onSuccess: (t) => {
                setCreating(false)
                setName('')
                navigate(`/t/${t.id}`)
              },
            })
          }}
        >
          <input
            className="input"
            placeholder="Team name"
            aria-label="New team name"
            value={name}
            autoFocus
            onChange={(e) => setName(e.target.value)}
          />
          <ErrorMessage error={create.error} />
          <div className="flex gap-2">
            <button type="submit" className="btn-primary" disabled={!name.trim() || create.isPending}>
              Create
            </button>
            <button type="button" className="btn" onClick={() => setCreating(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </div>
  )
}

function UserMenu() {
  const { data: me } = useMe()
  const navigate = useNavigate()
  const logout = useLogout()
  const [theme, setTheme] = useState<Theme>(storedTheme)
  const [open, setOpen] = useState(false)
  return (
    <div className="relative">
      <button type="button" className="btn" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        {me?.user.name || me?.user.email}
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-52 rounded-md border border-line bg-surface p-1 shadow-lg">
          <p className="truncate px-3 py-1.5 text-xs text-muted">{me?.user.email}</p>
          <button
            type="button"
            className="block w-full rounded px-3 py-1.5 text-left text-sm hover:bg-line/60"
            onClick={() => {
              const next = theme === 'dark' ? 'light' : 'dark'
              saveTheme(next)
              setTheme(next)
            }}
          >
            {theme === 'dark' ? 'Light theme' : 'Dark theme'}
          </button>
          <button
            type="button"
            className="block w-full rounded px-3 py-1.5 text-left text-sm hover:bg-line/60"
            onClick={() => logout.mutate(undefined, { onSettled: () => navigate('/login') })}
          >
            Log out
          </button>
        </div>
      )}
    </div>
  )
}
