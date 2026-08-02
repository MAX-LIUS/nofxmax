import { describe, it, expect } from 'vitest'
import { mergeTraderHistories } from './comparisonMerge'

const T = new Date('2026-06-19T00:00:00Z').getTime()
const iso = (offsetMin: number) =>
  new Date(T + offsetMin * 60_000).toISOString()

const traders = [{ trader_id: 'a' }, { trader_id: 'b' }] as any[]

describe('mergeTraderHistories', () => {
  it('aligns traders that sample at different instants within a minute', () => {
    const rows = mergeTraderHistories(traders, [
      [{ timestamp: iso(0), total_equity: 100, total_pnl_pct: 0 }],
      [
        {
          timestamp: new Date(T + 20_000).toISOString(),
          total_equity: 200,
          total_pnl_pct: 5,
        },
      ],
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].a_pnl_pct).toBe(0)
    expect(rows[0].b_pnl_pct).toBe(5)
  })

  it('carries a trader forward on minutes where only the other reported', () => {
    const rows = mergeTraderHistories(traders, [
      [
        { timestamp: iso(0), total_equity: 100, total_pnl_pct: 1 },
        { timestamp: iso(5), total_equity: 110, total_pnl_pct: 2 },
      ],
      [{ timestamp: iso(0), total_equity: 200, total_pnl_pct: 9 }],
    ])
    expect(rows).toHaveLength(2)
    expect(rows[1].b_pnl_pct).toBe(9)
    // Carried values must be marked, so the tooltip does not present an old book
    // reading as a fresh one.
    expect(rows[1].b_stale).toBe(true)
    expect(rows[1].a_stale).toBe(false)
  })

  it('leaves a trader absent before its first sample rather than back-filling', () => {
    const rows = mergeTraderHistories(traders, [
      [{ timestamp: iso(0), total_equity: 100, total_pnl_pct: 1 }],
      [{ timestamp: iso(10), total_equity: 200, total_pnl_pct: 9 }],
    ])
    // b started later; the first row must not invent a value for it, or the chart
    // would show it competing before it existed.
    expect(rows[0].b_pnl_pct).toBeUndefined()
    expect(rows[1].b_pnl_pct).toBe(9)
  })

  it('keeps the latest sample when several land in one minute', () => {
    const rows = mergeTraderHistories(traders, [
      [
        {
          timestamp: new Date(T + 10_000).toISOString(),
          total_equity: 100,
          total_pnl_pct: 1,
        },
        {
          timestamp: new Date(T + 50_000).toISOString(),
          total_equity: 105,
          total_pnl_pct: 3,
        },
      ],
      [],
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].a_pnl_pct).toBe(3)
  })

  it('emits rows in ascending time even if input is unsorted', () => {
    const rows = mergeTraderHistories(traders, [
      [
        { timestamp: iso(10), total_equity: 110, total_pnl_pct: 2 },
        { timestamp: iso(0), total_equity: 100, total_pnl_pct: 1 },
        { timestamp: iso(5), total_equity: 105, total_pnl_pct: 1.5 },
      ],
      [],
    ])
    const times = rows.map((r) => r.ts)
    expect(times).toEqual([...times].sort((x, y) => x - y))
  })

  it('carries the book fields, not just pnl', () => {
    const rows = mergeTraderHistories(traders, [
      [
        {
          timestamp: iso(0),
          total_equity: 100,
          total_pnl_pct: 1,
          position_count: 4,
          position_count_recon: 4,
          position_notional: 1234.5,
          long_notional: 1000,
          short_notional: 234.5,
          margin_used_pct: 66,
        },
      ],
      [
        { timestamp: iso(0), total_equity: 50, total_pnl_pct: 0 },
        { timestamp: iso(5), total_equity: 55, total_pnl_pct: 1 },
      ],
    ])
    expect(rows[0].a_pos_count).toBe(4)
    expect(rows[0].a_notional).toBe(1234.5)
    expect(rows[0].a_long_notional).toBe(1000)
    expect(rows[0].a_margin_pct).toBe(66)
    expect(rows[0].a_age_ms).toBe(0)
  })

  // ~70% of live minute buckets contain only one of the four traders, so blanking
  // book fields on carried rows would leave most tooltip entries empty. They are
  // carried, and the age of the reading is carried with them.
  it('carries book fields onto later rows and reports their age', () => {
    const rows = mergeTraderHistories(traders, [
      [
        {
          timestamp: iso(0),
          total_equity: 100,
          total_pnl_pct: 1,
          position_count: 4,
          position_notional: 1234.5,
        },
      ],
      [
        { timestamp: iso(0), total_equity: 50, total_pnl_pct: 0 },
        { timestamp: iso(5), total_equity: 55, total_pnl_pct: 1 },
      ],
    ])
    expect(rows).toHaveLength(2)
    expect(rows[1].a_pos_count).toBe(4)
    expect(rows[1].a_notional).toBe(1234.5)
    expect(rows[1].a_stale).toBe(true)
    expect(rows[1].a_age_ms).toBe(5 * 60_000)
    expect(rows[1].b_stale).toBe(false)
    expect(rows[1].b_age_ms).toBe(0)
  })

  it('survives malformed and empty input without throwing', () => {
    expect(mergeTraderHistories(traders, [[], []])).toEqual([])
    const rows = mergeTraderHistories(traders, [
      [
        { timestamp: 'not-a-date', total_equity: 1, total_pnl_pct: 1 },
        { timestamp: iso(0), total_equity: 100, total_pnl_pct: 1 },
      ],
      [],
    ])
    expect(rows).toHaveLength(1)
    expect(rows.every((r) => Number.isFinite(r.ts))).toBe(true)
  })

  it('treats a missing pnl as zero rather than emitting NaN into the chart', () => {
    const rows = mergeTraderHistories(traders, [
      [{ timestamp: iso(0), total_equity: 100 } as any],
      [],
    ])
    expect(rows[0].a_pnl_pct).toBe(0)
  })
})
