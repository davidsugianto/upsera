import { describe, expect, it } from 'vitest'
import type { Monitor } from '../api/types'
import { defaultMonitorForm, formToBody, monitorToForm } from './monitorForm'

describe('formToBody', () => {
  it('emits only http keys for http monitors', () => {
    const f = defaultMonitorForm('http')
    f.name = 'site'
    f.http.url = 'https://example.com'
    f.keyword.keyword = 'leftover'
    const body = formToBody(f)
    expect(body.config).toEqual({
      url: 'https://example.com',
      method: 'GET',
      accepted_status: ['200-299'],
      max_redirects: 10,
      ignore_tls_errors: false,
    })
    expect(body.config).not.toHaveProperty('keyword')
    expect(body.channel_ids).toEqual([])
    expect(body).not.toHaveProperty('parent_id')
    expect(body).not.toHaveProperty('escalation_policy_id')
  })
  it('adds keyword keys for keyword monitors', () => {
    const f = defaultMonitorForm('keyword')
    f.keyword.keyword = 'ok'
    expect(formToBody(f).config).toMatchObject({ keyword: 'ok', invert_keyword: false, case_sensitive: false })
  })
  it('sends an empty config for push monitors', () => {
    const f = defaultMonitorForm('push')
    f.http.url = 'https://ignored'
    expect(formToBody(f).config).toEqual({})
  })
  it('omits an empty dns resolver', () => {
    const f = defaultMonitorForm('dns')
    f.dns.hostname = 'example.com'
    expect(formToBody(f).config).toEqual({ hostname: 'example.com', record_type: 'A' })
  })
  it('sends the body only for methods that take one, and headers when set', () => {
    const f = defaultMonitorForm('http')
    f.http.body = '{}'
    f.http.headers = [{ key: 'X-A', value: '1' }, { key: ' ', value: 'dropped' }]
    expect(formToBody(f).config).not.toHaveProperty('body')
    f.http.method = 'POST'
    expect(formToBody(f).config).toMatchObject({ body: '{}', headers: { 'X-A': '1' } })
  })
  it('converts parent and policy selections to numbers', () => {
    const f = defaultMonitorForm('push')
    f.parentId = '7'
    f.policyId = '3'
    expect(formToBody(f)).toMatchObject({ parent_id: 7, escalation_policy_id: 3 })
  })
})

describe('monitorToForm', () => {
  it('round-trips a keyword monitor', () => {
    const m = {
      type: 'keyword',
      name: 'api',
      group_name: 'prod',
      tags: ['a'],
      interval_s: 30,
      retry_interval_s: 20,
      retries: 2,
      timeout_s: 5,
      paused: false,
      parent_id: null,
      escalation_policy_id: 4,
      channel_ids: [1],
      config: {
        url: 'https://x',
        method: 'GET',
        accepted_status: ['200-299', '301'],
        max_redirects: 0,
        ignore_tls_errors: true,
        keyword: 'ok',
        invert_keyword: true,
        case_sensitive: false,
      },
    } as unknown as Monitor
    const body = formToBody(monitorToForm(m))
    expect(body).toMatchObject({ name: 'api', group_name: 'prod', escalation_policy_id: 4, channel_ids: [1] })
    expect(body.config).toEqual(m.config)
  })
})
