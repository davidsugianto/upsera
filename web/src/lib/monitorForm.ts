import type { Monitor, MonitorRequest, MonitorType } from '../api/types'

export type HeaderRow = { key: string; value: string }

/** MonitorForm is the editable state of a monitor, with one config sub-state per type. */
export type MonitorForm = {
  type: MonitorType
  name: string
  group_name: string
  tags: string[]
  interval_s: number
  retry_interval_s: number
  retries: number
  timeout_s: number
  paused: boolean
  /** '' = none */
  parentId: string
  /** '' = none */
  policyId: string
  channelIds: number[]
  http: {
    url: string
    method: string
    headers: HeaderRow[]
    body: string
    /** comma separated, e.g. "200-299, 301" */
    accepted_status: string
    max_redirects: number
    ignore_tls_errors: boolean
  }
  keyword: { keyword: string; invert_keyword: boolean; case_sensitive: boolean }
  tcp: { host: string; port: string }
  ping: { host: string; count: number }
  dns: { hostname: string; record_type: string; resolver: string; expected: string }
}

export const monitorTypes: { value: MonitorType; label: string }[] = [
  { value: 'http', label: 'HTTP(S)' },
  { value: 'keyword', label: 'HTTP(S) keyword' },
  { value: 'tcp', label: 'TCP port' },
  { value: 'ping', label: 'Ping' },
  { value: 'dns', label: 'DNS' },
  { value: 'push', label: 'Push (heartbeat)' },
]

export const httpMethods = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']
export const methodsWithBody = ['POST', 'PUT', 'PATCH']
export const dnsRecordTypes = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS']

export function defaultMonitorForm(type: MonitorType): MonitorForm {
  return {
    type,
    name: '',
    group_name: '',
    tags: [],
    interval_s: 60,
    retry_interval_s: 20,
    retries: 1,
    timeout_s: 10,
    paused: false,
    parentId: '',
    policyId: '',
    channelIds: [],
    http: {
      url: '',
      method: 'GET',
      headers: [],
      body: '',
      accepted_status: '200-299',
      max_redirects: 10,
      ignore_tls_errors: false,
    },
    keyword: { keyword: '', invert_keyword: false, case_sensitive: false },
    tcp: { host: '', port: '' },
    ping: { host: '', count: 3 },
    dns: { hostname: '', record_type: 'A', resolver: '', expected: '' },
  }
}

type Obj = Record<string, unknown>
const str = (v: unknown, d = '') => (typeof v === 'string' ? v : d)
const num = (v: unknown, d: number) => (typeof v === 'number' ? v : d)
const bool = (v: unknown) => v === true

export function monitorToForm(m: Monitor): MonitorForm {
  const f = defaultMonitorForm(m.type)
  const c = (m.config && typeof m.config === 'object' ? m.config : {}) as Obj
  f.name = m.name
  f.group_name = m.group_name
  f.tags = m.tags ?? []
  f.interval_s = m.interval_s
  f.retry_interval_s = m.retry_interval_s
  f.retries = m.retries
  f.timeout_s = m.timeout_s
  f.paused = m.paused
  f.parentId = m.parent_id === null ? '' : String(m.parent_id)
  f.policyId = m.escalation_policy_id === null ? '' : String(m.escalation_policy_id)
  f.channelIds = m.channel_ids ?? []
  switch (m.type) {
    case 'http':
    case 'keyword': {
      const headers = (c.headers && typeof c.headers === 'object' ? c.headers : {}) as Record<string, string>
      f.http = {
        url: str(c.url),
        method: str(c.method, 'GET') || 'GET',
        headers: Object.entries(headers).map(([key, value]) => ({ key, value: String(value) })),
        body: str(c.body),
        accepted_status: Array.isArray(c.accepted_status) ? c.accepted_status.join(', ') : '200-299',
        max_redirects: num(c.max_redirects, 10),
        ignore_tls_errors: bool(c.ignore_tls_errors),
      }
      f.keyword = {
        keyword: str(c.keyword),
        invert_keyword: bool(c.invert_keyword),
        case_sensitive: bool(c.case_sensitive),
      }
      break
    }
    case 'tcp':
      f.tcp = { host: str(c.host), port: typeof c.port === 'number' ? String(c.port) : '' }
      break
    case 'ping':
      f.ping = { host: str(c.host), count: num(c.count, 3) }
      break
    case 'dns':
      f.dns = {
        hostname: str(c.hostname),
        record_type: str(c.record_type, 'A') || 'A',
        resolver: str(c.resolver),
        expected: str(c.expected),
      }
      break
    case 'push':
      break
  }
  return f
}

/** configFor returns only the config keys valid for f.type: the server rejects unknown fields. */
export function configFor(f: MonitorForm): Obj {
  switch (f.type) {
    case 'http':
    case 'keyword': {
      const h = f.http
      const c: Obj = { url: h.url.trim(), method: h.method }
      const headers = Object.fromEntries(
        h.headers.filter((r) => r.key.trim() !== '').map((r) => [r.key.trim(), r.value]),
      )
      if (Object.keys(headers).length > 0) c.headers = headers
      if (h.body !== '' && methodsWithBody.includes(h.method)) c.body = h.body
      c.accepted_status = h.accepted_status
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      c.max_redirects = h.max_redirects
      c.ignore_tls_errors = h.ignore_tls_errors
      if (f.type === 'keyword') {
        c.keyword = f.keyword.keyword
        c.invert_keyword = f.keyword.invert_keyword
        c.case_sensitive = f.keyword.case_sensitive
      }
      return c
    }
    case 'tcp':
      return { host: f.tcp.host.trim(), port: Number(f.tcp.port) }
    case 'ping':
      return { host: f.ping.host.trim(), count: f.ping.count }
    case 'dns': {
      const c: Obj = { hostname: f.dns.hostname.trim(), record_type: f.dns.record_type }
      if (f.dns.resolver.trim() !== '') c.resolver = f.dns.resolver.trim()
      if (f.dns.expected.trim() !== '') c.expected = f.dns.expected.trim()
      return c
    }
    case 'push':
      return {}
  }
}

export function formToBody(f: MonitorForm): MonitorRequest {
  const body: MonitorRequest = {
    type: f.type,
    name: f.name.trim(),
    group_name: f.group_name.trim(),
    tags: f.tags,
    interval_s: f.interval_s,
    retry_interval_s: f.retry_interval_s,
    retries: f.retries,
    timeout_s: f.timeout_s,
    paused: f.paused,
    channel_ids: f.channelIds,
    config: configFor(f),
  }
  if (f.parentId !== '') body.parent_id = Number(f.parentId)
  if (f.policyId !== '') body.escalation_policy_id = Number(f.policyId)
  return body
}

/** validateMonitorForm mirrors the server's checks; it returns field errors keyed like the server's. */
export function validateMonitorForm(f: MonitorForm): Record<string, string> {
  const e: Record<string, string> = {}
  if (f.name.trim() === '') e.name = 'Name is required'
  if (!(f.interval_s >= 20)) e.interval_s = 'Interval must be at least 20 seconds'
  if (!(f.retry_interval_s >= 20)) e.retry_interval_s = 'Retry interval must be at least 20 seconds'
  if (!(f.timeout_s >= 1) || f.timeout_s >= f.interval_s) e.timeout_s = 'Timeout must be less than the interval'
  if (!(f.retries >= 0 && f.retries <= 10)) e.retries = 'Retries must be between 0 and 10'
  return e
}

/** targetSummary is a one-line description of what a monitor checks. */
export function targetSummary(m: Pick<Monitor, 'type' | 'config'>): string {
  const c = (m.config && typeof m.config === 'object' ? m.config : {}) as Obj
  switch (m.type) {
    case 'http':
    case 'keyword':
      return str(c.url)
    case 'tcp':
      return `${str(c.host)}:${num(c.port, 0)}`
    case 'ping':
      return str(c.host)
    case 'dns':
      return `${str(c.hostname)} ${str(c.record_type)}`.trim()
    case 'push':
      return 'push'
  }
}
