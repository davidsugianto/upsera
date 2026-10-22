import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { StatusPage } from '../../api/types'
import { StatusPagesPage } from './StatusPagesPage'

const pages = [
  { id: 1, title: 'Live page', published: true, url: 'https://status.example.com/status/live', slug: 'live', monitor_ids: [] },
  { id: 2, title: 'Work in progress', published: false, url: 'https://status.example.com/status/wip', slug: 'wip', monitor_ids: [] },
] as unknown as StatusPage[]

vi.mock('../../api/queries', () => ({
  useStatusPages: () => ({ data: pages, isPending: false, error: null }),
  useDeleteStatusPage: () => ({ mutate: vi.fn(), isPending: false }),
}))
vi.mock('../../hooks/useRole', () => ({ useRole: () => ({ can: () => true }) }))

describe('StatusPagesPage', () => {
  afterEach(cleanup)

  it('marks each page Published or Draft in words', () => {
    render(
      <MemoryRouter initialEntries={['/t/7/status-pages']}>
        <Routes>
          <Route path="/t/:teamId/status-pages" element={<StatusPagesPage />} />
        </Routes>
      </MemoryRouter>,
    )
    const item = (title: string) => within(screen.getByText(title).closest('li')!)
    expect(item('Live page').getByText('Published')).toBeInTheDocument()
    expect(item('Live page').queryByText('Draft')).not.toBeInTheDocument()
    expect(item('Work in progress').getByText('Draft')).toBeInTheDocument()
    expect(item('Work in progress').queryByText('Published')).not.toBeInTheDocument()
  })
})
