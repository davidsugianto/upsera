import { useEffect, useRef, useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { useCreateTeam, useLogout, useMe } from '../api/queries'
import { useRole } from '../hooks/useRole'
import { resolveTheme, saveChoice, storedChoice, type ThemeChoice } from '../lib/theme'
import { ErrorBanner } from './ErrorBanner'
import { ErrorMessage } from './ui'

export function Logo() {
  return (
    <span className="flex items-center gap-2 text-base font-semibold tracking-tight">
      <span className="inline-block size-3 shrink-0 rounded-full bg-accent" aria-hidden />
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
    `flex h-9 items-center rounded-md px-3 text-sm transition-colors ${isActive ? 'bg-accent font-medium text-white' : 'text-ink hover:bg-surface-2'}`
  const opener = useRef<HTMLButtonElement>(null)
  const closer = useRef<HTMLButtonElement>(null)

  // Mobile rail: Escape closes it, focus moves into it on open and back to the opener on close.
  useEffect(() => {
    if (!navOpen) return
    closer.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setNavOpen(false)
        opener.current?.focus()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [navOpen])

  const closeNav = () => {
    setNavOpen(false)
    opener.current?.focus()
  }

  return (
    <div className="min-h-screen">
      <ErrorBanner />
      <div className="flex">
        {navOpen && <div className="fixed inset-0 z-20 bg-black/50 md:hidden" onClick={closeNav} aria-hidden />}
        <aside
          className={`${navOpen ? 'block' : 'hidden'} fixed inset-y-0 left-0 z-30 w-60 shrink-0 overflow-y-auto border-r border-line bg-surface p-4 md:sticky md:top-0 md:block md:h-screen`}
        >
          <div className="mb-5 flex items-center justify-between">
            <Logo />
            <button
              ref={closer}
              type="button"
              className="btn min-h-9 min-w-9 md:hidden"
              onClick={closeNav}
              aria-label="Close menu"
            >
              ×
            </button>
          </div>
          {me && <TeamSwitcher teamId={teamId} />}
          <nav className="mt-4 space-y-0.5" onClick={closeNav}>
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
                <NavLink to={`${base}/docker-hosts`} className={link}>
                  Docker hosts
                </NavLink>
                <NavLink to={`${base}/status-pages`} className={link}>
                  Status pages
                </NavLink>
                <NavLink to={`${base}/policies`} className={link}>
                  Escalation policies
                </NavLink>
                <NavLink to={`${base}/maintenance`} className={link}>
                  Maintenance
                </NavLink>
                <NavLink to={`${base}/reports`} className={link}>
                  Reports
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
          <header className="sticky top-0 z-10 flex items-center justify-between gap-3 border-b border-line bg-bg px-4 py-2 md:px-8">
            <button
              ref={opener}
              type="button"
              className="btn min-h-9 min-w-9 md:hidden"
              onClick={() => setNavOpen(true)}
              aria-label="Open menu"
            >
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
      <label className="mb-1 block text-xs font-medium text-muted" htmlFor="team-switcher">
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
            <button type="submit" className="btn-primary min-h-9" disabled={!name.trim() || create.isPending}>
              Create
            </button>
            <button type="button" className="btn min-h-9" onClick={() => setCreating(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </div>
  )
}

const themeOptions: { value: ThemeChoice; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

function UserMenu() {
  const { data: me } = useMe()
  const navigate = useNavigate()
  const logout = useLogout()
  const [choice, setChoice] = useState<ThemeChoice>(storedChoice)
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)

  // Escape and a click outside close the menu.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        trigger.current?.focus()
      }
    }
    const onDown = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('mousedown', onDown)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('mousedown', onDown)
    }
  }, [open])

  const item = 'flex min-h-9 w-full items-center gap-2 rounded-md px-3 text-left text-sm transition-colors hover:bg-surface-2'
  return (
    <div className="relative" ref={root}>
      <button
        ref={trigger}
        type="button"
        className="btn min-h-9"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        {me?.user.name || me?.user.email}
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-56 rounded-lg border border-line bg-surface p-1">
          <p className="truncate px-3 py-1.5 text-xs text-muted">{me?.user.email}</p>
          <div role="group" aria-label="Theme" className="border-y border-line py-1">
            <p className="px-3 py-1 text-xs text-muted">Theme</p>
            {themeOptions.map((o) => (
              <button
                key={o.value}
                type="button"
                aria-pressed={choice === o.value}
                title={o.value === 'system' ? `Follows your device (now ${resolveTheme('system')})` : undefined}
                className={item}
                onClick={() => {
                  saveChoice(o.value)
                  setChoice(o.value)
                }}
              >
                <span
                  aria-hidden
                  className={`size-2.5 shrink-0 rounded-full border ${choice === o.value ? 'border-accent bg-accent' : 'border-line-strong'}`}
                />
                {o.label}
              </button>
            ))}
          </div>
          <button
            type="button"
            className={`${item} mt-1`}
            onClick={() => logout.mutate(undefined, { onSettled: () => navigate('/login') })}
          >
            Log out
          </button>
        </div>
      )}
    </div>
  )
}
