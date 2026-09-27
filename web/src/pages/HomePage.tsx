import { Navigate } from 'react-router'
import { useMe, useSetupStatus } from '../api/queries'
import { lastTeam } from '../lib/lastTeam'
import { ErrorMessage, Loading } from '../components/ui'

/** HomePage routes to setup, login, the last-used team, or the no-team page. */
export function HomePage() {
  const setup = useSetupStatus()
  const me = useMe()
  if (setup.error || me.error) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorMessage error={setup.error ?? me.error} />
      </div>
    )
  }
  if (setup.isPending || me.isPending) return <Loading />
  if (setup.data.needed) return <Navigate to="/setup" replace />
  if (!me.data) return <Navigate to="/login" replace />
  const teams = me.data.teams ?? []
  if (teams.length === 0) return <Navigate to="/no-team" replace />
  const last = lastTeam()
  const team = teams.find((t) => t.id === last) ?? teams[0]!
  return <Navigate to={`/t/${team.id}`} replace />
}
