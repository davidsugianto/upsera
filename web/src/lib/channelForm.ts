import type { ChannelType } from '../api/types'
import type { HeaderRow } from './monitorForm'

/** REDACTED is how the server shows stored secrets; sending it back keeps the stored value. */
export const REDACTED = '********'

export type FieldKind = 'text' | 'password' | 'number' | 'list' | 'select' | 'headers'

export type FieldSpec = {
  name: string
  label: string
  kind: FieldKind
  secret: boolean
  required: boolean
  default?: string
  options?: string[]
  help?: string
}

export const channelTypes: { value: ChannelType; label: string }[] = [
  { value: 'telegram', label: 'Telegram' },
  { value: 'discord', label: 'Discord' },
  { value: 'slack', label: 'Slack (webhook)' },
  { value: 'slack_app', label: 'Slack (app)' },
  { value: 'smtp', label: 'Email (SMTP)' },
  { value: 'webhook', label: 'Webhook' },
]

const f = (name: string, label: string, kind: FieldKind, opts: Partial<FieldSpec> = {}): FieldSpec => ({
  name,
  label,
  kind,
  secret: false,
  required: true,
  ...opts,
})

export const channelFields: Record<ChannelType, FieldSpec[]> = {
  telegram: [f('bot_token', 'Bot token', 'password', { secret: true }), f('chat_id', 'Chat ID', 'text')],
  discord: [f('webhook_url', 'Webhook URL', 'password', { secret: true })],
  slack: [f('webhook_url', 'Webhook URL', 'password', { secret: true })],
  slack_app: [
    f('bot_token', 'Bot token (xoxb-…)', 'password', { secret: true }),
    f('app_token', 'App token (xapp-…)', 'password', { secret: true }),
    f('channel', 'Channel', 'text'),
  ],
  smtp: [
    f('host', 'Host', 'text'),
    f('port', 'Port', 'number', { default: '587' }),
    f('username', 'Username', 'text', { required: false }),
    f('password', 'Password', 'password', { secret: true, required: false }),
    f('from', 'From', 'text'),
    f('to', 'To', 'list', { help: 'One or more recipient addresses' }),
    f('tls', 'TLS', 'select', { options: ['starttls', 'tls', 'none'], default: 'starttls' }),
  ],
  webhook: [
    f('url', 'URL', 'password', { secret: true }),
    f('headers', 'Headers', 'headers', { secret: true, required: false }),
  ],
}

export type ChannelForm = {
  /** text, password, number and select fields */
  values: Record<string, string>
  /** list fields */
  lists: Record<string, string[]>
  /** webhook headers */
  headers: HeaderRow[]
  /** the stored headers are redacted (edit mode) */
  headersMasked: boolean
  /** the user chose to replace redacted headers */
  replaceHeaders: boolean
}

export function configToForm(type: ChannelType, config: unknown): ChannelForm {
  const c = (config && typeof config === 'object' ? config : {}) as Record<string, unknown>
  const form: ChannelForm = { values: {}, lists: {}, headers: [], headersMasked: false, replaceHeaders: false }
  for (const spec of channelFields[type]) {
    const v = c[spec.name]
    switch (spec.kind) {
      case 'list':
        form.lists[spec.name] = Array.isArray(v) ? v.map(String) : []
        break
      case 'headers':
        if (v === REDACTED) form.headersMasked = true
        else if (v && typeof v === 'object')
          form.headers = Object.entries(v as Record<string, unknown>).map(([key, value]) => ({ key, value: String(value) }))
        break
      default:
        form.values[spec.name] = v === undefined || v === null ? (spec.default ?? '') : String(v)
    }
  }
  return form
}

export function formToConfig(type: ChannelType, form: ChannelForm): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const spec of channelFields[type]) {
    switch (spec.kind) {
      case 'list': {
        const l = (form.lists[spec.name] ?? []).map((s) => s.trim()).filter(Boolean)
        if (l.length > 0 || spec.required) out[spec.name] = l
        break
      }
      case 'headers': {
        if (form.headersMasked && !form.replaceHeaders) {
          out[spec.name] = REDACTED
          break
        }
        const h = Object.fromEntries(form.headers.filter((r) => r.key.trim() !== '').map((r) => [r.key.trim(), r.value]))
        // Replacing always sends an object: omitting the key would keep the stored headers.
        if (Object.keys(h).length > 0 || form.replaceHeaders) out[spec.name] = h
        break
      }
      case 'number': {
        const v = (form.values[spec.name] ?? '').trim()
        if (v !== '') out[spec.name] = Number(v)
        break
      }
      default: {
        const raw = form.values[spec.name] ?? ''
        const v = spec.secret ? raw : raw.trim()
        if (v !== '' || spec.required) out[spec.name] = v
      }
    }
  }
  return out
}
