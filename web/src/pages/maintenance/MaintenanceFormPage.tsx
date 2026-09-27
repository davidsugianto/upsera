import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { useMaintenanceWindows, useMonitors, useSaveMaintenance } from '../../api/queries'
import type { MaintenanceRequest, MaintenanceWindow, Monitor } from '../../api/types'
import { MultiCheck, Select, TextField } from '../../components/Fields'
import { EmptyState, ErrorMessage, Loading, PageHeader, fieldError } from '../../components/ui'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'

type Recurrence = MaintenanceWindow['recurrence']

type FormState = {
  name: string
  startsAt: string
  endsAt: string
  recurrence: Recurrence
  monitorIds: number[]
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** formatLocal renders a Date as a `datetime-local` input value in the browser's local time. */
function formatLocal(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function defaultForm(): FormState {
  const start = new Date()
  start.setSeconds(0, 0)
  const end = new Date(start.getTime() + 60 * 60 * 1000)
  return { name: '', startsAt: formatLocal(start), endsAt: formatLocal(end), recurrence: 'none', monitorIds: [] }
}

function toForm(w: MaintenanceWindow): FormState {
  return {
    name: w.name,
    startsAt: formatLocal(new Date(w.starts_at)),
    endsAt: formatLocal(new Date(w.ends_at)),
    recurrence: w.recurrence,
    monitorIds: w.monitor_ids ?? [],
  }
}

const recurrenceOptions: { value: Recurrence; label: string }[] = [
  { value: 'none', label: 'One-time' },
  { value: 'daily', label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
]

/** MaintenanceFormPage creates or edits a maintenance window. */
export function MaintenanceFormPage() {
  const t = useTeamId()
  const windowId = useNumberParam('windowId')
  const isEdit = !Number.isNaN(windowId)
  const windows = useMaintenanceWindows(t)
  const monitors = useMonitors(t)

  if ((isEdit && windows.isPending) || monitors.isPending) return <Loading />
  if (isEdit && windows.error) return <ErrorMessage error={windows.error} />
  if (monitors.error) return <ErrorMessage error={monitors.error} />

  const windowList = windows.data ?? []
  const monitorList = monitors.data ?? []
  const existing = isEdit ? windowList.find((w) => w.id === windowId) : undefined
  if (isEdit && !existing) {
    return (
      <div>
        <PageHeader title="Maintenance window" />
        <EmptyState>
          This maintenance window was deleted or does not exist.{' '}
          <Link to={`/t/${t}/maintenance`} className="link">
            Back to maintenance windows
          </Link>
        </EmptyState>
      </div>
    )
  }

  return <MaintenanceForm t={t} existing={existing} monitors={monitorList} />
}

function MaintenanceForm({
  t,
  existing,
  monitors,
}: {
  t: number
  existing?: MaintenanceWindow
  monitors: Monitor[]
}) {
  const navigate = useNavigate()
  const save = useSaveMaintenance(t)
  const [form, setForm] = useState<FormState>(() => (existing ? toForm(existing) : defaultForm()))
  const [clientError, setClientError] = useState<string>()

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((f) => ({ ...f, [key]: value }))
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setClientError(undefined)

    const starts = new Date(form.startsAt)
    const ends = new Date(form.endsAt)
    if (Number.isNaN(starts.getTime()) || Number.isNaN(ends.getTime())) {
      setClientError('Enter valid start and end times.')
      return
    }
    if (ends.getTime() <= starts.getTime()) {
      setClientError('The end time must be after the start time.')
      return
    }
    if (form.monitorIds.length === 0) {
      setClientError('Select at least one monitor.')
      return
    }
    if (form.monitorIds.length > 100) {
      setClientError('Select at most 100 monitors.')
      return
    }

    const body: MaintenanceRequest = {
      name: form.name,
      starts_at: starts.toISOString(),
      ends_at: ends.toISOString(),
      recurrence: form.recurrence,
      monitor_ids: form.monitorIds,
    }
    save.mutate(
      { id: existing?.id, body },
      { onSuccess: () => navigate(`/t/${t}/maintenance`) },
    )
  }

  return (
    <div>
      <PageHeader title={existing ? 'Edit maintenance window' : 'Add maintenance window'} />
      <form onSubmit={onSubmit} className="card max-w-xl space-y-4 p-5">
        <TextField
          label="Name"
          value={form.name}
          onChange={(v) => update('name', v)}
          required
          error={fieldError(save.error, 'name')}
        />
        <div className="grid gap-4 sm:grid-cols-2">
          <TextField
            label="Starts at"
            type="datetime-local"
            value={form.startsAt}
            onChange={(v) => update('startsAt', v)}
            required
            error={fieldError(save.error, 'starts_at')}
          />
          <TextField
            label="Ends at"
            type="datetime-local"
            value={form.endsAt}
            onChange={(v) => update('endsAt', v)}
            required
            error={fieldError(save.error, 'ends_at')}
          />
        </div>
        <Select
          label="Recurrence"
          value={form.recurrence}
          onChange={(v) => update('recurrence', v as Recurrence)}
          options={recurrenceOptions}
          help="Daily and weekly windows repeat at the same wall-clock time in the server's TZ."
        />
        <MultiCheck
          label="Monitors"
          options={monitors.map((m) => ({ value: m.id, label: m.name }))}
          value={form.monitorIds}
          onChange={(v) => update('monitorIds', v)}
          error={fieldError(save.error, 'monitor_ids')}
          empty="No monitors yet."
        />
        {clientError && <ErrorMessage error={clientError} />}
        <ErrorMessage error={save.error} />
        <div className="flex gap-2">
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {existing ? 'Save' : 'Create'}
          </button>
          <Link to={`/t/${t}/maintenance`} className="btn">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  )
}
