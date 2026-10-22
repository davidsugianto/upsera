import type { MaintenanceWindow } from '../api/types'

export type Phase = 'active' | 'scheduled' | 'past'

/**
 * phaseOf classifies a window by its first occurrence. A recurring window has no phase once that
 * occurrence is over: a later one may be running right now and the client does not know the
 * schedule, so it says nothing rather than "Scheduled".
 */
export function phaseOf(w: Pick<MaintenanceWindow, 'starts_at' | 'ends_at' | 'recurrence'>, now: number): Phase | null {
  const start = new Date(w.starts_at).getTime()
  const end = new Date(w.ends_at).getTime()
  if (now < start) return 'scheduled'
  if (now < end) return 'active'
  return w.recurrence === 'none' ? 'past' : null
}
