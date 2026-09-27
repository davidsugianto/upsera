import { useMe } from '../api/queries'
import type { Role } from '../api/types'

const rank: Record<Role, number> = { viewer: 0, editor: 1, owner: 2 }

/** useRole returns the caller's role in teamId and a `can(min)` check (viewer < editor < owner). */
export function useRole(teamId: number) {
  const { data: me } = useMe()
  const role = me?.teams?.find((t) => t.id === teamId)?.role
  return {
    role,
    can: (min: Role) => role !== undefined && rank[role] >= rank[min],
  }
}
