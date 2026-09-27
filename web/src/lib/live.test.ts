import { describe, expect, it } from 'vitest'
import type { Alert, Monitor, MonitorEvent } from '../api/types'
import { applyAlert, patchMonitorState } from './live'

const monitor = (id: number, state: Monitor['state']): Monitor =>
  ({ id, name: `m${id}`, state }) as Monitor

const ev: MonitorEvent = {
  monitor_id: 1,
  from: 'up',
  to: 'down',
  at: '2026-01-01T00:00:10Z',
  since: '2026-01-01T00:00:00Z',
  message: 'timeout',
  flapping: false,
}

describe('patchMonitorState', () => {
  it('updates an existing state', () => {
    const existing = monitor(1, {
      status: 'up',
      since: '2025-12-31T00:00:00Z',
      last_check_at: '2026-01-01T00:00:00Z',
      consecutive_failures: 0,
      tls_expires_at: '2026-06-01T00:00:00Z',
      flap_count: 2,
    })
    const other = monitor(2, null)
    const got = patchMonitorState([existing, other], ev)!
    expect(got[0]!.state).toMatchObject({ status: 'down', since: ev.since, flap_count: 2 })
    expect(got[0]!.state!.tls_expires_at).toBe('2026-06-01T00:00:00Z')
    expect(got[1]).toBe(other)
  })
  it('creates a state for a monitor that had none', () => {
    const got = patchMonitorState([monitor(1, null)], ev)!
    expect(got[0]!.state).toEqual({
      status: 'down',
      since: ev.since,
      last_check_at: ev.at,
      consecutive_failures: 0,
      tls_expires_at: null,
      flap_count: 0,
    })
  })
  it('leaves unknown ids untouched', () => {
    const list = [monitor(2, null)]
    expect(patchMonitorState(list, ev)).toBe(list)
    expect(patchMonitorState(undefined, ev)).toBeUndefined()
  })
})

const alert = (id: string, monitorId: number, openedAt: string, resolvedAt: string | null = null): Alert =>
  ({ id, monitor_id: monitorId, opened_at: openedAt, resolved_at: resolvedAt, acked: false }) as Alert

describe('applyAlert', () => {
  it('drops a resolved alert from an open list', () => {
    const open = alert('a', 1, '2026-01-01T00:00:00Z')
    const resolved = { ...open, resolved_at: '2026-01-01T01:00:00Z' }
    expect(applyAlert([open], resolved, { state: 'open' })).toEqual([])
  })
  it('adds a resolved alert only to resolved lists', () => {
    const resolved = alert('a', 1, '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z')
    expect(applyAlert([], resolved, { state: 'resolved' })).toEqual([resolved])
    expect(applyAlert([], alert('b', 1, '2026-01-01T00:00:00Z'), { state: 'resolved' })).toEqual([])
  })
  it('ignores other monitors when filtered by monitor', () => {
    const list = [alert('a', 1, '2026-01-01T00:00:00Z')]
    expect(applyAlert(list, alert('b', 2, '2026-01-02T00:00:00Z'), { state: 'all', monitorId: 1 })).toBe(list)
  })
  it('replaces by id and sorts newest first', () => {
    const older = alert('a', 1, '2026-01-01T00:00:00Z')
    const newer = alert('b', 1, '2026-01-02T00:00:00Z')
    const acked = { ...older, acked: true }
    const got = applyAlert([older, newer], acked, { state: 'open' })!
    expect(got.map((x) => x.id)).toEqual(['b', 'a'])
    expect(got[1]!.acked).toBe(true)
  })
})
