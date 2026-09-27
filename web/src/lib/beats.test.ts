import { describe, expect, it } from 'vitest'
import { formatUptime, padBeats } from './beats'

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
