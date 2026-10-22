import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Monitor, Status } from '../../api/types'
import { MonitorsPage } from './MonitorsPage'

const list: Monitor[] = (['up', 'down', 'pending', 'degraded', 'maintenance'] as Status[]).map(
  (status, i) => ({ id: i + 1, name: `mon-${status}`, type: 'tcp', group_name: '', tags: [], paused: false, state: { status } }) as unknown as Monitor,
)

vi.mock('../../api/queries', () => ({
  useMonitors: () => ({ data: list, isPending: false, error: null }),
  useOverview: () => ({ data: [], error: null }),
}))
vi.mock('../../hooks/useRole', () => ({ useRole: () => ({ can: () => true }) }))
vi.mock('../../lib/monitorForm', () => ({ targetSummary: () => 'example.com:443' }))

describe('MonitorsPage rows', () => {
  afterEach(cleanup)

  it('says the state in words for every non-up monitor, so colour is never the only signal', () => {
    render(
      <MemoryRouter initialEntries={['/t/7']}>
        <Routes>
          <Route path="/t/:teamId" element={<MonitorsPage />} />
        </Routes>
      </MemoryRouter>,
    )
    const row = (name: string) => within(screen.getByRole('link', { name: new RegExp(name) }))
    expect(row('mon-down').getByText('Down')).toBeInTheDocument()
    expect(row('mon-pending').getByText('Pending')).toBeInTheDocument()
    expect(row('mon-degraded').getByText('Degraded')).toBeInTheDocument()
    expect(row('mon-maintenance').getByText('Maintenance')).toBeInTheDocument()
    // An up monitor stays quiet: only its dot carries the state.
    expect(row('mon-up').queryByText(/^(Up|Down|Pending|Degraded|Maintenance)$/)).not.toBeInTheDocument()
  })
})
