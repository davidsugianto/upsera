import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router'
import { useChannels, useMonitor, useMonitors, usePolicies, useSaveMonitor } from '../../api/queries'
import type { MonitorType } from '../../api/types'
import { Checkbox, KeyValueEditor, MultiCheck, NumberField, Select, TagInput, TextField } from '../../components/Fields'
import { ErrorMessage, Loading, PageHeader, fieldError } from '../../components/ui'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import {
  defaultMonitorForm,
  dnsRecordTypes,
  formToBody,
  httpMethods,
  methodsWithBody,
  monitorToForm,
  monitorTypes,
  validateMonitorForm,
  type MonitorForm,
} from '../../lib/monitorForm'

const noneOption = { value: '', label: 'None' }

/** MonitorFormPage handles both monitor creation and editing. */
export function MonitorFormPage() {
  const t = useTeamId()
  const editingId = useNumberParam('monitorId')
  const isEdit = !Number.isNaN(editingId)
  const monitorQuery = useMonitor(t, editingId, { enabled: isEdit })

  if (!isEdit) return <MonitorEditor key="new" initial={defaultMonitorForm('http')} />
  if (monitorQuery.isPending) return <Loading />
  if (monitorQuery.error) return <ErrorMessage error={monitorQuery.error} />
  // Keyed by id so navigating between monitors starts from fresh state;
  // later cache updates (live events) don't overwrite the user's edits.
  return <MonitorEditor key={editingId} editingId={editingId} initial={monitorToForm(monitorQuery.data)} />
}

function MonitorEditor({ initial, editingId }: { initial: MonitorForm; editingId?: number }) {
  const t = useTeamId()
  const navigate = useNavigate()
  const isEdit = editingId !== undefined
  const monitors = useMonitors(t)
  const channels = useChannels(t)
  const policies = usePolicies(t)
  const save = useSaveMonitor(t)

  const [form, setForm] = useState<MonitorForm>(initial)
  const [errors, setErrors] = useState<Record<string, string>>({})

  const parentOptions = [
    noneOption,
    ...(monitors.data ?? []).filter((m) => m.id !== editingId).map((m) => ({ value: String(m.id), label: m.name })),
  ]
  const policyOptions = [noneOption, ...(policies.data ?? []).map((p) => ({ value: String(p.id), label: p.name }))]
  const channelOptions = (channels.data ?? []).map((c) => ({ value: c.id, label: `${c.name} (${c.type})` }))

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
    }))
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const localErrors = validateMonitorForm(form)
    setErrors(localErrors)
    if (Object.keys(localErrors).length > 0) return
    save.mutate(
      { id: isEdit ? editingId : undefined, body: formToBody(form) },
      { onSuccess: (m) => navigate(`/t/${t}/monitors/${m.id}`) },
    )
  }

  const err = (name: string) => fieldError(save.error, name, errors)

  return (
    <div className="mx-auto max-w-2xl">
      <PageHeader title={isEdit ? `Edit ${form.name || 'monitor'}` : 'Add monitor'} />
      <form onSubmit={submit} className="card space-y-5 p-5">
        <ErrorMessage error={save.error} />

        {!isEdit && (
          <Select
            label="Type"
            value={form.type}
            onChange={(v) => changeType(v as MonitorType)}
            options={monitorTypes.map((mt) => ({ value: mt.value, label: mt.label }))}
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

        <div className="grid grid-cols-2 gap-4">
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
        </div>

        <TypeFields form={form} set={set} err={err} />

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
        <MultiCheck
          label="Channels"
          value={form.channelIds}
          onChange={(v) => set('channelIds', v)}
          options={channelOptions}
          error={err('channel_ids')}
          help="Used when no escalation policy is set; none = team default channels"
          empty="No channels yet."
        />

        <Checkbox label="Paused" checked={form.paused} onChange={(v) => set('paused', v)} />

        <div className="flex justify-end gap-2">
          <button type="button" className="btn" onClick={() => navigate(-1)}>
            Cancel
          </button>
          <button type="submit" className="btn-primary" disabled={save.isPending}>
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
        <div className="space-y-5 border-t border-line pt-5">
          <TextField
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
          <KeyValueEditor
            label="Headers"
            value={form.http.headers}
            onChange={(v) => set('http', { ...form.http, headers: v })}
          />
          {methodsWithBody.includes(form.http.method) && (
            <TextField
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
          <Checkbox
            label="Ignore TLS errors"
            checked={form.http.ignore_tls_errors}
            onChange={(v) => set('http', { ...form.http, ignore_tls_errors: v })}
          />
          {form.type === 'keyword' && (
            <>
              <TextField
                label="Keyword"
                value={form.keyword.keyword}
                onChange={(v) => set('keyword', { ...form.keyword, keyword: v })}
                required
                error={err('config.keyword')}
              />
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
            </>
          )}
        </div>
      )
    case 'tcp':
      return (
        <div className="grid grid-cols-2 gap-4 border-t border-line pt-5">
          <TextField
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
        </div>
      )
    case 'ping':
      return (
        <div className="grid grid-cols-2 gap-4 border-t border-line pt-5">
          <TextField
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
        </div>
      )
    case 'dns':
      return (
        <div className="space-y-5 border-t border-line pt-5">
          <TextField
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
            label="Resolver"
            value={form.dns.resolver}
            onChange={(v) => set('dns', { ...form.dns, resolver: v })}
            placeholder="Default resolver"
            help="Optional; host:port of a specific DNS resolver."
            error={err('config.resolver')}
          />
          <TextField
            label="Expected value"
            value={form.dns.expected}
            onChange={(v) => set('dns', { ...form.dns, expected: v })}
            placeholder="Any"
            help="Optional; alert unless a record matches this value."
            error={err('config.expected')}
          />
        </div>
      )
    case 'push':
      return (
        <p className="border-t border-line pt-5 text-sm text-muted">
          A push URL is issued once the monitor is created. Send a request to it on your own schedule to report
          liveness.
        </p>
      )
  }
}
