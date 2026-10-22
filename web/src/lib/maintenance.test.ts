import { describe, expect, it } from 'vitest'
import type { MaintenanceWindow } from '../api/types'
import { phaseOf } from './maintenance'

const hour = 3_600_000
const t0 = Date.parse('2026-10-01T10:00:00Z')

function win(recurrence: MaintenanceWindow['recurrence'], startOffsetH = 0, lenH = 2): Pick<MaintenanceWindow, 'starts_at' | 'ends_at' | 'recurrence'> {
  return {
    starts_at: new Date(t0 + startOffsetH * hour).toISOString(),
    ends_at: new Date(t0 + (startOffsetH + lenH) * hour).toISOString(),
    recurrence,
  }
}

describe('phaseOf', () => {
  it('classifies a one-time window as scheduled, active, then past', () => {
    const w = win('none')
    expect(phaseOf(w, t0 - hour)).toBe('scheduled')
    expect(phaseOf(w, t0)).toBe('active')
    expect(phaseOf(w, t0 + hour)).toBe('active')
    expect(phaseOf(w, t0 + 2 * hour)).toBe('past')
    expect(phaseOf(w, t0 + 90 * 24 * hour)).toBe('past')
  })

  it('classifies a recurring window by its first occurrence', () => {
    for (const r of ['daily', 'weekly'] as const) {
      const w = win(r)
      expect(phaseOf(w, t0 - hour)).toBe('scheduled')
      expect(phaseOf(w, t0 + hour)).toBe('active')
    }
  })

  it('does not guess a phase for a recurring window after its first occurrence', () => {
    // A later occurrence may be running right now; the client cannot tell, so it must not say "Scheduled".
    for (const r of ['daily', 'weekly'] as const) {
      expect(phaseOf(win(r), t0 + 3 * hour)).toBeNull()
      expect(phaseOf(win(r), t0 + 24 * hour + hour)).toBeNull()
    }
  })
})
