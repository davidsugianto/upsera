import { describe, expect, it } from 'vitest'
import { REDACTED, configToForm, formToConfig } from './channelForm'

describe('channel config forms', () => {
  it('preserves an untouched redacted secret', () => {
    const form = configToForm('telegram', { bot_token: REDACTED, chat_id: '42' })
    expect(formToConfig('telegram', form)).toEqual({ bot_token: REDACTED, chat_id: '42' })
  })
  it('keeps untouched redacted webhook headers redacted', () => {
    const form = configToForm('webhook', { url: REDACTED, headers: REDACTED })
    expect(form.headersMasked).toBe(true)
    expect(formToConfig('webhook', form)).toEqual({ url: REDACTED, headers: REDACTED })
  })
  it('sends replaced headers as an object, even when cleared', () => {
    const form = configToForm('webhook', { url: REDACTED, headers: REDACTED })
    form.replaceHeaders = true
    form.headers = [{ key: 'Authorization', value: 'Bearer x' }]
    expect(formToConfig('webhook', form).headers).toEqual({ Authorization: 'Bearer x' })
    form.headers = []
    expect(formToConfig('webhook', form).headers).toEqual({})
  })
  it('omits empty optional fields and applies defaults', () => {
    const form = configToForm('smtp', {})
    expect(form.values.port).toBe('587')
    expect(form.values.tls).toBe('starttls')
    form.values.host = 'smtp.example.com'
    form.values.from = 'a@example.com'
    form.lists.to = ['b@example.com', ' ']
    expect(formToConfig('smtp', form)).toEqual({
      host: 'smtp.example.com',
      port: 587,
      from: 'a@example.com',
      to: ['b@example.com'],
      tls: 'starttls',
    })
  })
})
