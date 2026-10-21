import { describe, expect, it } from 'vitest'
import { defaultSLOForm, sloFormToInput, sloToForm, validateSLOForm, type SLOForm } from './MonitorFormPage'

const ok: SLOForm = { ...defaultSLOForm(), enabled: true }

describe('SLO form', () => {
  it('defaults to off with 99.9 / 30 / 14.4 / 6', () => {
    expect(defaultSLOForm()).toEqual({ enabled: false, target: 99.9, window_days: 30, fast_burn: 14.4, slow_burn: 6 })
    expect(validateSLOForm(ok)).toEqual({})
  })

  it('validates nothing while disabled', () => {
    expect(validateSLOForm({ ...defaultSLOForm(), target: 100 })).toEqual({})
  })

  it.each([
    ['target', { target: 0 }, 'slo.target'],
    ['target', { target: 100 }, 'slo.target'],
    ['target', { target: -5 }, 'slo.target'],
    ['target', { target: NaN }, 'slo.target'],
    ['target decimals', { target: 99.9995 }, 'slo.target'],
    ['window', { window_days: 6 }, 'slo.window_days'],
    ['window', { window_days: 91 }, 'slo.window_days'],
    ['window', { window_days: 30.5 }, 'slo.window_days'],
    ['slow', { slow_burn: 0.5 }, 'slo.slow_burn'],
    ['slow above fast', { fast_burn: 5, slow_burn: 6 }, 'slo.slow_burn'],
    ['fast', { fast_burn: 1001 }, 'slo.fast_burn'],
    ['fast', { fast_burn: NaN }, 'slo.fast_burn'],
  ] as const)('rejects %s %o', (_name, patch, field) => {
    const errs = validateSLOForm({ ...ok, ...patch })
    expect(Object.keys(errs)).toContain(field)
    expect(errs[field]).toBeTruthy()
  })

  it('accepts the edges', () => {
    expect(validateSLOForm({ ...ok, target: 99.999, window_days: 7, fast_burn: 1000, slow_burn: 1 })).toEqual({})
    expect(validateSLOForm({ ...ok, target: 0.001, window_days: 90, fast_burn: 6, slow_burn: 6 })).toEqual({})
  })

  it('round-trips an SLO', () => {
    const f = sloToForm({ monitor_id: 3, target: 99.5, window_days: 7, fast_burn: 20, slow_burn: 2 })
    expect(f).toEqual({ enabled: true, target: 99.5, window_days: 7, fast_burn: 20, slow_burn: 2 })
    expect(sloFormToInput(f)).toEqual({ target: 99.5, window_days: 7, fast_burn: 20, slow_burn: 2 })
    expect(sloToForm(null)).toEqual(defaultSLOForm())
  })
})
