import { describe, expect, it } from 'vitest'
import type { Probe, RegionState } from '../api/types'
import { formatAge, probeRunCommand, probeStatusLine, regionRows, validateProbeForm } from './probes'

describe('probeRunCommand', () => {
  it('builds a docker run snippet with the server, token and volume', () => {
    const cmd = probeRunCommand('https://up.example.com', 'upr_abc')
    expect(cmd).toContain('docker run')
    expect(cmd).toContain('-e SERVER_URL=https://up.example.com')
    expect(cmd).toContain('-e PROBE_TOKEN=upr_abc')
    expect(cmd).toContain('-v upsera-probe:/data')
    expect(cmd.trim().endsWith('ghcr.io/davidsugianto/upsera-probe')).toBe(true)
  })
  it('flags plain http servers as needing ALLOW_INSECURE_HTTP', () => {
    expect(probeRunCommand('http://10.0.0.5:3080', 'upr_x')).toContain('-e ALLOW_INSECURE_HTTP=1')
    expect(probeRunCommand('https://up.example.com', 'upr_x')).not.toContain('ALLOW_INSECURE_HTTP')
  })
})

describe('formatAge', () => {
  it('renders compact relative ages', () => {
    expect(formatAge(0)).toBe('just now')
    expect(formatAge(4_000)).toBe('just now')
    expect(formatAge(42_000)).toBe('42s ago')
    expect(formatAge(5 * 60_000 + 3_000)).toBe('5m ago')
    expect(formatAge(3 * 3_600_000 + 60_000)).toBe('3h ago')
    expect(formatAge(2 * 86_400_000)).toBe('2d ago')
  })
  it('never goes negative', () => {
    expect(formatAge(-5_000)).toBe('just now')
  })
})

describe('probeStatusLine', () => {
  const base: Probe = {
    id: 2,
    name: 'Singapore',
    region: 'sg-1',
    registered: true,
    version: '',
    online: false,
    is_local: false,
    created_at: '2026-01-01T00:00:00Z',
  }
  it('shows the registration deadline while awaiting registration', () => {
    const line = probeStatusLine({ ...base, registered: false, registration_expires_at: '2099-01-01T00:00:00Z' }, Date.now())
    expect(line).toMatch(/^awaiting registration, expires /)
  })
  it('says the token expired once past the deadline', () => {
    const line = probeStatusLine(
      { ...base, registered: false, registration_expires_at: '2020-01-01T00:00:00Z' },
      Date.parse('2026-01-01T00:00:00Z'),
    )
    expect(line).toMatch(/^registration token expired /)
  })
  it('shows registered without a registration deadline', () => {
    expect(probeStatusLine({ ...base, registered: false }, Date.now())).toBe('awaiting registration')
    expect(probeStatusLine(base, Date.now())).toBe('registered')
  })
})

describe('regionRows', () => {
  const st = (status: RegionState['status']): RegionState => ({
    status,
    consecutive_failures: 0,
    last_result_at: '2026-01-01T00:00:00Z',
    latency_ms: 12,
    message: '',
  })
  it('sorts local first then alphabetically', () => {
    const rows = regionRows({ 'sg-1': st('up'), local: st('down'), 'fra-1': st('up') })
    expect(rows.map((r) => r.region)).toEqual(['local', 'fra-1', 'sg-1'])
    expect(rows[0]?.state.status).toBe('down')
  })
  it('handles missing states', () => {
    expect(regionRows(undefined)).toEqual([])
    expect(regionRows(null)).toEqual([])
    expect(regionRows({})).toEqual([])
  })
})

describe('validateProbeForm', () => {
  it('accepts slugs', () => {
    expect(validateProbeForm({ name: 'sg-1', region: 'sg-1' })).toEqual({})
  })
  it('requires both fields and rejects bad characters or length', () => {
    expect(Object.keys(validateProbeForm({ name: '', region: '' }))).toEqual(['name', 'region'])
    expect(validateProbeForm({ name: 'Singapore', region: 'sg' }).name).toBeTruthy()
    expect(validateProbeForm({ name: 'a', region: '-x' }).region).toBeTruthy()
    expect(validateProbeForm({ name: 'a'.repeat(41), region: 'sg' }).name).toBeTruthy()
  })
  it('reserves local', () => {
    expect(validateProbeForm({ name: 'x', region: 'local' }).region).toMatch(/reserved/)
  })
})
