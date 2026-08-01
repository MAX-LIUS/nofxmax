import { describe, expect, it } from 'vitest'
import { sortSymbolStats, positionPnlPct } from './PositionHistory'
import type { HistoricalPosition, SymbolStats } from '../../types'

function stat(symbol: string, over: Partial<SymbolStats> = {}): SymbolStats {
  return {
    symbol,
    total_trades: 1,
    win_trades: 0,
    win_rate: 0,
    total_pnl: 0,
    avg_pnl: 0,
    avg_hold_mins: 0,
    ...over,
  }
}

// A 12-symbol population: more than the old top-10 cut, so the two symbols the
// panel used to hide are the ones these tests reach for.
const population: SymbolStats[] = [
  stat('AAAUSDT', {
    total_pnl: 100,
    win_rate: 80,
    total_trades: 10,
    avg_pnl: 10,
  }),
  stat('BBBUSDT', {
    total_pnl: 90,
    win_rate: 70,
    total_trades: 9,
    avg_pnl: 10,
  }),
  stat('CCCUSDT', {
    total_pnl: 80,
    win_rate: 60,
    total_trades: 8,
    avg_pnl: 10,
  }),
  stat('DDDUSDT', {
    total_pnl: 70,
    win_rate: 55,
    total_trades: 7,
    avg_pnl: 10,
  }),
  stat('EEEUSDT', {
    total_pnl: 60,
    win_rate: 50,
    total_trades: 6,
    avg_pnl: 10,
  }),
  stat('FFFUSDT', {
    total_pnl: 50,
    win_rate: 45,
    total_trades: 5,
    avg_pnl: 10,
  }),
  stat('GGGUSDT', {
    total_pnl: 40,
    win_rate: 40,
    total_trades: 4,
    avg_pnl: 10,
  }),
  stat('HHHUSDT', {
    total_pnl: 30,
    win_rate: 35,
    total_trades: 3,
    avg_pnl: 10,
  }),
  stat('IIIUSDT', {
    total_pnl: 20,
    win_rate: 30,
    total_trades: 2,
    avg_pnl: 10,
  }),
  stat('JJJUSDT', {
    total_pnl: 10,
    win_rate: 25,
    total_trades: 1,
    avg_pnl: 10,
  }),
  stat('KKKUSDT', {
    total_pnl: -50,
    win_rate: 10,
    total_trades: 12,
    avg_pnl: -4,
  }),
  stat('LLLUSDT', {
    total_pnl: -80,
    win_rate: 5,
    total_trades: 20,
    avg_pnl: -4,
  }),
]

describe('sortSymbolStats', () => {
  it('surfaces the worst symbols that the top-10 cut used to hide', () => {
    // The whole reason for ascending sort: KKK/LLL are the two biggest losers and
    // ranked 11th/12th by PnL, so the old `slice(0, 10)` never showed them.
    const asc = sortSymbolStats(population, 'total_pnl', 'asc')
    expect(asc.slice(0, 2).map((s) => s.symbol)).toEqual(['LLLUSDT', 'KKKUSDT'])
  })

  it('reverses on direction and keeps the full population', () => {
    const desc = sortSymbolStats(population, 'total_pnl', 'desc')
    expect(desc[0].symbol).toBe('AAAUSDT')
    expect(desc).toHaveLength(population.length)
    expect(desc.map((s) => s.symbol).reverse()).toEqual(
      sortSymbolStats(population, 'total_pnl', 'asc').map((s) => s.symbol)
    )
  })

  it('sorts symbol names alphabetically, not numerically', () => {
    const asc = sortSymbolStats(population, 'symbol', 'asc')
    expect(asc[0].symbol).toBe('AAAUSDT')
    expect(asc[asc.length - 1].symbol).toBe('LLLUSDT')
  })

  it('sorts on trade count and win rate independently of PnL', () => {
    // LLL is the worst by PnL but the most traded: a column sort must not
    // secretly fall back to the PnL ordering.
    expect(sortSymbolStats(population, 'total_trades', 'desc')[0].symbol).toBe(
      'LLLUSDT'
    )
    expect(sortSymbolStats(population, 'win_rate', 'desc')[0].symbol).toBe(
      'AAAUSDT'
    )
  })

  it('does not mutate the input array', () => {
    const before = population.map((s) => s.symbol)
    sortSymbolStats(population, 'win_rate', 'asc')
    expect(population.map((s) => s.symbol)).toEqual(before)
  })

  it('handles an empty list and missing metrics without throwing', () => {
    expect(sortSymbolStats([], 'total_pnl', 'desc')).toEqual([])
    const partial = [
      { symbol: 'XUSDT' } as SymbolStats,
      stat('YUSDT', { total_pnl: 5 }),
    ]
    // Missing metric is treated as 0, so it ranks below the positive one.
    expect(sortSymbolStats(partial, 'total_pnl', 'desc')[0].symbol).toBe(
      'YUSDT'
    )
  })
})

function pos(over: Partial<HistoricalPosition>): HistoricalPosition {
  return { symbol: 'ETHUSDT', side: 'LONG', ...over } as HistoricalPosition
}

describe('positionPnlPct', () => {
  it('signs a short by the profit direction, not the raw price move', () => {
    // The ETHUSDT short that prompted this: entry 1862.63, exit 1869.48 — price
    // ROSE, so a short LOST. A raw price-move % would report +0.37%.
    const pct = positionPnlPct(
      pos({ side: 'SHORT', entry_price: 1862.63, exit_price: 1869.48 })
    )
    expect(pct).toBeLessThan(0)
    expect(pct).toBeCloseTo(-0.3677, 3)
  })

  it('reports a long the plain way', () => {
    expect(
      positionPnlPct(pos({ entry_price: 100, exit_price: 105 }))
    ).toBeCloseTo(5, 6)
  })

  it('returns 0 rather than Infinity or NaN when prices are missing', () => {
    // Open positions have no exit price; a divide-by-zero here would render
    // "NaN%" in the dropdown for every still-open row.
    expect(positionPnlPct(pos({ entry_price: 0, exit_price: 105 }))).toBe(0)
    expect(positionPnlPct(pos({ entry_price: 100 }))).toBe(0)
    expect(positionPnlPct(pos({}))).toBe(0)
  })

  it('treats an unknown side as short-side signing only for non-LONG', () => {
    // Defensive: side is a free-form string from the API. Anything that is not
    // LONG is signed as a short, which matches how the row colours it.
    expect(
      positionPnlPct(pos({ side: 'short', entry_price: 100, exit_price: 90 }))
    ).toBeCloseTo(10, 6)
    expect(
      positionPnlPct(pos({ side: 'long', entry_price: 100, exit_price: 90 }))
    ).toBeCloseTo(-10, 6)
  })
})
