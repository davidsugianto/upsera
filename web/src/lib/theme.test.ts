import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyChoice, resolveTheme, saveChoice, storedChoice, storedTheme } from './theme'

function mockOS(dark: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('dark') ? dark : !dark, media: q }))
}

describe('theme', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
  })
  afterEach(() => vi.unstubAllGlobals())

  it('defaults to following the OS', () => {
    expect(storedChoice()).toBe('system')
  })

  it('keeps honouring a previously saved light or dark choice', () => {
    localStorage.setItem('upsera.theme', 'light')
    expect(storedChoice()).toBe('light')
    localStorage.setItem('upsera.theme', 'dark')
    expect(storedChoice()).toBe('dark')
  })

  it('ignores junk in storage', () => {
    localStorage.setItem('upsera.theme', 'purple')
    expect(storedChoice()).toBe('system')
  })

  it('resolves system from the OS preference', () => {
    mockOS(true)
    expect(resolveTheme('system')).toBe('dark')
    mockOS(false)
    expect(resolveTheme('system')).toBe('light')
    expect(resolveTheme('dark')).toBe('dark')
  })

  it('storedTheme is the resolved light or dark value', () => {
    mockOS(true)
    expect(storedTheme()).toBe('dark')
    localStorage.setItem('upsera.theme', 'light')
    expect(storedTheme()).toBe('light')
  })

  it('applyChoice sets data-theme only for an explicit choice', () => {
    applyChoice('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
    applyChoice('system')
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })

  it('saveChoice persists light/dark and clears the key for system', () => {
    saveChoice('light')
    expect(localStorage.getItem('upsera.theme')).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    saveChoice('system')
    expect(localStorage.getItem('upsera.theme')).toBeNull()
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })
})
