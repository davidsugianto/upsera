import { describe, expect, it } from 'vitest'
import type { DockerHost } from '../api/types'
import { REDACTED, formToBody, hostToForm, insecureWarning } from './dockerHostForm'

const form = { name: ' remote ', endpoint: ' tcp://10.0.0.5:2376 ', ca: '', cert: '', key: '' }

describe('formToBody', () => {
  it('omits empty TLS fields on create', () => {
    expect(formToBody(form, false)).toEqual({ name: 'remote', endpoint: 'tcp://10.0.0.5:2376' })
  })
  it('sends new PEM material on create', () => {
    expect(formToBody({ ...form, ca: 'CA' }, false).tls).toEqual({ ca: 'CA' })
  })
  it('clears emptied fields and keeps redacted ones on update', () => {
    const body = formToBody({ ...form, ca: REDACTED, cert: '', key: 'NEWKEY' }, true)
    expect(body.tls).toEqual({ ca: REDACTED, cert: '', key: 'NEWKEY' })
  })
})

describe('hostToForm', () => {
  it('carries the redacted placeholders so an unchanged save keeps the secrets', () => {
    const h = {
      id: 1,
      team_id: 1,
      name: 'r',
      endpoint: 'tcp://h',
      tls: { ca: REDACTED, cert: REDACTED, key: REDACTED },
    } as unknown as DockerHost
    expect(formToBody(hostToForm(h), true).tls).toEqual({ ca: REDACTED, cert: REDACTED, key: REDACTED })
  })
})

describe('insecureWarning', () => {
  it('warns for plain tcp and http only', () => {
    expect(insecureWarning(form)).not.toBe('')
    expect(insecureWarning({ ...form, endpoint: 'http://h' })).not.toBe('')
    expect(insecureWarning({ ...form, ca: 'CA' })).toBe('')
    expect(insecureWarning({ ...form, endpoint: 'https://h' })).toBe('')
    expect(insecureWarning({ ...form, endpoint: 'unix:///var/run/docker.sock' })).toBe('')
  })
})
