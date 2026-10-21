import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { SLODetail, SLOStatus } from '../api/types'
import { ErrorBudgetCard } from './ErrorBudgetCard'

// jsdom has no canvas: stand in for uPlot and record what it is given.
const plots: { data: unknown; destroyed: boolean }[] = []
vi.mock('uplot', () => ({
  default: class {
    rec: { data: unknown; destroyed: boolean }
    constructor(_opts: unknown, data: unknown) {
      this.rec = { data, destroyed: false }
      plots.push(this.rec)
    }
    setSize() {}
    destroy() {
      this.rec.destroyed = true
    }
  },
}))

beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    disconnect() {}
    unobserve() {}
  } as unknown as typeof ResizeObserver
})
afterEach(() => {
  cleanup()
  plots.length = 0
})

function detail(status: Partial<SLOStatus>, days = 3): SLODetail {
  return {
    slo: { monitor_id: 1, target: 99.9, window_days: days, fast_burn: 14.4, slow_burn: 6 },
    status: { good: 0, valid: 0, firing: 'none', ...status },
    series: Array.from({ length: days }, (_, i) => ({
      day: `2026-10-${String(17 + i).padStart(2, '0')}`,
      budget_consumed: (status.budget_consumed ?? 0) * ((i + 1) / days),
      ...(status.sli === undefined ? {} : { sli: status.sli }),
    })),
  }
}

describe('ErrorBudgetCard', () => {
  it('says when there is no data yet', () => {
    render(<ErrorBudgetCard slo={detail({})} />)
    expect(screen.getByText('99.9% over 3 days')).toBeInTheDocument()
    expect(screen.getByText(/No checks in the window yet/)).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
    expect(screen.queryByText(/Burning/)).not.toBeInTheDocument()
  })

  it('shows a healthy budget', () => {
    render(
      <ErrorBudgetCard
        slo={detail({ sli: 0.9998, good: 9998, valid: 10000, budget_consumed: 0.2, burn_rate_1h: 0.5, burn_rate_6h: 1.25 })}
      />,
    )
    expect(screen.getByText('SLI', { selector: 'dt' }).nextElementSibling).toHaveTextContent('99.980%')
    expect(screen.getByText('20% used')).toBeInTheDocument()
    expect(screen.getByText('80% remaining')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: 'Error budget used' })).toHaveAttribute('aria-valuenow', '20')
    expect(screen.getByText('0.5×')).toBeInTheDocument()
    expect(screen.getByText('1.3×')).toBeInTheDocument()
    expect(screen.queryByText('Exhausted')).not.toBeInTheDocument()
  })

  it('shows an exhausted budget in red with words', () => {
    render(<ErrorBudgetCard slo={detail({ sli: 0.9985, good: 9985, valid: 10000, budget_consumed: 1.5 })} />)
    expect(screen.getByText('Exhausted')).toBeInTheDocument()
    expect(screen.getByText('150% used')).toBeInTheDocument()
    expect(screen.getByText('0% remaining')).toBeInTheDocument()
    const bar = screen.getByRole('progressbar', { name: 'Error budget used' })
    expect(bar).toHaveAttribute('aria-valuenow', '100')
    expect(bar).toHaveAttribute('aria-valuetext', '150% used, exhausted')
    expect(bar.firstElementChild).toHaveClass('bg-down')
    expect(screen.getByText('—', { selector: '[data-burn="1h"]' })).toBeInTheDocument()
  })

  it.each([
    ['fast', 'Burning fast'],
    ['slow', 'Burning slow'],
  ] as const)('flags firing %s', (firing, label) => {
    render(<ErrorBudgetCard slo={detail({ sli: 0.99, good: 99, valid: 100, budget_consumed: 10, firing })} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })

  it('charts the cumulative budget per day, with a table view, and cleans up', () => {
    const { unmount } = render(<ErrorBudgetCard slo={detail({ sli: 0.999, good: 999, valid: 1000, budget_consumed: 0.9 })} />)
    expect(plots).toHaveLength(1)
    const [xs, ys] = plots[0]!.data as [number[], number[]]
    expect(xs).toHaveLength(3)
    expect(ys.map((v) => Math.round(v))).toEqual([30, 60, 90]) // percent of the budget
    expect(screen.getAllByRole('row')).toHaveLength(4) // header + 3 days
    unmount()
    expect(plots[0]!.destroyed).toBe(true)
  })
})
