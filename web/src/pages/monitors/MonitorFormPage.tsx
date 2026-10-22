import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import {
  useChannels,
  useDockerContainers,
  useDockerHosts,
  useMonitor,
  useMonitors,
  usePolicies,
  useDeleteSLO,
  useRegions,
  useSaveMonitor,
  useSaveSLO,
  useSLO,
} from '../../api/queries'
import type { MonitorType, SLO, SLOInput } from '../../api/types'
import { Checkbox, KeyValueEditor, MultiCheck, NumberField, Select, TagInput, TextField } from '../../components/Fields'
import { ErrorMessage, Loading, PageHeader, errorText, fieldError } from '../../components/ui'
import { Link } from 'react-router'
import { useRole } from '../../hooks/useRole'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import {
  canRunRemotely,
  clampQuorum,
  defaultMonitorForm,
  defaultQuorum,
  dnsRecordTypes,
  formToBody,
  httpMethods,
  methodsWithBody,
  monitorToForm,
  monitorTypes,
  setRegions,
  validateMonitorForm,
  type MonitorForm,
} from '../../lib/monitorForm'

const noneOption = { value: '', label: 'None' }

/** Module is one hairline group of the form: a heading and a two-column grid that collapses on phones. */
function Module({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="card p-4">
      <h2 className="mb-4 text-base font-semibold text-balance">{title}</h2>
      <div className="grid gap-4 sm:grid-cols-2">{children}</div>
    </section>
  )
}

const wide = 'sm:col-span-2'

/** SLOForm is the monitor form's service level objective section. */
export type SLOForm = {
  enabled: boolean
  target: number
  window_days: number
  fast_burn: number
  slow_burn: number
}

/** defaultSLOForm is off, with the server's defaults ready for when it is turned on. */
export function defaultSLOForm(): SLOForm {
  return {
    enabled: false,
    target: 99.9,
    window_days: 30,
    fast_burn: 14.4,
    slow_burn: 6,
  }
}

/** sloToForm loads an existing SLO into the form (null: the monitor has none). */
export function sloToForm(slo: SLO | null | undefined): SLOForm {
  if (!slo) return defaultSLOForm()
  return {
    enabled: true,
    target: slo.target,
    window_days: slo.window_days,
    fast_burn: slo.fast_burn,
    slow_burn: slo.slow_burn,
  }
}

export function sloFormToInput(f: SLOForm): SLOInput {
  return {
    target: f.target,
    window_days: f.window_days,
    fast_burn: f.fast_burn,
    slow_burn: f.slow_burn,
  }
}

/**
 * validateSLOForm mirrors the server's rules: 0 < target < 100 with at most 3
 * decimals, a whole window of 7 to 90 days, and 1 <= slow <= fast <= 1000.
 * Errors are keyed "slo.<field>"; a disabled SLO is never invalid.
 */
export function validateSLOForm(f: SLOForm): Record<string, string> {
  const e: Record<string, string> = {}
  if (!f.enabled) return e
  if (!(f.target > 0 && f.target < 100)) e['slo.target'] = 'Must be above 0 and below 100.'
  else if (Math.abs(f.target * 1000 - Math.round(f.target * 1000)) > 1e-6) e['slo.target'] = 'At most 3 decimals.'
  if (!Number.isInteger(f.window_days) || f.window_days < 7 || f.window_days > 90)
    e['slo.window_days'] = 'A whole number of days from 7 to 90.'
  if (!(f.fast_burn >= 1 && f.fast_burn <= 1000)) e['slo.fast_burn'] = 'From 1 to 1000.'
  if (!(f.slow_burn >= 1 && f.slow_burn <= 1000)) e['slo.slow_burn'] = 'From 1 to 1000.'
  else if (f.fast_burn >= 1 && f.slow_burn > f.fast_burn) e['slo.slow_burn'] = 'Must not be above the fast threshold.'
  return e
}

/** MonitorFormPage handles both monitor creation and editing. */
export function MonitorFormPage() {
  const t = useTeamId()
  const editingId = useNumberParam('monitorId')
  const isEdit = !Number.isNaN(editingId)
  const monitorQuery = useMonitor(t, editingId, { enabled: isEdit })
  const sloQuery = useSLO(t, editingId, { enabled: isEdit })

  if (!isEdit) return <MonitorEditor key="new" initial={defaultMonitorForm('http')} initialSLO={defaultSLOForm()} />
  if (monitorQuery.isPending || sloQuery.isPending) return <Loading />
  if (monitorQuery.error) return <ErrorMessage error={monitorQuery.error} />
  if (sloQuery.error) return <ErrorMessage error={sloQuery.error} />
  // Keyed by id so navigating between monitors starts from fresh state;
  // later cache updates (live events) don't overwrite the user's edits.
  return (
    <MonitorEditor
      key={editingId}
      editingId={editingId}
      initial={monitorToForm(monitorQuery.data)}
      initialSLO={sloToForm(sloQuery.data?.slo)}
    />
  )
}

function MonitorEditor({
  initial,
  initialSLO,
  editingId,
}: {
  initial: MonitorForm
  initialSLO: SLOForm
  editingId?: number
}) {
  const t = useTeamId()
  const navigate = useNavigate()
  const { can } = useRole(t)
  const isEdit = editingId !== undefined
  const monitors = useMonitors(t)
  const channels = useChannels(t)
  const policies = usePolicies(t)
  const save = useSaveMonitor(t)
  const saveSLO = useSaveSLO(t)
  const deleteSLO = useDeleteSLO(t)

  const [form, setForm] = useState<MonitorForm>(initial)
  const [slo, setSLO] = useState<SLOForm>(initialSLO)
  const [errors, setErrors] = useState<Record<string, string>>({})
  // savedId is the monitor created by an earlier submit whose SLO step
  // failed: the next submit updates it instead of creating another.
  const [savedId, setSavedId] = useState<number | undefined>(editingId)

  const parentOptions = [
    noneOption,
    ...(monitors.data ?? []).filter((m) => m.id !== editingId).map((m) => ({ value: String(m.id), label: m.name })),
  ]
  const policyOptions = [
    noneOption,
    ...(policies.data ?? []).map((p) => ({
      value: String(p.id),
      label: p.name,
    })),
  ]
  const channelOptions = (channels.data ?? []).map((c) => ({
    value: c.id,
    label: `${c.name} (${c.type})`,
  }))

  const set = <K extends keyof MonitorForm>(key: K, value: MonitorForm[K]) => setForm((f) => ({ ...f, [key]: value }))

  const changeType = (type: MonitorType) => {
    setForm((f) => ({
      ...defaultMonitorForm(type),
      name: f.name,
      group_name: f.group_name,
      tags: f.tags,
      interval_s: f.interval_s,
      retry_interval_s: f.retry_interval_s,
      retries: f.retries,
      timeout_s: f.timeout_s,
      paused: f.paused,
      parentId: f.parentId,
      policyId: f.policyId,
      channelIds: f.channelIds,
      // Remote regions only apply to types that can run on a probe; others fall back to local.
      ...(canRunRemotely(type) ? { regions: f.regions, quorum: f.quorum } : {}),
    }))
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const localErrors = {
      ...validateMonitorForm(form),
      ...validateSLOForm(slo),
    }
    setErrors(localErrors)
    if (Object.keys(localErrors).length > 0) return
    saveSLO.reset()
    deleteSLO.reset()
    save.mutate(
      { id: savedId, body: formToBody(form) },
      {
        onSuccess: async (m) => {
          setSavedId(m.id)
          try {
            // The SLO is saved after its monitor (a new monitor has no id before).
            if (slo.enabled)
              await saveSLO.mutateAsync({
                id: m.id,
                body: sloFormToInput(slo),
              })
            else if (initialSLO.enabled) await deleteSLO.mutateAsync(m.id)
          } catch {
            return // shown below; the monitor itself is saved
          }
          navigate(`/t/${t}/monitors/${m.id}`)
        },
      },
    )
  }

  const err = (name: string) => fieldError(save.error, name, errors)
  const sloErr = (name: string) => fieldError(saveSLO.error, name, errors) ?? errors[`slo.${name}`]
  const busy = save.isPending || saveSLO.isPending || deleteSLO.isPending

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title={isEdit ? `Edit ${form.name || 'monitor'}` : 'Add monitor'} />
      <form onSubmit={submit} className="space-y-6">
        <ErrorMessage error={save.error} />

        <Module title="Monitor">
          {!isEdit && (
            <Select
              label="Type"
              value={form.type}
              onChange={(v) => changeType(v as MonitorType)}
              options={monitorTypes.map((mt) => ({
                value: mt.value,
                label: mt.label,
              }))}
            />
          )}

          <TextField label="Name" value={form.name} onChange={(v) => set('name', v)} required error={err('name')} />
          <TextField
            label="Group"
            value={form.group_name}
            onChange={(v) => set('group_name', v)}
            placeholder="Ungrouped"
            help="Monitors with the same group are shown together on the list."
          />
          <TagInput label="Tags" value={form.tags} onChange={(v) => set('tags', v)} />
        </Module>

        <Module title="Schedule">
          <NumberField
            label="Check interval (s)"
            value={form.interval_s}
            onChange={(v) => set('interval_s', v)}
            min={20}
            error={err('interval_s')}
          />
          <NumberField
            label="Retry interval (s)"
            value={form.retry_interval_s}
            onChange={(v) => set('retry_interval_s', v)}
            min={20}
            error={err('retry_interval_s')}
          />
          <NumberField
            label="Retries before down"
            value={form.retries}
            onChange={(v) => set('retries', v)}
            min={0}
            max={10}
            error={err('retries')}
          />
          <NumberField
            label="Timeout (s)"
            value={form.timeout_s}
            onChange={(v) => set('timeout_s', v)}
            min={1}
            error={err('timeout_s')}
          />
        </Module>

        <TypeFields form={form} set={set} err={err} />

        <RegionFields form={form} setForm={setForm} err={err} />

        <Module title="Alerting">
          <Select
            label="Parent monitor"
            value={form.parentId}
            onChange={(v) => set('parentId', v)}
            options={parentOptions}
            help="Skip checks while the parent is down."
            error={err('parent_id')}
          />
          <Select
            label="Escalation policy"
            value={form.policyId}
            onChange={(v) => set('policyId', v)}
            options={policyOptions}
            error={err('escalation_policy_id')}
          />
          <div className={wide}>
            <MultiCheck
              label="Channels"
              value={form.channelIds}
              onChange={(v) => set('channelIds', v)}
              options={channelOptions}
              error={err('channel_ids')}
              help="Used when no escalation policy is set; none = team default channels"
              empty="No channels yet."
            />
          </div>
          <div className={wide}>
            <Checkbox label="Paused" checked={form.paused} onChange={(v) => set('paused', v)} />
          </div>
        </Module>

        <SLOFields slo={slo} setSLO={setSLO} err={sloErr} readOnly={!can('editor')} />
        {(saveSLO.error || deleteSLO.error) && (
          <p role="alert" className="rounded-md border border-down/50 bg-down/10 px-3 py-2 text-sm text-down-ink">
            The monitor was saved, but its service level objective was not:{' '}
            {errorText(saveSLO.error ?? deleteSLO.error)}
          </p>
        )}

        <div className="sticky bottom-0 z-10 flex justify-end gap-2 border-t border-line bg-bg py-3">
          <button type="button" className="btn" onClick={() => navigate(-1)}>
            Cancel
          </button>
          <button type="submit" className="btn-primary" disabled={busy}>
            {isEdit ? 'Save' : 'Create'}
          </button>
        </div>
      </form>
    </div>
  )
}

function TypeFields({
  form,
  set,
  err,
}: {
  form: MonitorForm
  set: <K extends keyof MonitorForm>(key: K, value: MonitorForm[K]) => void
  err: (name: string) => string | undefined
}) {
  switch (form.type) {
    case 'http':
    case 'keyword':
      return (
        <Module title={form.type === 'keyword' ? 'Keyword check' : 'HTTP request'}>
          <TextField
            className={`${wide} [&_input]:font-mono`}
            label="URL"
            type="url"
            value={form.http.url}
            onChange={(v) => set('http', { ...form.http, url: v })}
            placeholder="https://example.com"
            required
            error={err('config.url')}
          />
          <Select
            label="Method"
            value={form.http.method}
            onChange={(v) => set('http', { ...form.http, method: v })}
            options={httpMethods.map((m) => ({ value: m, label: m }))}
          />
          <div className={wide}>
            <KeyValueEditor
              label="Headers"
              value={form.http.headers}
              onChange={(v) => set('http', { ...form.http, headers: v })}
            />
          </div>
          {methodsWithBody.includes(form.http.method) && (
            <TextField
              className={wide}
              label="Body"
              value={form.http.body}
              onChange={(v) => set('http', { ...form.http, body: v })}
              multiline
            />
          )}
          <TextField
            label="Accepted status codes"
            value={form.http.accepted_status}
            onChange={(v) => set('http', { ...form.http, accepted_status: v })}
            help="Comma separated, e.g. 200-299, 301"
            error={err('config.accepted_status')}
          />
          <NumberField
            label="Max redirects"
            value={form.http.max_redirects}
            onChange={(v) => set('http', { ...form.http, max_redirects: v })}
            min={0}
          />
          <div className={wide}>
            <Checkbox
              label="Ignore TLS errors"
              checked={form.http.ignore_tls_errors}
              onChange={(v) => set('http', { ...form.http, ignore_tls_errors: v })}
            />
          </div>
          {form.type === 'keyword' && (
            <>
              <TextField
                label="Keyword"
                value={form.keyword.keyword}
                onChange={(v) => set('keyword', { ...form.keyword, keyword: v })}
                required
                error={err('config.keyword')}
              />
              <div className={`${wide} space-y-2`}>
                <Checkbox
                  label="Invert (alert when the keyword is present)"
                  checked={form.keyword.invert_keyword}
                  onChange={(v) => set('keyword', { ...form.keyword, invert_keyword: v })}
                />
                <Checkbox
                  label="Case sensitive"
                  checked={form.keyword.case_sensitive}
                  onChange={(v) => set('keyword', { ...form.keyword, case_sensitive: v })}
                />
              </div>
            </>
          )}
        </Module>
      )
    case 'tcp':
      return (
        <Module title="TCP connection">
          <TextField
            className="[&_input]:font-mono"
            label="Host"
            value={form.tcp.host}
            onChange={(v) => set('tcp', { ...form.tcp, host: v })}
            required
            error={err('config.host')}
          />
          <TextField
            label="Port"
            value={form.tcp.port}
            onChange={(v) => set('tcp', { ...form.tcp, port: v })}
            required
            error={err('config.port')}
          />
        </Module>
      )
    case 'ping':
      return (
        <Module title="Ping">
          <TextField
            className="[&_input]:font-mono"
            label="Host"
            value={form.ping.host}
            onChange={(v) => set('ping', { ...form.ping, host: v })}
            required
            error={err('config.host')}
          />
          <NumberField
            label="Packet count"
            value={form.ping.count}
            onChange={(v) => set('ping', { ...form.ping, count: v })}
            min={1}
            max={10}
            error={err('config.count')}
          />
        </Module>
      )
    case 'dns':
      return (
        <Module title="DNS lookup">
          <TextField
            className="[&_input]:font-mono"
            label="Hostname"
            value={form.dns.hostname}
            onChange={(v) => set('dns', { ...form.dns, hostname: v })}
            required
            error={err('config.hostname')}
          />
          <Select
            label="Record type"
            value={form.dns.record_type}
            onChange={(v) => set('dns', { ...form.dns, record_type: v })}
            options={dnsRecordTypes.map((r) => ({ value: r, label: r }))}
          />
          <TextField
            className="[&_input]:font-mono"
            label="Resolver"
            value={form.dns.resolver}
            onChange={(v) => set('dns', { ...form.dns, resolver: v })}
            placeholder="Default resolver"
            help="Optional; host:port of a specific DNS resolver."
            error={err('config.resolver')}
          />
          <TextField
            className="[&_input]:font-mono"
            label="Expected value"
            value={form.dns.expected}
            onChange={(v) => set('dns', { ...form.dns, expected: v })}
            placeholder="Any"
            help="Optional; alert unless a record matches this value."
            error={err('config.expected')}
          />
        </Module>
      )
    case 'docker':
      return <DockerFields form={form} set={set} err={err} />
    case 'push':
      return (
        <Module title="Push">
          <p className={`${wide} text-sm text-muted`}>
            A push URL is issued once the monitor is created. Send a request to it on your own schedule to report
            liveness.
          </p>
        </Module>
      )
  }
}

/** DockerFields picks a Docker host, then a container from that host's list (or types a name). */
function DockerFields({
  form,
  set,
  err,
}: {
  form: MonitorForm
  set: <K extends keyof MonitorForm>(key: K, value: MonitorForm[K]) => void
  err: (name: string) => string | undefined
}) {
  const t = useTeamId()
  const listId = useId()
  const hosts = useDockerHosts(t)
  const hostId = form.docker.hostId === '' ? undefined : Number(form.docker.hostId)
  const containers = useDockerContainers(t, hostId)
  const hostOptions = [
    {
      value: '',
      label: hosts.data && hosts.data.length === 0 ? 'No Docker hosts yet' : 'Choose a host…',
    },
    ...(hosts.data ?? []).map((h) => ({
      value: String(h.id),
      label: `${h.name} (${h.endpoint})`,
    })),
  ]
  const hostError = err('config.docker_host_id')
  const containerError = err('config.container')
  const running = new Set((containers.data ?? []).filter((c) => c.state === 'running').map((c) => c.name))

  return (
    <Module title="Docker container">
      <Select
        label="Docker host"
        value={form.docker.hostId}
        onChange={(v) =>
          set('docker', {
            hostId: v,
            container: v === form.docker.hostId ? form.docker.container : '',
          })
        }
        options={hostOptions}
        error={hostError}
        help={
          <>
            Hosts are managed on the{' '}
            <Link to={`/t/${t}/docker-hosts`} className="link">
              Docker hosts
            </Link>{' '}
            page.
          </>
        }
      />
      <div>
        <label htmlFor={`${listId}-input`} className="mb-1 block text-sm font-medium">
          Container
        </label>
        <input
          id={`${listId}-input`}
          className="input font-mono"
          list={listId}
          value={form.docker.container}
          placeholder={hostId === undefined ? 'Choose a host first' : 'Search or type a container name'}
          disabled={hostId === undefined}
          aria-invalid={containerError ? true : undefined}
          onChange={(e) => set('docker', { ...form.docker, container: e.target.value })}
        />
        <datalist id={listId}>
          {(containers.data ?? []).map((c) => (
            <option key={c.id} value={c.name}>
              {c.image} · {c.status}
            </option>
          ))}
        </datalist>
        {containerError && <p className="mt-1 text-xs text-down-ink">{containerError}</p>}
        {!containerError && containers.error && (
          <p className="mt-1 text-xs text-muted">
            Could not list containers from this host; you can still type a container name or id.
          </p>
        )}
        {!containerError && containers.data && form.docker.container !== '' && !running.has(form.docker.container) && (
          <p className="mt-1 text-xs text-muted">
            {containers.data.some((c) => c.name === form.docker.container)
              ? 'This container is not running right now.'
              : 'No container with this name was found on the host.'}
          </p>
        )}
      </div>
      <p className={`${wide} text-xs text-muted`}>
        UP while the container runs (and its healthcheck, if it has one, is healthy). Alerts say why: for example
        &quot;exited (137)&quot; or &quot;unhealthy&quot;.
      </p>
    </Module>
  )
}

/** RegionFields picks the regions a monitor runs in and how many must agree it is down. */
function RegionFields({
  form,
  setForm,
  err,
}: {
  form: MonitorForm
  setForm: (update: (f: MonitorForm) => MonitorForm) => void
  err: (name: string) => string | undefined
}) {
  const regions = useRegions()

  if (!canRunRemotely(form.type)) {
    return (
      <section className="card p-4">
        <h2 className="text-base font-semibold text-balance">Regions</h2>
        <p className="mt-2 text-xs text-muted">
          {form.type === 'push'
            ? 'Push monitors are reported by your own jobs, so they are not checked from probe regions.'
            : 'Docker monitors read the container state from the Docker host, so they always run on this server.'}
        </p>
      </section>
    )
  }

  const known = regions.data ?? []
  // Keep a selected region visible even if it is no longer listed (for example a deleted probe).
  const options = [
    ...known,
    ...form.regions.filter((r) => !known.some((k) => k.name === r)).map((name) => ({ name, online: false })),
  ]
  const toggle = (name: string, on: boolean) =>
    setForm((f) => setRegions(f, on ? [...f.regions, name] : f.regions.filter((r) => r !== name)))
  const n = form.regions.length

  return (
    <section className="card space-y-4 p-4">
      <fieldset>
        <legend className="mb-3 block text-base font-semibold">Regions</legend>
        {regions.isPending ? (
          <p className="text-sm text-muted">Loading regions…</p>
        ) : regions.error ? (
          <ErrorMessage error={regions.error} />
        ) : (
          <div className="grid gap-2 sm:grid-cols-2">
            {options.map((r) => (
              <Checkbox
                key={r.name}
                label={
                  <>
                    {r.name}
                    {r.name === 'local' && <span className="text-muted"> (this server)</span>}
                    {!r.online && <span className="text-muted"> (offline)</span>}
                  </>
                }
                checked={form.regions.includes(r.name)}
                onChange={(on) => toggle(r.name, on)}
              />
            ))}
          </div>
        )}
        {err('regions') ? (
          <p className="mt-1 text-xs text-down-ink">{err('regions')}</p>
        ) : (
          <p className="mt-1 text-xs text-muted">
            The monitor is checked from every selected region. Probes are added by an instance admin.
          </p>
        )}
      </fieldset>
      {n > 1 && (
        <NumberField
          label="Quorum"
          value={form.quorum}
          onChange={(v) => setForm((f) => ({ ...f, quorum: v }))}
          min={1}
          max={n}
          error={err('quorum')}
          help={
            <>
              Down when at least {Number.isNaN(form.quorum) ? defaultQuorum(n) : clampQuorum(form.quorum, n)} of {n}{' '}
              regions report down (majority is {defaultQuorum(n)}). A region that stops reporting never counts as down.
            </>
          }
        />
      )}
    </section>
  )
}

/**
 * SLOFields edits the monitor's service level objective: on/off, target and
 * window, and the burn-rate thresholds under "Advanced". Read-only for viewers.
 */
function SLOFields({
  slo,
  setSLO,
  err,
  readOnly,
}: {
  slo: SLOForm
  setSLO: (update: (f: SLOForm) => SLOForm) => void
  err: (name: string) => string | undefined
  readOnly: boolean
}) {
  const set = <K extends keyof SLOForm>(key: K, value: SLOForm[K]) => setSLO((f) => ({ ...f, [key]: value }))
  return (
    <fieldset className="card space-y-4 p-4" disabled={readOnly}>
      <legend className="sr-only">Service level objective</legend>
      <div className="text-base font-semibold" aria-hidden="true">
        Service level objective
      </div>
      <Checkbox
        label="Track an SLO for this monitor"
        checked={slo.enabled}
        onChange={(v) => set('enabled', v)}
        help="Counts up, pending and degraded checks as good, and notifies when the error budget burns too fast. It never opens an alert."
      />
      {slo.enabled && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <NumberField
              label="Target (%)"
              value={slo.target}
              onChange={(v) => set('target', v)}
              min={0}
              max={100}
              step="any"
              error={err('target')}
              help="e.g. 99.9; at most 3 decimals."
            />
            <NumberField
              label="Window (days)"
              value={slo.window_days}
              onChange={(v) => set('window_days', v)}
              min={7}
              max={90}
              error={err('window_days')}
            />
          </div>
          <details className="rounded-md border border-line px-3 py-2">
            <summary className="cursor-pointer text-sm font-medium">Advanced</summary>
            <div className="mt-3 grid gap-4 sm:grid-cols-2">
              <NumberField
                label="Fast burn threshold"
                value={slo.fast_burn}
                onChange={(v) => set('fast_burn', v)}
                min={1}
                max={1000}
                step="any"
                error={err('fast_burn')}
                help="Over the last 1h and 5m."
              />
              <NumberField
                label="Slow burn threshold"
                value={slo.slow_burn}
                onChange={(v) => set('slow_burn', v)}
                min={1}
                max={1000}
                step="any"
                error={err('slow_burn')}
                help="Over the last 6h and 30m; at most the fast threshold."
              />
            </div>
          </details>
        </>
      )}
    </fieldset>
  )
}
