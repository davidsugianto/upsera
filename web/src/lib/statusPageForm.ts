import type {
  IncidentSeverity,
  IncidentStatus,
  Monitor,
  SmtpSettings,
  StatusPage,
  StatusPageItemRequest,
  StatusPageRequest,
  Theme,
} from '../api/types'

// Status page builder state <-> request body, validation that mirrors
// internal/api/status_pages.go, and the small bits of logic the incident,
// subscriber and SMTP forms share. Components stay thin; the rules live here.

export const themes: { value: Theme; label: string }[] = [
  { value: 'auto', label: 'Match the visitor’s system' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export const incidentStatuses: { value: IncidentStatus; label: string }[] = [
  { value: 'investigating', label: 'Investigating' },
  { value: 'identified', label: 'Identified' },
  { value: 'monitoring', label: 'Monitoring' },
  { value: 'resolved', label: 'Resolved' },
]

export const incidentSeverities: { value: IncidentSeverity; label: string }[] = [
  { value: 'minor', label: 'Minor' },
  { value: 'major', label: 'Major' },
  { value: 'critical', label: 'Critical' },
]

export const RESERVED_SLUGS: readonly string[] = [
  'api',
  'assets',
  'static',
  'admin',
  'status',
  'new',
  'healthz',
  'metrics',
]

const SLUG_RE = /^[a-z0-9][a-z0-9-]{1,62}$/
const DNS_LABEL_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

export const MAX_ITEMS = 100
export const MAX_TEXT = { title: 100, description: 500, logo: 500, name: 100, domain: 253, incidentTitle: 200, incidentBody: 2000 }

// ---- builder state ----

export type BuilderItem = { monitorId: number; displayName: string }
/** key is a client-only identity so renaming a section keeps its React state. */
export type BuilderSection = { key: string; name: string; items: BuilderItem[] }

export type StatusPageForm = {
  title: string
  slug: string
  description: string
  logoUrl: string
  theme: Theme
  customDomain: string
  published: boolean
  sections: BuilderSection[]
}

let sectionSeq = 0
/** newSection makes an empty section with a fresh key. */
export function newSection(name = '', items: BuilderItem[] = []): BuilderSection {
  sectionSeq += 1
  return { key: `section-${sectionSeq}`, name, items }
}

export function emptyStatusPageForm(): StatusPageForm {
  return {
    title: '',
    slug: '',
    description: '',
    logoUrl: '',
    theme: 'auto',
    customDomain: '',
    published: false,
    sections: [newSection()],
  }
}

/**
 * pageToForm loads a page into builder state. Items are grouped by *consecutive* equal section
 * names, so a page whose items alternate between sections round-trips with the same order.
 */
export function pageToForm(p: StatusPage): StatusPageForm {
  const sections: BuilderSection[] = []
  for (const it of p.items ?? []) {
    const last = sections[sections.length - 1]
    const item = { monitorId: it.monitor_id, displayName: it.display_name }
    if (last && last.name === it.section) last.items.push(item)
    else sections.push(newSection(it.section, [item]))
  }
  return {
    title: p.title,
    slug: p.slug,
    description: p.description,
    logoUrl: p.logo_url,
    theme: p.theme,
    customDomain: p.custom_domain ?? '',
    published: p.published,
    sections: sections.length > 0 ? sections : [newSection()],
  }
}

/**
 * formToBody flattens the builder into the request: item order is section order then item order.
 * Sections without monitors cannot be represented and are dropped. An empty display name is
 * omitted so the server defaults it to the monitor's name; an empty custom domain becomes null.
 */
export function formToBody(f: StatusPageForm): StatusPageRequest {
  const items: StatusPageItemRequest[] = []
  for (const s of f.sections) {
    const section = s.name.trim()
    for (const it of s.items) {
      const item: StatusPageItemRequest = { monitor_id: it.monitorId }
      if (section !== '') item.section = section
      const name = it.displayName.trim()
      if (name !== '') item.display_name = name
      items.push(item)
    }
  }
  const domain = normalizeDomain(f.customDomain)
  return {
    slug: f.slug.trim(),
    title: f.title.trim(),
    description: f.description.trim(),
    logo_url: f.logoUrl.trim(),
    theme: f.theme,
    custom_domain: domain === '' ? null : domain,
    published: f.published,
    items,
  }
}

// ---- validation (mirrors the server) ----

export function normalizeDomain(raw: string): string {
  return raw.trim().toLowerCase()
}

/** validateSlug returns '' when the slug is acceptable, else the reason. */
export function validateSlug(slug: string): string {
  const s = slug.trim()
  if (s === '') return 'Slug is required.'
  if (!SLUG_RE.test(s)) {
    return 'Use 2-63 characters: lower-case letters, digits and hyphens, not starting with a hyphen.'
  }
  if (RESERVED_SLUGS.includes(s)) return `"${s}" is reserved; pick another slug.`
  return ''
}

/** slugify suggests a slug from a title. */
export function slugify(title: string): string {
  return title
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/, '')
}

/** validateLogoUrl accepts '' or an https URL with a host and no credentials. */
export function validateLogoUrl(raw: string): string {
  const v = raw.trim()
  if (v === '') return ''
  if (v.length > MAX_TEXT.logo) return `Logo URL must be at most ${MAX_TEXT.logo} characters.`
  try {
    const u = new URL(v)
    if (u.protocol !== 'https:' || u.hostname === '' || u.username !== '' || u.password !== '') throw new Error()
  } catch {
    return 'Logo URL must be an https:// address.'
  }
  return ''
}

/**
 * validateCustomDomain checks a plain DNS host name. ownHost, when given, is this dashboard's own
 * host name, which the server refuses. '' means no custom domain.
 */
export function validateCustomDomain(raw: string, ownHost?: string): string {
  const v = normalizeDomain(raw)
  if (v === '') return ''
  if (v.length > MAX_TEXT.domain) return 'Custom domain is too long.'
  if (v.includes(':') || /^\d+(\.\d+){3}$/.test(v)) return 'Custom domain must be a host name, not an IP address.'
  const labels = v.split('.')
  if (labels.length < 2) return 'Use a fully qualified host name such as status.example.com.'
  if (!labels.every((l) => DNS_LABEL_RE.test(l))) {
    return 'Use a plain host name (letters, digits and hyphens; no scheme, port or path).'
  }
  if (/^\d+$/.test(labels.at(-1) ?? '')) return 'Custom domain must be a host name, not an IP address.'
  if (ownHost && ownHost.toLowerCase() === v) return 'Custom domain must differ from this server’s own host name.'
  return ''
}

export function flatItemCount(sections: BuilderSection[]): number {
  return sections.reduce((n, s) => n + s.items.length, 0)
}

/** validateStatusPageForm returns field errors keyed like the server's (`slug`, `custom_domain`, `items`, ...). */
export function validateStatusPageForm(f: StatusPageForm, ownHost?: string): Record<string, string> {
  const errors: Record<string, string> = {}
  const title = f.title.trim()
  if (title === '') errors.title = 'Title is required.'
  else if (title.length > MAX_TEXT.title) errors.title = `Title must be at most ${MAX_TEXT.title} characters.`
  const slug = validateSlug(f.slug)
  if (slug) errors.slug = slug
  if (f.description.trim().length > MAX_TEXT.description) {
    errors.description = `Description must be at most ${MAX_TEXT.description} characters.`
  }
  const logo = validateLogoUrl(f.logoUrl)
  if (logo) errors.logo_url = logo
  if (!themes.some((t) => t.value === f.theme)) errors.theme = 'Theme must be auto, light or dark.'
  const domain = validateCustomDomain(f.customDomain, ownHost)
  if (domain) errors.custom_domain = domain
  if (flatItemCount(f.sections) > MAX_ITEMS) errors.items = `A status page can list at most ${MAX_ITEMS} monitors.`
  let flat = 0
  f.sections.forEach((s, si) => {
    if (s.name.trim().length > MAX_TEXT.name) errors[`sections.${si}.name`] = `Section names are limited to ${MAX_TEXT.name} characters.`
    for (const it of s.items) {
      if (it.displayName.trim().length > MAX_TEXT.name) {
        errors[`items.${flat}.display_name`] = `Public names are limited to ${MAX_TEXT.name} characters.`
      }
      flat += 1
    }
  })
  return errors
}

/** publicUrl previews where the page will be served; '' until the slug (or domain) is usable. */
export function publicUrl(f: Pick<StatusPageForm, 'slug' | 'customDomain'>, origin: string): string {
  const domain = normalizeDomain(f.customDomain)
  if (domain !== '' && validateCustomDomain(domain) === '') return `https://${domain}`
  const slug = f.slug.trim()
  if (validateSlug(slug) !== '') return ''
  return `${origin.replace(/\/+$/, '')}/status/${slug}`
}

// ---- builder operations (all pure; the input is never mutated) ----

export function usedMonitorIds(sections: BuilderSection[]): Set<number> {
  return new Set(sections.flatMap((s) => s.items.map((i) => i.monitorId)))
}

/** availableMonitors are the team's monitors not on the page yet, by name. */
export function availableMonitors(monitors: Monitor[], sections: BuilderSection[]): Monitor[] {
  const used = usedMonitorIds(sections)
  return monitors.filter((m) => !used.has(m.id)).sort((a, b) => a.name.localeCompare(b.name))
}

const mapSection = (sections: BuilderSection[], key: string, fn: (s: BuilderSection) => BuilderSection) =>
  sections.map((s) => (s.key === key ? fn(s) : s))

export function addSection(sections: BuilderSection[], name = ''): BuilderSection[] {
  return [...sections, newSection(name)]
}

export function renameSection(sections: BuilderSection[], key: string, name: string): BuilderSection[] {
  return mapSection(sections, key, (s) => ({ ...s, name }))
}

/** removeSection drops a section and its monitors (they become available to add again). */
export function removeSection(sections: BuilderSection[], key: string): BuilderSection[] {
  return sections.filter((s) => s.key !== key)
}

export function moveSection(sections: BuilderSection[], key: string, dir: -1 | 1): BuilderSection[] {
  const i = sections.findIndex((s) => s.key === key)
  const moved = sections[i]
  const j = i + dir
  if (!moved || j < 0 || j >= sections.length) return sections
  const next = [...sections]
  next.splice(i, 1)
  next.splice(j, 0, moved)
  return next
}

/** addItem appends a monitor to a section unless the page already lists it. */
export function addItem(sections: BuilderSection[], sectionKey: string, monitorId: number, displayName: string): BuilderSection[] {
  if (usedMonitorIds(sections).has(monitorId)) return sections
  return mapSection(sections, sectionKey, (s) => ({ ...s, items: [...s.items, { monitorId, displayName }] }))
}

export function removeItem(sections: BuilderSection[], monitorId: number): BuilderSection[] {
  return sections.map((s) => ({ ...s, items: s.items.filter((i) => i.monitorId !== monitorId) }))
}

export function renameItem(sections: BuilderSection[], monitorId: number, displayName: string): BuilderSection[] {
  return sections.map((s) => ({
    ...s,
    items: s.items.map((i) => (i.monitorId === monitorId ? { ...i, displayName } : i)),
  }))
}

/**
 * moveItem moves a monitor one place up (-1) or down (1). Past the edge of a section it crosses into
 * the neighbouring section: up from the first item lands last in the previous section, down from the
 * last item lands first in the next one. At the very top or bottom of the page it does nothing.
 */
export function moveItem(sections: BuilderSection[], monitorId: number, dir: -1 | 1): BuilderSection[] {
  const si = sections.findIndex((s) => s.items.some((i) => i.monitorId === monitorId))
  const cur = sections[si]
  if (!cur) return sections
  const ii = cur.items.findIndex((i) => i.monitorId === monitorId)
  const item = cur.items[ii]
  if (!item) return sections
  const j = ii + dir
  if (j >= 0 && j < cur.items.length) {
    const items = [...cur.items]
    items.splice(ii, 1)
    items.splice(j, 0, item)
    return sections.map((s, k) => (k === si ? { ...s, items } : s))
  }
  const target = si + dir
  if (target < 0 || target >= sections.length) return sections
  return sections.map((s, k) => {
    if (k === si) return { ...s, items: s.items.filter((i) => i.monitorId !== monitorId) }
    if (k === target) return { ...s, items: dir < 0 ? [...s.items, item] : [item, ...s.items] }
    return s
  })
}

/** canMoveItem tells the UI whether a move up or down would do anything. */
export function canMoveItem(sections: BuilderSection[], monitorId: number, dir: -1 | 1): boolean {
  return moveItem(sections, monitorId, dir) !== sections
}

// ---- incidents ----

export type IncidentForm = {
  title: string
  severity: IncidentSeverity
  status: IncidentStatus
  body: string
  draft: boolean
}

export const emptyIncidentForm: IncidentForm = { title: '', severity: 'minor', status: 'investigating', body: '', draft: false }

export function validateIncidentForm(f: IncidentForm): Record<string, string> {
  const errors: Record<string, string> = {}
  const title = f.title.trim()
  if (title === '') errors.title = 'Title is required.'
  else if (title.length > MAX_TEXT.incidentTitle) errors.title = `Title must be at most ${MAX_TEXT.incidentTitle} characters.`
  if (f.body.trim().length > MAX_TEXT.incidentBody) errors.body = `Message must be at most ${MAX_TEXT.incidentBody} characters.`
  return errors
}

export function incidentFormToBody(f: IncidentForm) {
  const body: { title: string; severity: IncidentSeverity; status: IncidentStatus; draft: boolean; body?: string } = {
    title: f.title.trim(),
    severity: f.severity,
    status: f.status,
    draft: f.draft,
  }
  if (f.body.trim() !== '') body.body = f.body.trim()
  return body
}

/** validateIncidentUpdate checks a timeline entry; a message is required. */
export function validateIncidentUpdate(message: string): string {
  const m = message.trim()
  if (m === '') return 'A message is required.'
  if (m.length > MAX_TEXT.incidentBody) return `Message must be at most ${MAX_TEXT.incidentBody} characters.`
  return ''
}

export function statusLabel(s: IncidentStatus): string {
  return incidentStatuses.find((x) => x.value === s)?.label ?? s
}

// ---- webhook subscribers ----

/** validateWebhookUrl mirrors the server: http(s), a host, no credentials. */
export function validateWebhookUrl(raw: string): string {
  const v = raw.trim()
  if (v === '') return 'A URL is required.'
  if (v.length > 500) return 'URL must be at most 500 characters.'
  let u: URL
  try {
    u = new URL(v)
  } catch {
    return 'URL must be an http:// or https:// address.'
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || u.hostname === '') {
    return 'URL must be an http:// or https:// address.'
  }
  if (u.username !== '' || u.password !== '') return 'URL must not contain a user name or password.'
  return ''
}

// ---- instance SMTP settings (admin) ----

/** SMTP_MASK is what the API returns for a stored password; sending it back keeps the stored one. */
export const SMTP_MASK = '********'

export type SmtpTls = 'starttls' | 'tls' | 'none'
export const smtpTlsOptions: { value: SmtpTls; label: string }[] = [
  { value: 'starttls', label: 'STARTTLS (usually port 587)' },
  { value: 'tls', label: 'TLS (usually port 465)' },
  { value: 'none', label: 'None (no encryption, no login)' },
]

export type SmtpForm = { host: string; port: string; username: string; password: string; from: string; tls: SmtpTls }

export const emptySmtpForm: SmtpForm = { host: '', port: '587', username: '', password: '', from: '', tls: 'starttls' }

function asTls(v: string): SmtpTls {
  return v === 'tls' || v === 'none' ? v : 'starttls'
}

export function smtpToForm(s: SmtpSettings | null | undefined): SmtpForm {
  if (!s) return emptySmtpForm
  return { host: s.host, port: String(s.port), username: s.username, password: s.password, from: s.from, tls: asTls(s.tls) }
}

/** smtpChanged reports whether the user touched the SMTP fields; only then is `smtp` sent. */
export function smtpChanged(initial: SmtpForm, current: SmtpForm): boolean {
  return (Object.keys(initial) as (keyof SmtpForm)[]).some((k) => initial[k] !== current[k])
}

/** validateSmtpForm returns field errors keyed by the form's field names. */
export function validateSmtpForm(f: SmtpForm): Record<string, string> {
  const errors: Record<string, string> = {}
  if (f.host.trim() === '') errors.host = 'Host is required.'
  const port = Number(f.port)
  if (f.port.trim() === '' || !Number.isInteger(port) || port < 1 || port > 65535) errors.port = 'Port must be 1-65535.'
  const from = f.from.trim()
  if (from === '' || !from.includes('@') || /[\r\n]/.test(from)) errors.from = 'Enter a valid sender address.'
  if (f.tls === 'none' && f.username.trim() !== '') errors.tls = 'Logging in requires STARTTLS or TLS.'
  return errors
}

/**
 * smtpFormToBody builds the `smtp` object. The password is sent as typed: the mask keeps the stored
 * one and an empty string clears it.
 */
export function smtpFormToBody(f: SmtpForm): SmtpSettings {
  return {
    host: f.host.trim(),
    port: Number(f.port),
    username: f.username.trim(),
    password: f.password,
    from: f.from.trim(),
    tls: f.tls,
  }
}
