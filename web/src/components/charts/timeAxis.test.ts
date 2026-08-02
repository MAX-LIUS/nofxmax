import { describe, it, expect } from 'vitest'
import {
  buildTimeTicks,
  formatAxisTick,
  clampDomain,
  panDomain,
  zoomDomain,
  MIN_SPAN_MS,
  type Domain,
} from './timeAxis'

const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const T0 = new Date('2026-06-19T00:00:00').getTime()

describe('formatAxisTick', () => {
  // The reported problem: the label format was picked from the selected range
  // button, so it did not follow the span actually on screen.
  it('shows time-of-day for intraday spans', () => {
    expect(formatAxisTick(T0 + 5 * HOUR + 30 * MIN, 4 * HOUR)).toMatch(
      /^\d{2}:\d{2}$/
    )
  })

  it('shows date and time for multi-hour to two-day spans', () => {
    expect(formatAxisTick(T0, 36 * HOUR)).toMatch(/^\d+\/\d+ \d{2}:\d{2}$/)
  })

  it('shows date only for multi-week spans', () => {
    expect(formatAxisTick(T0, 45 * DAY)).toMatch(/^\d+\/\d+$/)
  })

  it('changes format when the visible span changes, same instant', () => {
    const zoomedIn = formatAxisTick(T0 + 3 * HOUR, 2 * HOUR)
    const zoomedOut = formatAxisTick(T0 + 3 * HOUR, 45 * DAY)
    expect(zoomedIn).not.toEqual(zoomedOut)
  })
})

describe('buildTimeTicks', () => {
  it('produces a sane number of ticks across very different spans', () => {
    for (const span of [HOUR, 6 * HOUR, DAY, 7 * DAY, 45 * DAY, 400 * DAY]) {
      const ticks = buildTimeTicks(T0, T0 + span)
      expect(ticks.length).toBeGreaterThan(1)
      expect(ticks.length).toBeLessThanOrEqual(24)
    }
  })

  it('keeps every tick inside the domain', () => {
    const from = T0 + 137 * MIN
    const to = from + 3 * DAY
    for (const tk of buildTimeTicks(from, to)) {
      expect(tk).toBeGreaterThanOrEqual(from)
      expect(tk).toBeLessThanOrEqual(to)
    }
  })

  it('places daily ticks on local midnight, not on an arbitrary offset', () => {
    const ticks = buildTimeTicks(T0 + 137 * MIN, T0 + 8 * DAY)
    const daily = ticks.filter((tk) => {
      const d = new Date(tk)
      return d.getHours() === 0 && d.getMinutes() === 0
    })
    expect(daily.length).toBeGreaterThan(0)
  })

  it('returns empty for a degenerate domain instead of looping', () => {
    expect(buildTimeTicks(T0, T0)).toEqual([])
    expect(buildTimeTicks(T0, T0 - DAY)).toEqual([])
    expect(buildTimeTicks(NaN, T0)).toEqual([])
  })
})

describe('clampDomain', () => {
  const bounds: Domain = [T0, T0 + 45 * DAY]

  it('holds the view inside the data bounds', () => {
    const [from, to] = clampDomain([T0 - 10 * DAY, T0 + 5 * DAY], bounds)
    expect(from).toBe(bounds[0])
    expect(to - from).toBe(15 * DAY)
  })

  it('shifts rather than shrinks when pushed past the right edge', () => {
    const [from, to] = clampDomain(
      [bounds[1] - 2 * DAY, bounds[1] + 10 * DAY],
      bounds
    )
    expect(to).toBe(bounds[1])
    expect(to - from).toBe(12 * DAY)
  })

  it('refuses to zoom below the minimum span', () => {
    const [from, to] = clampDomain([T0 + DAY, T0 + DAY + 1000], bounds)
    expect(to - from).toBe(MIN_SPAN_MS)
  })

  it('never widens beyond the full data span', () => {
    const [from, to] = clampDomain([T0 - 100 * DAY, T0 + 200 * DAY], bounds)
    expect(from).toBe(bounds[0])
    expect(to).toBe(bounds[1])
  })
})

describe('zoomDomain', () => {
  const bounds: Domain = [T0, T0 + 45 * DAY]

  it('narrows on zoom in and widens on zoom out', () => {
    const start: Domain = [T0 + 10 * DAY, T0 + 20 * DAY]
    const inSpan = zoomDomain(start, bounds, 0.6, 0.5)
    const outSpan = zoomDomain(start, bounds, 1.6, 0.5)
    expect(inSpan[1] - inSpan[0]).toBeLessThan(start[1] - start[0])
    expect(outSpan[1] - outSpan[0]).toBeGreaterThan(start[1] - start[0])
  })

  // Cursor anchoring is what makes wheel zoom feel like magnifying the point under
  // the pointer instead of the middle of the chart.
  it('keeps the instant under the anchor fixed', () => {
    const start: Domain = [T0, T0 + 10 * DAY]
    const frac = 0.25
    const anchorBefore = start[0] + (start[1] - start[0]) * frac
    const zoomed = zoomDomain(start, bounds, 0.5, frac)
    const anchorAfter = zoomed[0] + (zoomed[1] - zoomed[0]) * frac
    expect(Math.abs(anchorAfter - anchorBefore)).toBeLessThan(1000)
  })

  it('clamps an out-of-range anchor instead of producing a bad domain', () => {
    const start: Domain = [T0, T0 + 10 * DAY]
    for (const frac of [-3, 5, NaN]) {
      const [from, to] = zoomDomain(start, bounds, 0.5, frac as number)
      expect(Number.isFinite(from)).toBe(true)
      expect(to).toBeGreaterThan(from)
    }
  })
})

describe('panDomain', () => {
  const bounds: Domain = [T0, T0 + 45 * DAY]

  it('moves later for positive fractions and earlier for negative', () => {
    const start: Domain = [T0 + 10 * DAY, T0 + 20 * DAY]
    expect(panDomain(start, bounds, 0.5)[0]).toBeGreaterThan(start[0])
    expect(panDomain(start, bounds, -0.5)[0]).toBeLessThan(start[0])
  })

  it('preserves the visible span while panning', () => {
    const start: Domain = [T0 + 10 * DAY, T0 + 20 * DAY]
    const span = start[1] - start[0]
    for (const frac of [0.25, -0.25, 3, -3]) {
      const d = panDomain(start, bounds, frac)
      expect(d[1] - d[0]).toBe(span)
    }
  })

  it('stops at the data edges rather than panning into empty space', () => {
    const start: Domain = [T0 + 40 * DAY, T0 + 45 * DAY]
    expect(panDomain(start, bounds, 10)[1]).toBe(bounds[1])
    expect(panDomain([T0, T0 + 5 * DAY], bounds, -10)[0]).toBe(bounds[0])
  })
})
