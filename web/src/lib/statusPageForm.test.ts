import { describe, expect, it } from 'vitest'
import type { Monitor, StatusPage } from '../api/types'
import {
  SMTP_MASK,
  addItem,
  addSection,
  availableMonitors,
  canMoveItem,
  emptyIncidentForm,
  emptySmtpForm,
  emptyStatusPageForm,
  formToBody,
  incidentFormToBody,
  moveItem,
  moveSection,
  newSection,
  pageToForm,
  publicUrl,
  removeItem,
  removeSection,
  renameItem,
  renameSection,
  slugify,
  smtpChanged,
  smtpFormToBody,
  smtpToForm,
  validateCustomDomain,
  validateIncidentForm,
  validateIncidentUpdate,
  validateLogoUrl,
  validateSlug,
  validateSmtpForm,
  validateStatusPageForm,
  validateWebhookUrl,
  type BuilderSection,
} from './statusPageForm'

const sec = (name: string, ...ids: number[]): BuilderSection =>
  newSection(
    name,
    ids.map((id) => ({ monitorId: id, displayName: `m${id}` })),
  )
function at<T>(a: T[], i: number): T {
  const v = a[i]
  if (v === undefined) throw new Error(`no element at ${i}`)
  return v
}
const layout = (sections: BuilderSection[]) => sections.map((s) => [s.name, s.items.map((i) => i.monitorId)])

describe('formToBody', () => {
  it('orders items by section then position and trims text', () => {
    const f = {
      ...emptyStatusPageForm(),
      title: ' Acme ',
      slug: ' acme ',
      description: ' d ',
      sections: [sec(' Core ', 3, 1), sec('', 2)],
    }
    const body = formToBody(f)
    expect(body.title).toBe('Acme')
    expect(body.slug).toBe('acme')
    expect(body.description).toBe('d')
    expect(body.items).toEqual([
      { monitor_id: 3, section: 'Core', display_name: 'm3' },
      { monitor_id: 1, section: 'Core', display_name: 'm1' },
      { monitor_id: 2, display_name: 'm2' },
    ])
  })

  it('allows an empty section name and omits an empty display name so the server defaults it', () => {
    const f = { ...emptyStatusPageForm(), sections: [newSection('', [{ monitorId: 7, displayName: '  ' }])] }
    expect(formToBody(f).items).toEqual([{ monitor_id: 7 }])
  })

  it('drops sections without monitors', () => {
    const f = { ...emptyStatusPageForm(), sections: [sec('Empty'), sec('Full', 1)] }
    expect(formToBody(f).items).toEqual([{ monitor_id: 1, section: 'Full', display_name: 'm1' }])
  })

  it('normalizes the custom domain and sends null when blank', () => {
    expect(formToBody({ ...emptyStatusPageForm(), customDomain: ' Status.Example.COM ' }).custom_domain).toBe('status.example.com')
    expect(formToBody({ ...emptyStatusPageForm(), customDomain: '  ' }).custom_domain).toBeNull()
  })
})

describe('pageToForm', () => {
  const page = {
    id: 1,
    team_id: 1,
    slug: 'acme',
    title: 'Acme',
    description: '',
    logo_url: '',
    theme: 'dark',
    custom_domain: null,
    published: true,
    url: 'https://x/status/acme',
    items: [
      { id: 1, section: 'A', monitor_id: 1, display_name: 'One' },
      { id: 2, section: 'A', monitor_id: 2, display_name: 'Two' },
      { id: 3, section: '', monitor_id: 3, display_name: 'Three' },
      { id: 4, section: 'A', monitor_id: 4, display_name: 'Four' },
    ],
    created_at: '',
    updated_at: '',
  } as StatusPage

  it('groups consecutive items by section and round-trips the order', () => {
    const f = pageToForm(page)
    expect(layout(f.sections)).toEqual([
      ['A', [1, 2]],
      ['', [3]],
      ['A', [4]],
    ])
    expect(formToBody(f).items?.map((i) => i.monitor_id)).toEqual([1, 2, 3, 4])
    expect(f.theme).toBe('dark')
    expect(f.published).toBe(true)
  })

  it('starts with one unnamed section when the page has no items', () => {
    const f = pageToForm({ ...page, items: null })
    expect(layout(f.sections)).toEqual([['', []]])
  })
})

describe('validation', () => {
  it('validates slugs like the server', () => {
    expect(validateSlug('acme-status')).toBe('')
    expect(validateSlug('a1')).toBe('')
    for (const bad of ['', 'a', '-ab', 'Acme', 'a_b', 'a b', 'x'.repeat(64)]) expect(validateSlug(bad)).not.toBe('')
    for (const r of ['api', 'assets', 'static', 'admin', 'status', 'new', 'healthz', 'metrics']) {
      expect(validateSlug(r)).toContain('reserved')
    }
  })

  it('suggests a slug from a title', () => {
    expect(slugify('Acme Corp — Status!')).toBe('acme-corp-status')
    expect(slugify('  ')).toBe('')
    expect(validateSlug(slugify('Ünïcode Page'))).toBe('')
  })

  it('accepts only https logo URLs without credentials', () => {
    expect(validateLogoUrl('')).toBe('')
    expect(validateLogoUrl('https://cdn.example.com/logo.png')).toBe('')
    for (const bad of ['http://x.com/l.png', 'ftp://x.com', 'logo.png', 'https://u:p@x.com/l.png']) {
      expect(validateLogoUrl(bad)).not.toBe('')
    }
  })

  it('validates custom domains', () => {
    expect(validateCustomDomain('')).toBe('')
    expect(validateCustomDomain('Status.Example.com')).toBe('')
    for (const bad of ['localhost', '10.0.0.1', '::1', 'https://status.example.com', 'a.com/x', 'a.com:8080', '-a.example.com', 'a..com', '1.2.3.4.5', 'example.123']) {
      expect(validateCustomDomain(bad)).not.toBe('')
    }
    expect(validateCustomDomain('up.example.com', 'UP.example.com')).toContain('differ')
  })

  it('collects field errors keyed like the API', () => {
    const f = { ...emptyStatusPageForm(), title: ' ', slug: 'API', logoUrl: 'http://x', customDomain: 'nope' }
    expect(Object.keys(validateStatusPageForm(f)).sort()).toEqual(['custom_domain', 'logo_url', 'slug', 'title'])
    expect(validateStatusPageForm({ ...emptyStatusPageForm(), title: 'T', slug: 'ok-page' })).toEqual({})
  })

  it('flags over-long public names by their flat item index', () => {
    const f = {
      ...emptyStatusPageForm(),
      title: 'T',
      slug: 'ok-page',
      sections: [sec('A', 1), newSection('B', [{ monitorId: 2, displayName: 'x'.repeat(101) }])],
    }
    expect(Object.keys(validateStatusPageForm(f))).toEqual(['items.1.display_name'])
  })

  it('validates webhook URLs', () => {
    expect(validateWebhookUrl('https://hooks.example.com/abc')).toBe('')
    expect(validateWebhookUrl('http://10.0.0.1:8080/x')).toBe('')
    for (const bad of ['', 'hooks.example.com', 'ftp://x.com/a', 'https://u:p@x.com/a']) {
      expect(validateWebhookUrl(bad)).not.toBe('')
    }
  })
})

describe('publicUrl', () => {
  it('prefers a valid custom domain, else /status/<slug> on the origin', () => {
    expect(publicUrl({ slug: 'acme', customDomain: '' }, 'https://up.example.com/')).toBe('https://up.example.com/status/acme')
    expect(publicUrl({ slug: 'acme', customDomain: 'Status.Acme.io' }, 'https://up.example.com')).toBe('https://status.acme.io')
    expect(publicUrl({ slug: 'acme', customDomain: 'bad' }, 'https://up.example.com')).toBe('https://up.example.com/status/acme')
    expect(publicUrl({ slug: 'API', customDomain: '' }, 'https://up.example.com')).toBe('')
  })
})

describe('builder operations', () => {
  it('adds a monitor once per page, never duplicating', () => {
    const s = [sec('A', 1), sec('B')]
    const once = addItem(s, at(s, 1).key, 2, 'Two')
    expect(layout(once)).toEqual([['A', [1]], ['B', [2]]])
    expect(addItem(once, at(once, 0).key, 2, 'Two')).toBe(once)
    expect(addItem(once, at(once, 0).key, 1, 'One')).toBe(once)
  })

  it('removes and renames items and sections without mutating the input', () => {
    const s = [sec('A', 1, 2), sec('B', 3)]
    const snapshot = layout(s)
    expect(layout(removeItem(s, 2))).toEqual([['A', [1]], ['B', [3]]])
    expect(at(at(renameItem(s, 3, 'Public'), 1).items, 0).displayName).toBe('Public')
    expect(at(renameSection(s, at(s, 0).key, 'Core'), 0).name).toBe('Core')
    expect(layout(removeSection(s, at(s, 0).key))).toEqual([['B', [3]]])
    expect(layout(s)).toEqual(snapshot)
  })

  it('moves items within a section', () => {
    const s = [sec('A', 1, 2, 3)]
    expect(layout(moveItem(s, 2, -1))).toEqual([['A', [2, 1, 3]]])
    expect(layout(moveItem(s, 2, 1))).toEqual([['A', [1, 3, 2]]])
  })

  it('moves items across section boundaries', () => {
    const s = [sec('A', 1, 2), sec('B', 3, 4)]
    expect(layout(moveItem(s, 3, -1))).toEqual([['A', [1, 2, 3]], ['B', [4]]])
    expect(layout(moveItem(s, 2, 1))).toEqual([['A', [1]], ['B', [2, 3, 4]]])
  })

  it('does nothing at the top and bottom of the page', () => {
    const s = [sec('A', 1), sec('B', 2)]
    expect(moveItem(s, 1, -1)).toBe(s)
    expect(moveItem(s, 2, 1)).toBe(s)
    expect(moveItem(s, 99, 1)).toBe(s)
    expect(canMoveItem(s, 1, -1)).toBe(false)
    expect(canMoveItem(s, 1, 1)).toBe(true)
  })

  it('moves sections and adds new ones', () => {
    const s = [sec('A'), sec('B'), sec('C')]
    expect(moveSection(s, at(s, 2).key, -1).map((x) => x.name)).toEqual(['A', 'C', 'B'])
    expect(moveSection(s, at(s, 0).key, -1)).toBe(s)
    expect(addSection(s, 'D').map((x) => x.name)).toEqual(['A', 'B', 'C', 'D'])
  })

  it('offers only monitors not already on the page, sorted by name', () => {
    const monitors = [
      { id: 1, name: 'zeta' },
      { id: 2, name: 'alpha' },
      { id: 3, name: 'beta' },
    ] as Monitor[]
    expect(availableMonitors(monitors, [sec('A', 1)]).map((m) => m.id)).toEqual([2, 3])
  })
})

describe('incident forms', () => {
  it('requires a title and bounds the message', () => {
    expect(validateIncidentForm(emptyIncidentForm).title).toBeTruthy()
    expect(validateIncidentForm({ ...emptyIncidentForm, title: 'DB down' })).toEqual({})
    expect(validateIncidentForm({ ...emptyIncidentForm, title: 'x', body: 'y'.repeat(2001) }).body).toBeTruthy()
    expect(validateIncidentUpdate('  ')).not.toBe('')
    expect(validateIncidentUpdate('Fixed')).toBe('')
  })

  it('omits an empty message from the request', () => {
    expect(incidentFormToBody({ ...emptyIncidentForm, title: ' T ' })).toEqual({
      title: 'T',
      severity: 'minor',
      status: 'investigating',
      draft: false,
    })
    expect(incidentFormToBody({ ...emptyIncidentForm, title: 'T', body: ' hi ', draft: true }).body).toBe('hi')
  })
})

describe('smtp form', () => {
  const stored = { host: 'smtp.example.com', port: 587, username: 'u', password: SMTP_MASK, from: 'Up <up@example.com>', tls: 'starttls' }

  it('keeps the stored password by round-tripping the mask', () => {
    const f = smtpToForm(stored)
    expect(f.password).toBe(SMTP_MASK)
    expect(smtpFormToBody(f)).toEqual(stored)
    expect(smtpChanged(smtpToForm(stored), f)).toBe(false)
  })

  it('clears the password when the box is emptied and detects the change', () => {
    const f = { ...smtpToForm(stored), password: '' }
    expect(smtpFormToBody(f).password).toBe('')
    expect(smtpChanged(smtpToForm(stored), f)).toBe(true)
  })

  it('treats the untouched empty form as unchanged', () => {
    expect(smtpToForm(null)).toEqual(emptySmtpForm)
    expect(smtpChanged(smtpToForm(null), emptySmtpForm)).toBe(false)
  })

  it('validates host, port, sender and tls/login combination', () => {
    expect(Object.keys(validateSmtpForm(emptySmtpForm)).sort()).toEqual(['from', 'host'])
    expect(validateSmtpForm({ ...smtpToForm(stored) })).toEqual({})
    expect(validateSmtpForm({ ...smtpToForm(stored), port: '70000' }).port).toBeTruthy()
    expect(validateSmtpForm({ ...smtpToForm(stored), port: '25.5' }).port).toBeTruthy()
    expect(validateSmtpForm({ ...smtpToForm(stored), tls: 'none' }).tls).toBeTruthy()
    expect(validateSmtpForm({ ...smtpToForm(stored), tls: 'none', username: '' })).toEqual({})
  })

  it('falls back to starttls for an unknown tls value', () => {
    expect(smtpToForm({ ...stored, tls: 'weird' }).tls).toBe('starttls')
  })
})
