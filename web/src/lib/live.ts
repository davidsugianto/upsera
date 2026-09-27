import type { Alert, AlertFilter, Monitor, MonitorEvent } from '../api/types'

/** patchMonitorState applies a live `monitor` event to a cached monitor list. */
export function patchMonitorState(list: Monitor[] | undefined, ev: MonitorEvent): Monitor[] | undefined {
  if (!list) return list
  let changed = false
  const next = list.map((m) => {
    if (m.id !== ev.monitor_id) return m
    changed = true
    const state = m.state
      ? { ...m.state, status: ev.to, since: ev.since }
      : {
          status: ev.to,
          since: ev.since,
          last_check_at: ev.at,
          consecutive_failures: 0,
          tls_expires_at: null,
          flap_count: 0,
        }
    return { ...m, state }
  })
  return changed ? next : list
}

/** applyAlert merges a live or acknowledged alert into a cached alert list for filter. */
export function applyAlert(list: Alert[] | undefined, a: Alert, filter: AlertFilter): Alert[] | undefined {
  if (!list) return list
  if (filter.monitorId !== undefined && filter.monitorId !== a.monitor_id) return list
  const without = list.filter((x) => x.id !== a.id)
  const resolved = a.resolved_at !== null
  let keep: boolean
  switch (filter.state) {
    case 'open':
      keep = !resolved
      break
    case 'resolved':
      keep = resolved
      break
    case 'all':
      keep = true
      break
  }
  const next = keep ? [...without, a] : without
  return next.sort((x, y) => Date.parse(y.opened_at) - Date.parse(x.opened_at))
}
