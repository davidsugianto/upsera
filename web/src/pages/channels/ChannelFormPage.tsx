import { useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import { Link, useNavigate } from 'react-router'
import { useChannels, useSaveChannel } from '../../api/queries'
import type { Channel, ChannelType } from '../../api/types'
import { Checkbox, KeyValueEditor, NumberField, Select, TagInput, TextField } from '../../components/Fields'
import { ErrorMessage, Loading, PageHeader, fieldError } from '../../components/ui'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'
import {
  channelFields,
  channelTypes,
  configToForm,
  formToConfig,
  type ChannelForm,
  type FieldSpec,
} from '../../lib/channelForm'

/** ChannelFormPage creates or edits a notification channel. */
export function ChannelFormPage() {
  const t = useTeamId()
  const channelId = useNumberParam('channelId')
  const editing = !Number.isNaN(channelId)
  const channels = useChannels(t)
  const existing = editing ? channels.data?.find((c) => c.id === channelId) : undefined

  if (editing && channels.isPending) return <Loading />
  if (editing && channels.error) return <ErrorMessage error={channels.error} />
  if (editing && !existing) {
    return (
      <div>
        <PageHeader title="Channel not found" />
        <Link to={`/t/${t}/channels`} className="link">
          Back to channels
        </Link>
      </div>
    )
  }
  return <ChannelEditor key={channelId} team={t} channelId={editing ? channelId : undefined} initial={existing} />
}

function ChannelEditor({ team, channelId, initial }: { team: number; channelId?: number; initial?: Channel }) {
  const navigate = useNavigate()
  const save = useSaveChannel(team)
  const editing = channelId !== undefined
  const [type, setType] = useState<ChannelType>(initial?.type ?? 'telegram')
  const [name, setName] = useState(initial?.name ?? '')
  const [isDefault, setIsDefault] = useState(initial?.is_default ?? false)
  const [form, setForm] = useState<ChannelForm>(() => configToForm(initial?.type ?? 'telegram', initial?.config))

  const setValue = (fieldName: string, v: string) => setForm((f) => ({ ...f, values: { ...f.values, [fieldName]: v } }))
  const setList = (fieldName: string, v: string[]) => setForm((f) => ({ ...f, lists: { ...f.lists, [fieldName]: v } }))

  const submit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate(
      { id: channelId, body: { type, name: name.trim(), is_default: isDefault, config: formToConfig(type, form) } },
      { onSuccess: () => navigate(`/t/${team}/channels`) },
    )
  }

  return (
    <div className="max-w-xl">
      <PageHeader title={editing ? 'Edit channel' : 'Add channel'} />
      <form onSubmit={submit} className="card space-y-4 p-5">
        <ErrorMessage error={save.error} />
        {editing ? (
          <p className="text-sm text-muted">Type: {channelTypes.find((c) => c.value === type)?.label ?? type}</p>
        ) : (
          <Select
            label="Type"
            value={type}
            onChange={(v) => {
              const nt = v as ChannelType
              setType(nt)
              setForm(configToForm(nt, undefined))
            }}
            options={channelTypes.map((c) => ({ value: c.value, label: c.label }))}
          />
        )}
        <TextField
          label="Name"
          value={name}
          onChange={setName}
          required
          error={fieldError(save.error, 'name')}
        />
        <Checkbox
          label="Default channel"
          checked={isDefault}
          onChange={setIsDefault}
          help="Used when a monitor has no channels and no escalation policy."
        />
        {channelFields[type].map((spec) => (
          <ChannelField
            key={spec.name}
            spec={spec}
            editing={editing}
            form={form}
            setForm={setForm}
            setValue={setValue}
            setList={setList}
            error={fieldError(save.error, `config.${spec.name}`)}
          />
        ))}
        <div className="flex gap-2">
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {editing ? 'Save' : 'Create'}
          </button>
          <Link to={`/t/${team}/channels`} className="btn">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  )
}

function ChannelField({
  spec,
  editing,
  form,
  setForm,
  setValue,
  setList,
  error,
}: {
  spec: FieldSpec
  editing: boolean
  form: ChannelForm
  setForm: Dispatch<SetStateAction<ChannelForm>>
  setValue: (name: string, v: string) => void
  setList: (name: string, v: string[]) => void
  error?: string
}) {
  const secretHelp = spec.secret && editing ? 'Leave as ******** to keep the stored value' : spec.help

  switch (spec.kind) {
    case 'text':
    case 'password':
      return (
        <TextField
          label={spec.label}
          type={spec.kind === 'password' ? 'password' : 'text'}
          value={form.values[spec.name] ?? ''}
          onChange={(v) => setValue(spec.name, v)}
          required={spec.required}
          help={secretHelp}
          error={error}
        />
      )
    case 'number': {
      const raw = form.values[spec.name] ?? ''
      return (
        <NumberField
          label={spec.label}
          value={raw === '' ? NaN : Number(raw)}
          onChange={(v) => setValue(spec.name, Number.isNaN(v) ? '' : String(v))}
          error={error}
        />
      )
    }
    case 'select':
      return (
        <Select
          label={spec.label}
          value={form.values[spec.name] ?? ''}
          onChange={(v) => setValue(spec.name, v)}
          options={(spec.options ?? []).map((o) => ({ value: o, label: o }))}
        />
      )
    case 'list':
      return (
        <TagInput
          label={spec.label}
          value={form.lists[spec.name] ?? []}
          onChange={(v) => setList(spec.name, v)}
          help={secretHelp}
          error={error}
        />
      )
    case 'headers':
      if (form.headersMasked && !form.replaceHeaders) {
        return (
          <div>
            <p className="mb-1 text-sm font-medium">{spec.label}</p>
            <p className="text-sm text-muted">Headers are set (hidden)</p>
            <div className="mt-1.5">
              <Checkbox
                label="Replace headers"
                checked={form.replaceHeaders}
                onChange={(on) => setForm((f) => ({ ...f, replaceHeaders: on }))}
              />
            </div>
          </div>
        )
      }
      return (
        <KeyValueEditor
          label={spec.label}
          value={form.headers}
          onChange={(v) => setForm((f) => ({ ...f, headers: v }))}
          error={error}
        />
      )
  }
}
