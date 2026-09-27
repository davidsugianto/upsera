import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router'
import { ApiError } from '../../api/client'
import {
  keys,
  useAcknowledge,
  useAlerts,
  useDeleteMonitor,
  useHeartbeats,
  useMonitor,
  useSaveMonitor,
  useStatusEvents,
  useUptimeSummary,
} from '../../api/queries'
import type { Alert } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { HeartbeatBar } from '../../components/HeartbeatBar'
import { LatencyChart } from '../../components/LatencyChart'
import { StatusBadge, StatusDot } from '../../components/StatusBadge'
import { CopyButton, EmptyState, ErrorMessage, Loading, PageHeader, errorText, formatTime } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import { formatUptime } from '../../lib/beats'
import { applyAlert } from '../../lib/live'
import { formToBody, monitorToForm, targetSummary } from '../../lib/monitorForm'

const ranges = { '1h': 3_600_000, '6h': 21_600_000, '24h': 86_400_000 } as const
type Range = keyof typeof ranges

/** MonitorDetailPage shows a monitor's status, charts, alerts and event history. */
export function MonitorDetailPage() {
  const t = useTeamId()
  const id = useNumberParam('monitorId')
  const navigate = useNavigate()
  const { can } = useRole(t)
  const [range, setRange] = useState<Range>('1h')
  const [confirmDelete, setConfirmDelete] = useState(false)

  const monitor = useMonitor(t, id)
  const uptime = useUptimeSummary(t, id)
  const bar = useHeartbeats(t, id, 'last100', { limit: 100 })
  const chart = useHeartbeats(t, id, range, { limit: 5000, sinceMs: ranges[range] })
  const openAlerts = useAlerts(t, { state: 'open', monitorId: id })
  const events = useStatusEvents(t, id)
  const save = useSaveMonitor(t)
  const del = useDeleteMonitor(t)

  const barBeats = useMemo(() => [...(bar.data ?? [])].reverse(), [bar.data])
  const chartBeats = useMemo(() => [...(chart.data ?? [])].reverse(), [chart.data])

  if (monitor.isPending) return <Loading />
  if (monitor.error) {
    if (monitor.error instanceof ApiError && monitor.error.status === 404) {
      return (
        <EmptyState>
          This monitor was deleted.{' '}
          <Link to={`/t/${t}`} className="link">
            Back to monitors
          </Link>
        </EmptyState>
      )
    }
    return <ErrorMessage error={monitor.error} />
  }
  const m = monitor.data

  const togglePause = () => {
    save.mutate({ id, body: { ...formToBody(monitorToForm(m)), paused: !m.paused } })
  }

  return (
    <div className="space-y-6">
      <div>
        <PageHeader
          title={
            <span className="flex flex-wrap items-center gap-2">
              {m.name}
              <StatusBadge status={m.state?.status} />
              {m.state && <span className="text-xs font-normal text-muted">since {formatTime(m.state.since)}</span>}
              {(m.state?.flap_count ?? 0) >= 5 && (
                <span className="rounded-full bg-pend/10 px-2 py-0.5 text-xs font-normal text-pend">Flapping</span>
              )}
            </span>
          }
        >
          {can('editor') && (
            <>
              <button type="button" className="btn" onClick={togglePause} disabled={save.isPending}>
                {m.paused ? 'Resume' : 'Pause'}
              </button>
              <Link to={`/t/${t}/monitors/${id}/edit`} className="btn">
                Edit
              </Link>
              <button type="button" className="btn-danger" onClick={() => setConfirmDelete(true)}>
                Delete
              </button>
            </>
          )}
        </PageHeader>
        <p className="text-sm text-muted">
          {m.type} · {targetSummary(m)}
          {m.state?.tls_expires_at && <> · TLS expires {formatTime(m.state.tls_expires_at)}</>}
        </p>
        {m.type === 'push' && m.push_url && (
          <div className="mt-2 flex items-center gap-2">
            <code className="rounded-md border border-line bg-bg px-2 py-1 text-xs">{m.push_url}</code>
            <CopyButton text={m.push_url} />
          </div>
        )}
        <ErrorMessage error={save.error} />
      </div>

      <div className="grid grid-cols-3 gap-4">
        <UptimeCard label="24h uptime" value={uptime.data?.uptime_24h} />
        <UptimeCard label="7d uptime" value={uptime.data?.uptime_7d} />
        <UptimeCard label="30d uptime" value={uptime.data?.uptime_30d} />
      </div>

      <section className="card p-4">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-semibold">Latency</h2>
          <div className="flex gap-1.5">
            {(Object.keys(ranges) as Range[]).map((r) => (
              <button
                key={r}
                type="button"
                className={`rounded-full border px-2.5 py-1 text-xs ${
                  range === r ? 'border-accent bg-accent-soft text-accent-ink' : 'border-line text-muted hover:text-ink'
                }`}
                onClick={() => setRange(r)}
              >
                {r}
              </button>
            ))}
          </div>
        </div>
        {chart.isPending ? <Loading /> : <LatencyChart beats={chartBeats} />}
      </section>

      <section className="card p-4">
        <h2 className="mb-3 text-sm font-semibold">Recent heartbeats</h2>
        {bar.isPending ? <Loading /> : <HeartbeatBar beats={barBeats} slots={100} />}
      </section>

      <section className="card">
        <h2 className="border-b border-line px-4 py-3 text-sm font-semibold">Open alerts</h2>
        {openAlerts.isPending ? (
          <Loading />
        ) : (openAlerts.data ?? []).length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-muted">No open alerts.</p>
        ) : (
          <div>
            {(openAlerts.data ?? []).map((a) => (
              <AlertRow key={a.id} t={t} monitorId={id} alert={a} canAck={can('editor')} />
            ))}
          </div>
        )}
      </section>

      <section className="card">
        <h2 className="border-b border-line px-4 py-3 text-sm font-semibold">Event history</h2>
        {events.isPending ? (
          <Loading />
        ) : (events.data ?? []).length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-muted">No events yet.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line text-left text-xs text-muted">
                <th className="px-4 py-2 font-medium">Time</th>
                <th className="px-4 py-2 font-medium">Change</th>
                <th className="px-4 py-2 font-medium">Message</th>
              </tr>
            </thead>
            <tbody>
              {(events.data ?? []).map((ev, i) => (
                <tr key={i} className="border-b border-line last:border-0">
                  <td className="px-4 py-2 whitespace-nowrap">{formatTime(ev.time)}</td>
                  <td className="px-4 py-2 whitespace-nowrap">
                    <span className="inline-flex items-center gap-1.5">
                      <StatusDot status={ev.previous_status} />
                      {ev.previous_status ?? 'unknown'}
                      <span className="text-muted">→</span>
                      <StatusDot status={ev.status} />
                      {ev.status}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-muted">{ev.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <ConfirmDialog
        open={confirmDelete}
        title={`Delete ${m.name}?`}
        busy={del.isPending}
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() => del.mutate(id, { onSuccess: () => navigate(`/t/${t}`) })}
      >
        This can't be undone. Its heartbeats, alerts and event history will be deleted too.
      </ConfirmDialog>
    </div>
  )
}

function UptimeCard({ label, value }: { label: string; value: number | null | undefined }) {
  return (
    <div className="card p-4">
      <div className="text-xs text-muted">{label}</div>
      <div className="mt-1 text-2xl font-semibold">{formatUptime(value)}</div>
    </div>
  )
}

function AlertRow({
  t,
  monitorId,
  alert,
  canAck,
}: {
  t: number
  monitorId: number
  alert: Alert
  canAck: boolean
}) {
  const qc = useQueryClient()
  const ack = useAcknowledge(t)
  const [localError, setLocalError] = useState<string | null>(null)

  const acknowledge = () => {
    setLocalError(null)
    ack.mutate(alert.id, {
      onSuccess: (updated) => {
        const filter = { state: 'open' as const, monitorId }
        qc.setQueryData<Alert[]>(keys.alerts(t, filter), (l) => applyAlert(l, updated, filter))
      },
      onError: (err) => {
        setLocalError(err instanceof ApiError && err.status === 409 ? 'alert is resolved' : errorText(err))
      },
    })
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3 last:border-0">
      <div>
        <div className="text-sm font-medium">
          Step {alert.step} · opened {formatTime(alert.opened_at)}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
          {alert.suppressed && <span className="rounded-full bg-line px-2 py-0.5">Suppressed</span>}
          {alert.flapping && <span className="rounded-full bg-line px-2 py-0.5">Flapping</span>}
          <span className="text-muted">
            {alert.acked ? `Acked by ${alert.acked_by_name || 'unknown'} at ${formatTime(alert.acked_at)}` : 'Not acknowledged'}
          </span>
        </div>
        {alert.message && <p className="mt-1 text-xs text-muted">{alert.message}</p>}
        {localError && <p className="mt-1 text-xs text-down">{localError}</p>}
      </div>
      {canAck && !alert.acked && (
        <button type="button" className="btn" onClick={acknowledge} disabled={ack.isPending}>
          Acknowledge
        </button>
      )}
    </div>
  )
}
