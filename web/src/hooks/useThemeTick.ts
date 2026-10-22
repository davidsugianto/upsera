import { useEffect, useState } from 'react'

/**
 * useThemeTick changes whenever the appearance does (the OS colour scheme or the data-theme
 * attribute), so canvas charts that read CSS variables when they draw can redraw.
 */
export function useThemeTick(): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    const bump = () => setTick((n) => n + 1)
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)')
    mq?.addEventListener?.('change', bump)
    const mo = new MutationObserver(bump)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => {
      mq?.removeEventListener?.('change', bump)
      mo.disconnect()
    }
  }, [])
  return tick
}
