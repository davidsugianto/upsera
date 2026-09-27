import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { keys } from '../api/queries'
import type { Alert, AlertFilter, Monitor, MonitorEvent, MonitorsChangedEvent } from '../api/types'
import { applyAlert, patchMonitorState } from '../lib/live'

const reconnectDelayMs = 5_000
const trailingMs = 2_000

/**
 * useTeamEvents keeps the team's cached queries live from the SSE stream:
 * state transitions patch monitors, alert changes patch alert lists, and
 * monitor-list changes refetch. Steady-state checks are not streamed; the
 * heartbeat queries poll instead.
 */
export function useTeamEvents(teamId: number) {
  const qc = useQueryClient()

  useEffect(() => {
    let es: EventSource | null = null
    let reconnectTimer: number | undefined
    const trailing = new Map<string, number>()
    let hadError = false
    let closed = false

    // invalidateSoon refetches queryKey once, 2s after the first call of a burst.
    const invalidateSoon = (queryKey: readonly unknown[]) => {
      const id = JSON.stringify(queryKey)
      if (trailing.has(id)) return
      trailing.set(
        id,
        window.setTimeout(() => {
          trailing.delete(id)
          void qc.invalidateQueries({ queryKey })
        }, trailingMs),
      )
    }

    const onMonitor = (e: MessageEvent<string>) => {
      const ev = JSON.parse(e.data) as MonitorEvent
      const id = ev.monitor_id
      qc.setQueryData<Monitor[]>(keys.monitors(teamId), (l) => patchMonitorState(l, ev))
      qc.setQueryData<Monitor>(keys.monitor(teamId, id), (m) => (m ? patchMonitorState([m], ev)?.[0] : m))
      void qc.invalidateQueries({ queryKey: keys.heartbeatsAll(teamId, id) })
      void qc.invalidateQueries({ queryKey: keys.uptimeSummary(teamId, id) })
      void qc.invalidateQueries({ queryKey: keys.statusEvents(teamId, id) })
      invalidateSoon(keys.overview(teamId))
    }

    const onAlert = (e: MessageEvent<string>) => {
      const alert = JSON.parse(e.data) as Alert
      for (const [key] of qc.getQueriesData<Alert[]>({ queryKey: keys.alertsAll(teamId) })) {
        const filter = key[2] as AlertFilter | undefined
        if (!filter) continue
        qc.setQueryData<Alert[]>(key, (l) => applyAlert(l, alert, filter))
      }
      // The engine persists alerts asynchronously, so a list fetched just
      // before the flush can miss this change; refetch once it has landed.
      invalidateSoon(keys.alertsAll(teamId))
    }

    const onMonitorsChanged = (e: MessageEvent<string>) => {
      const ev = JSON.parse(e.data) as MonitorsChangedEvent
      void qc.invalidateQueries({ queryKey: keys.monitors(teamId) })
      void qc.invalidateQueries({ queryKey: keys.overview(teamId) })
      if (ev.deleted) qc.removeQueries({ queryKey: keys.monitor(teamId, ev.monitor_id) })
      else void qc.invalidateQueries({ queryKey: keys.monitor(teamId, ev.monitor_id) })
    }

    const connect = () => {
      if (closed) return
      const src = new EventSource(`/api/teams/${teamId}/events`)
      es = src
      src.addEventListener('monitor', onMonitor)
      src.addEventListener('alert', onAlert)
      src.addEventListener('monitors_changed', onMonitorsChanged)
      src.addEventListener('open', () => {
        if (!hadError) return
        hadError = false
        // Catch up on everything missed while disconnected.
        void qc.invalidateQueries({ predicate: (q) => q.queryKey[1] === teamId })
      })
      src.addEventListener('error', () => {
        hadError = true
        if (src.readyState !== EventSource.CLOSED) return // the browser retries by itself
        // An HTTP error (401, 503, ...) stops EventSource for good: re-check
        // the session and reopen later.
        src.close()
        void qc.invalidateQueries({ queryKey: keys.me })
        reconnectTimer = window.setTimeout(connect, reconnectDelayMs)
      })
    }
    connect()

    return () => {
      closed = true
      es?.close()
      clearTimeout(reconnectTimer)
      for (const timer of trailing.values()) clearTimeout(timer)
    }
  }, [qc, teamId])
}
