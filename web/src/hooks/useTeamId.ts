import { useParams } from 'react-router'

/** useTeamId returns the :teamId route parameter as a number. */
export function useTeamId(): number {
  return Number(useParams().teamId)
}

/** useNumberParam returns a numeric route parameter (NaN when absent). */
export function useNumberParam(name: string): number {
  return Number(useParams()[name])
}
