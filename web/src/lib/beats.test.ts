import { describe, expect, it } from 'vitest'
import type { Heartbeat } from '../api/types'
import { formatUptime, groupByRegion, padBeats } from './beats'

describe('padBeats', () => {
  it('left-pads short input with null', () => {
    expect(padBeats(['a', 'b'], 4)).toEqual([null, null, 'a', 'b'])
  })
  it('keeps the newest n', () => {
    const beats = Array.from({ length: 60 }, (_, i) => i)
    const got = padBeats(beats, 50)
    expect(got).toHaveLength(50)
    expect(got[0]).toBe(10)
    expect(got[49]).toBe(59)
  })
})

describe('formatUptime', () => {
  it('shows a dash without data', () => {
    expect(formatUptime(null)).toBe('—')
    expect(formatUptime(undefined)).toBe('—')
  })
  it('formats percentages', () => {
    expect(formatUptime(100)).toBe('100%')
    expect(formatUptime(99.954)).toBe('99.95%')
    expect(formatUptime(99.95)).toBe('99.95%')
    expect(formatUptime(0)).toBe('0.00%')
  })
  it('never rounds a near-perfect uptime up to 100%', () => {
    expect(formatUptime(99.999)).toBe('99.99%')
  })
})

describe('groupByRegion', () => {
  const hb = (time: string, region?: string): Heartbeat =>
    ({ time, region, status: 'up', latency_ms: 1, message: '', probe_id: 1 }) as Heartbeat
  it('groups by region keeping order, local first then alphabetical', () => {
    const beats = [hb('1', 'sg-1'), hb('2', 'local'), hb('3', 'fra-1'), hb('4', 'sg-1'), hb('5', 'local')]
    const groups = groupByRegion(beats)
    expect(groups.map((g) => g.region)).toEqual(['local', 'fra-1', 'sg-1'])
    expect(groups[0]?.beats.map((b) => b.time)).toEqual(['2', '5'])
    expect(groups[2]?.beats.map((b) => b.time)).toEqual(['1', '4'])
  })
  it('treats a missing or empty region as local', () => {
    const groups = groupByRegion([hb('1'), hb('2', ''), hb('3', 'local')])
    expect(groups).toHaveLength(1)
    expect(groups[0]?.region).toBe('local')
    expect(groups[0]?.beats).toHaveLength(3)
  })
  it('returns no groups for no beats', () => {
    expect(groupByRegion([])).toEqual([])
  })
})
