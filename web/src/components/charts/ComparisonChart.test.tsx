import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { ComparisonChart } from './ComparisonChart'

// Recharts needs real layout dimensions; jsdom reports zero, which collapses
// ResponsiveContainer and renders nothing. Force a size.
beforeEach(() => {
  // jsdom has no ResizeObserver; recharts' ResponsiveContainer constructs one.
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    width: 900,
    height: 420,
    top: 0,
    left: 0,
    right: 900,
    bottom: 420,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect)
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    value: 900,
  })
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    value: 420,
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

const TRADERS = [
  { trader_id: 'ta', trader_name: 'Claude', total_pnl_pct: -37 },
  { trader_id: 'tb', trader_name: 'GPT', total_pnl_pct: -40 },
] as any[]

// Matches the production curve's real shape: ~3200 samples spread over ~44 days,
// which averages a sample every ~20 minutes. Getting this wrong matters, because the
// whole defect was about long spans being silently truncated.
const SAMPLE_INTERVAL_MS = 20 * 60_000

function makeHistory(startEquity: number, seed: number) {
  const start = new Date('2026-06-19T00:00:00Z').getTime()
  const out: any[] = []
  let eq = startEquity
  for (let i = 0; i < 3200; i++) {
    eq += Math.sin((i + seed) / 40) * 0.6
    out.push({
      timestamp: new Date(start + i * SAMPLE_INTERVAL_MS).toISOString(),
      total_equity: eq,
      total_pnl_pct: ((eq - startEquity) / startEquity) * 100,
      position_count: (i % 5) + 1,
      position_count_recon: (i % 5) + 1,
      position_notional: 800 + (i % 11) * 40,
      long_notional: 500,
      short_notional: 300 + (i % 11) * 40,
      margin_used_pct: 40 + (i % 30),
    })
  }
  return out
}

vi.mock('../../lib/api', () => ({
  api: {
    getEquityHistoryBatch: vi.fn(async () => ({
      histories: { ta: makeHistory(234, 0), tb: makeHistory(61, 17) },
      sampling: {
        ta: { raw_points: 3200, returned_points: 1500, downsampled: true },
        tb: { raw_points: 3200, returned_points: 1500, downsampled: true },
      },
    })),
  },
}))

vi.mock('../../contexts/LanguageContext', () => ({
  useLanguage: () => ({ language: 'zh' }),
}))

describe('ComparisonChart', () => {
  it('renders the curve and reports the drawn range, not just a point count', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText('复位')).toBeTruthy(), {
      timeout: 5000,
    })
    // The range readout is what makes the axis verifiable at a glance; it was
    // impossible to tell what window was on screen before.
    expect(screen.getByText(/6\/19.*→.*8\//)).toBeTruthy()
  })

  it('spans the full 45 days rather than only the recent tail', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText('复位')).toBeTruthy(), {
      timeout: 5000,
    })
    const label = screen.getByText(/→/).textContent || ''
    // Start must be the first sample's date, which is the whole point of the fix.
    expect(label.startsWith('6/19')).toBe(true)
  })

  it('reset is disabled until the view is actually zoomed', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText('复位')).toBeTruthy(), {
      timeout: 5000,
    })
    const reset = screen
      .getByText('复位')
      .closest('button') as HTMLButtonElement
    expect(reset.disabled).toBe(true)

    fireEvent.click(screen.getByLabelText('放大'))
    await waitFor(() => {
      expect(
        (screen.getByText('复位').closest('button') as HTMLButtonElement)
          .disabled
      ).toBe(false)
    })
  })

  it('zoom narrows the reported range and reset restores it', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText('复位')).toBeTruthy(), {
      timeout: 5000,
    })
    const rangeOf = () => screen.getByText(/→/).textContent || ''
    const full = rangeOf()

    fireEvent.click(screen.getByLabelText('放大'))
    await waitFor(() => expect(rangeOf()).not.toEqual(full))
    const zoomed = rangeOf()
    expect(zoomed).not.toEqual(full)

    fireEvent.click(screen.getByText('复位').closest('button')!)
    await waitFor(() => expect(rangeOf()).toEqual(full))
  })

  it('panning shifts the window without changing its width', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText('复位')).toBeTruthy(), {
      timeout: 5000,
    })
    fireEvent.click(screen.getByLabelText('放大'))
    const before = screen.getByText(/→/).textContent || ''
    fireEvent.click(screen.getByLabelText('向前平移'))
    await waitFor(() =>
      expect(screen.getByText(/→/).textContent).not.toEqual(before)
    )
  })

  it('surfaces that the drawn series is downsampled', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText('已降采样')).toBeTruthy(), {
      timeout: 5000,
    })
  })

  it('shows the zoom/pan affordance so the interaction is discoverable', async () => {
    render(<ComparisonChart traders={TRADERS} />)
    await waitFor(() => expect(screen.getByText(/滚轮缩放/)).toBeTruthy(), {
      timeout: 5000,
    })
  })
})
