import { useEffect } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router'
import { useMe } from '../api/queries'
import { Layout } from '../components/Layout'
import { ErrorMessage, Loading } from '../components/ui'
import { useTeamEvents } from '../hooks/useTeamEvents'
import { useTeamId } from '../hooks/useTeamId'
import { rememberTeam } from '../lib/lastTeam'

/** TeamLayout guards /t/:teamId routes and keeps the team's data live. */
export function TeamLayout() {
  const teamId = useTeamId()
  const me = useMe()
  const loc = useLocation()
  const member = me.data?.teams?.some((t) => t.id === teamId) ?? false

  if (me.isPending) return <Loading />
  if (me.error) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorMessage error={me.error} />
      </div>
    )
  }
  if (!me.data) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />
  if (!member) return <Navigate to="/" replace />
  return <TeamShell teamId={teamId} />
}

function TeamShell({ teamId }: { teamId: number }) {
  useTeamEvents(teamId)
  useEffect(() => rememberTeam(teamId), [teamId])
  return (
    <Layout teamId={teamId}>
      <Outlet key={teamId} />
    </Layout>
  )
}
