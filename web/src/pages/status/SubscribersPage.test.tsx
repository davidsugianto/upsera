import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Subscriber } from '../../api/types'
import { SubscribersPage } from './SubscribersPage'

const subs = [
  { id: 1, kind: 'email', target: 'confirmed@example.com', confirmed: true, created_at: '2026-10-01T10:00:00Z' },
  { id: 2, kind: 'email', target: 'waiting@example.com', confirmed: false, created_at: '2026-10-02T10:00:00Z' },
] as unknown as Subscriber[]

vi.mock('../../api/queries', () => ({
  useStatusPages: () => ({ data: [{ id: 3, title: 'Acme', slug: 'acme' }], isPending: false, error: null }),
  useSubscribers: () => ({ data: subs, isPending: false, error: null }),
  useDeleteSubscriber: () => ({ mutate: vi.fn(), isPending: false, error: null }),
  useAddWebhookSubscriber: () => ({ mutate: vi.fn(), isPending: false, error: null }),
}))
vi.mock('../../hooks/useRole', () => ({ useRole: () => ({ can: () => true }) }))

describe('SubscribersPage', () => {
  afterEach(cleanup)

  it('shows whether each subscriber is confirmed in words', () => {
    render(
      <MemoryRouter initialEntries={['/t/7/status-pages/3/subscribers']}>
        <Routes>
          <Route path="/t/:teamId/status-pages/:pageId/subscribers" element={<SubscribersPage />} />
        </Routes>
      </MemoryRouter>,
    )
    const row = (target: string) => within(screen.getByText(target).closest('tr')!)
    expect(row('confirmed@example.com').getByText('Yes')).toBeInTheDocument()
    expect(row('confirmed@example.com').queryByText('Pending')).not.toBeInTheDocument()
    expect(row('waiting@example.com').getByText('Pending')).toBeInTheDocument()
    expect(row('waiting@example.com').queryByText('Yes')).not.toBeInTheDocument()
  })
})
