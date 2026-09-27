import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useMonitors, useOverview } from '../../api/queries'
import type { Monitor, OverviewItem, Status } from '../../api/types'
import { HeartbeatBar } from '../../components/HeartbeatBar'
import { StatusDot } from '../../components/StatusBadge'
import { EmptyState, ErrorMessage, Loading, PageHeader } from '../../components/ui'
import { useRole } from '../../hooks/useRole'
import { useTeamId } from '../../hooks/useTeamId'
import { formatUptime } from '../../lib/beats'
import { targetSummary } from '../../lib/monitorForm'

const statuses: { key: Status; label: string }[] = [
  { key: 'up', label: 'Up' },
  { key: 'down', label: 'Down' },
  { key: 'pending', label: 'Pending' },
  { key: 'maintenance', label: 'Maintenance' },
]

/** MonitorsPage lists the team's monitors, grouped, searchable and tag-filterable. */
export function MonitorsPage() {
  const t = useTeamId()
  const { can } = useRole(t)
  const monitors = useMonitors(t)
  const overview = useOverview(t)
  const [q, setQ] = useState('')
  const [tag, setTag] = useState<string | null>(null)

  const list = useMemo(() => monitors.data ?? [], [monitors.data])

  const overviewByID = useMemo(() => {
    const m = new Map<number, OverviewItem>()
    for (const o of overview.data ?? []) m.set(o.monitor_id, o)
    return m
  }, [overview.data])

  const counts = useMemo(() => {
    const c: Record<Status, number> = { up: 0, down: 0, pending: 0, maintenance: 0 }
    for (const m of list) if (m.state) c[m.state.status]++
    return c
  }, [list])

  const allTags = useMemo(() => {
    const s = new Set<string>()
    for (const m of list) for (const tg of m.tags ?? []) s.add(tg)
    return [...s].sort((a, b) => a.localeCompare(b))
  }, [list])

  const filtered = useMemo(() => {
    const query = q.trim().toLowerCase()
    return list.filter((m) => {
      if (tag && !(m.tags ?? []).includes(tag)) return false
      if (!query) return true
      return m.name.toLowerCase().includes(query) || targetSummary(m).toLowerCase().includes(query)
    })
  }, [list, q, tag])

  const groups = useMemo(() => {
    const byGroup = new Map<string, Monitor[]>()
    for (const m of filtered) {
      const key = m.group_name.trim() === '' ? 'Ungrouped' : m.group_name.trim()
      const arr = byGroup.get(key) ?? []
      arr.push(m)
      byGroup.set(key, arr)
    }
    const names = [...byGroup.keys()].filter((n) => n !== 'Ungrouped').sort((a, b) => a.localeCompare(b))
    if (byGroup.has('Ungrouped')) names.push('Ungrouped')
    return names.map((name) => ({ name, monitors: byGroup.get(name)! }))
  }, [filtered])

  if (monitors.isPending) return <Loading />
  if (monitors.error) return <ErrorMessage error={monitors.error} />

  return (
    <div>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-3">
            Monitors
            <span className="flex flex-wrap items-center gap-2.5 text-xs font-normal text-muted">
              {statuses.map((s) => (
                <span key={s.key} className="inline-flex items-center gap-1">
                  <StatusDot status={s.key} />
                  {counts[s.key]} {s.label}
                </span>
              ))}
            </span>
          </span>
        }
      >
        {can('editor') && (
          <Link to={`/t/${t}/monitors/new`} className="btn-primary">
            Add monitor
          </Link>
        )}
      </PageHeader>

      {overview.error && <ErrorMessage error={overview.error} />}

      {list.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <input
            className="input max-w-xs"
            placeholder="Search by name or target…"
            aria-label="Search monitors"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          {allTags.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {allTags.map((tg) => (
                <button
                  key={tg}
                  type="button"
                  className={`rounded-full border px-2.5 py-1 text-xs ${
                    tag === tg ? 'border-accent bg-accent-soft text-accent-ink' : 'border-line text-muted hover:text-ink'
                  }`}
                  onClick={() => setTag(tag === tg ? null : tg)}
                >
                  {tg}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {list.length === 0 ? (
        <EmptyState>
          No monitors yet.{' '}
          {can('editor') && (
            <Link to={`/t/${t}/monitors/new`} className="link">
              Create your first monitor
            </Link>
          )}
        </EmptyState>
      ) : filtered.length === 0 ? (
        <EmptyState>No monitors match your filters.</EmptyState>
      ) : (
        <div className="space-y-6">
          {groups.map((g) => (
            <section key={g.name}>
              <h2 className="mb-2 text-sm font-semibold text-muted">{g.name}</h2>
              <div className="card divide-y divide-line">
                {g.monitors.map((m) => (
                  <MonitorRow key={m.id} t={t} m={m} o={overviewByID.get(m.id)} />
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  )
}

function MonitorRow({ t, m, o }: { t: number; m: Monitor; o?: OverviewItem }) {
  const beats = o?.heartbeats ?? []
  const latest = beats.length > 0 ? beats[beats.length - 1] : undefined
  const latency = latest && (latest.status === 'up' || latest.status === 'pending') ? `${latest.latency_ms} ms` : '—'
  return (
    <Link to={`/t/${t}/monitors/${m.id}`} className="flex flex-wrap items-center gap-4 px-4 py-3 hover:bg-line/40">
      <StatusDot status={m.state?.status} />
      <div className="min-w-40 flex-1">
        <div className="flex items-center gap-2 text-sm font-medium">
          {m.name}
          {m.paused && (
            <span className="rounded-full border border-line px-1.5 py-0.5 text-[10px] text-muted">Paused</span>
          )}
        </div>
        <div className="text-xs text-muted">
          {m.type} · {targetSummary(m)}
        </div>
      </div>
      <HeartbeatBar beats={beats} slots={50} className="hidden sm:flex" />
      <div className="w-16 shrink-0 text-right text-xs text-muted">
        24h
        <span className="block text-sm text-ink">{formatUptime(o?.uptime_24h)}</span>
      </div>
      <div className="w-16 shrink-0 text-right text-xs text-muted">
        30d
        <span className="block text-sm text-ink">{formatUptime(o?.uptime_30d)}</span>
      </div>
      <div className="w-16 shrink-0 text-right text-xs text-muted">
        Latency
        <span className="block text-sm text-ink">{latency}</span>
      </div>
    </Link>
  )
}
