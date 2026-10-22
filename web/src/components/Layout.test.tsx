import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Layout } from './Layout'

vi.mock('../api/queries', () => ({
  useMe: () => ({
    data: {
      user: { id: 1, name: 'Dana', email: 'dana@example.com', is_admin: false },
      teams: [{ id: 7, name: 'Example Cloud' }],
    },
  }),
  useLogout: () => ({ mutate: vi.fn() }),
  useCreateTeam: () => ({ mutate: vi.fn(), error: null, isPending: false }),
}))
vi.mock('../hooks/useRole', () => ({ useRole: () => ({ can: () => true }) }))
vi.mock('./ErrorBanner', () => ({ ErrorBanner: () => null }))

function renderLayout() {
  return render(
    <MemoryRouter initialEntries={['/t/7']}>
      <Layout teamId={7}>
        <p>page</p>
      </Layout>
    </MemoryRouter>,
  )
}

const zIndex = (el: Element) => Number(/(?:^|\s)z-(\d+)(?:\s|$)/.exec(el.className)?.[1])

describe('Layout user menu', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
    vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('is a plain disclosure, not an ARIA menu that promises arrow-key navigation', () => {
    renderLayout()
    const trigger = screen.getByRole('button', { name: 'Dana' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitem')).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitemradio')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Log out' })).toBeInTheDocument()
  })

  it('offers System, Light and Dark and applies the choice', () => {
    renderLayout()
    fireEvent.click(screen.getByRole('button', { name: 'Dana' }))
    const group = screen.getByRole('group', { name: 'Theme' })
    const system = within(group).getByRole('button', { name: /System/ })
    const dark = within(group).getByRole('button', { name: 'Dark' })
    expect(system).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(dark)
    expect(dark).toHaveAttribute('aria-pressed', 'true')
    expect(system).toHaveAttribute('aria-pressed', 'false')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('upsera.theme')).toBe('dark')

    fireEvent.click(system)
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(localStorage.getItem('upsera.theme')).toBeNull()
  })

  it('closes on Escape and returns focus to the trigger', () => {
    renderLayout()
    const trigger = screen.getByRole('button', { name: 'Dana' })
    fireEvent.click(trigger)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveFocus()
  })
})

describe('Layout mobile rail', () => {
  afterEach(cleanup)

  it('returns focus to the opener when a nav link closes the rail', () => {
    renderLayout()
    const opener = screen.getByRole('button', { name: 'Open menu' })
    fireEvent.click(opener)
    expect(screen.getByRole('button', { name: 'Close menu' })).toHaveFocus()
    fireEvent.click(screen.getByRole('link', { name: 'Channels' }))
    expect(opener).toHaveFocus()
  })

  it('dims the page with a fixed dark scrim, not the text colour (which would lighten it in dark mode)', () => {
    const { container } = renderLayout()
    fireEvent.click(screen.getByRole('button', { name: 'Open menu' }))
    const backdrop = container.querySelector('div.fixed.inset-0')!
    expect(backdrop.className).toMatch(/(?:^|\s)bg-black\//)
    expect(backdrop.className).not.toMatch(/bg-ink/)
  })

  it('stacks the backdrop above the sticky header and below the rail', () => {
    const { container } = renderLayout()
    fireEvent.click(screen.getByRole('button', { name: 'Open menu' }))
    const backdrop = container.querySelector('div.fixed.inset-0')!
    const header = container.querySelector('header')!
    const rail = container.querySelector('aside')!
    expect(zIndex(header)).toBeLessThan(zIndex(backdrop))
    expect(zIndex(backdrop)).toBeLessThan(zIndex(rail))
  })
})
