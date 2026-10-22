import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useThemeTick } from './useThemeTick'

describe('useThemeTick', () => {
  let listeners: Array<() => void>

  beforeEach(() => {
    listeners = []
    delete document.documentElement.dataset.theme
    vi.stubGlobal('matchMedia', () => ({
      matches: false,
      addEventListener: (_: string, fn: () => void) => listeners.push(fn),
      removeEventListener: (_: string, fn: () => void) => {
        listeners = listeners.filter((l) => l !== fn)
      },
    }))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('changes when the data-theme attribute changes', async () => {
    const { result } = renderHook(() => useThemeTick())
    const before = result.current
    await act(async () => {
      document.documentElement.dataset.theme = 'dark'
      await Promise.resolve() // let the MutationObserver callback run
    })
    expect(result.current).not.toBe(before)
  })

  it('changes when the OS colour scheme changes', () => {
    const { result } = renderHook(() => useThemeTick())
    const before = result.current
    act(() => listeners.forEach((l) => l()))
    expect(result.current).not.toBe(before)
  })

  it('stops listening on unmount', () => {
    const { unmount } = renderHook(() => useThemeTick())
    expect(listeners).toHaveLength(1)
    unmount()
    expect(listeners).toHaveLength(0)
  })
})
