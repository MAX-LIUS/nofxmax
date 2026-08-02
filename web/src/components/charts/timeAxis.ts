/**
 * Time-axis and zoom/pan helpers for the competition comparison chart.
 *
 * These live outside the component so the axis behaviour is unit-testable. The
 * chart previously used a CATEGORY x-axis of pre-formatted label strings, which has
 * two consequences that looked like separate bugs:
 *
 *   1. Sample spacing became uniform regardless of real elapsed time, so a gap in
 *      the data (restart, outage) rendered as if no time had passed.
 *   2. The label format was chosen from the SELECTED period rather than from the
 *      span actually on screen, so after zooming the labels no longer described
 *      what was visible.
 *
 * Everything here works on epoch milliseconds and derives formatting from the
 * visible span, so labels follow the view instead of the button.
 */

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** Left inset of the plot area: chart margin + YAxis width. */
export const PLOT_LEFT_PX = 60
/** Right inset of the plot area: chart margin. */
export const PLOT_RIGHT_PX = 20

/** Never allow zooming closer than this, or the view can collapse to a point. */
export const MIN_SPAN_MS = 5 * MINUTE

export type Domain = [number, number]

/**
 * Formats one axis tick. The format is chosen from the VISIBLE span, not from the
 * selected range button: zoomed into two hours you want HH:mm, looking at six
 * weeks you want M/D.
 */
export function formatAxisTick(ms: number, spanMs: number): string {
  const d = new Date(ms)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  const md = `${d.getMonth() + 1}/${d.getDate()}`

  if (spanMs <= 6 * HOUR) return `${hh}:${mm}`
  if (spanMs <= 2 * DAY) return `${md} ${hh}:${mm}`
  if (spanMs <= 120 * DAY) return md
  return `${d.getFullYear()}/${d.getMonth() + 1}`
}

/**
 * Chooses tick positions on round time boundaries inside [from, to].
 *
 * Ticks are placed on human-meaningful instants (start of hour, start of day)
 * rather than at every Nth sample. With a numeric axis and irregular sampling,
 * per-sample ticks drift to arbitrary times like 03:47, and their spacing changes
 * as you pan even though the view width has not.
 */
export function buildTimeTicks(from: number, to: number, target = 8): number[] {
  if (!isFinite(from) || !isFinite(to) || to <= from) return []
  const span = to - from
  const steps = [
    MINUTE,
    2 * MINUTE,
    5 * MINUTE,
    10 * MINUTE,
    15 * MINUTE,
    30 * MINUTE,
    HOUR,
    2 * HOUR,
    3 * HOUR,
    6 * HOUR,
    12 * HOUR,
    DAY,
    2 * DAY,
    3 * DAY,
    7 * DAY,
    14 * DAY,
    30 * DAY,
    90 * DAY,
    365 * DAY,
  ]
  const ideal = span / target
  let step = steps[steps.length - 1]
  for (const s of steps) {
    if (s >= ideal) {
      step = s
      break
    }
  }

  // Align to local-time boundaries for steps of an hour or more, so a daily tick
  // lands on local midnight rather than on UTC midnight shifted by the offset.
  const ticks: number[] = []
  const offset = new Date(from).getTimezoneOffset() * MINUTE
  let first = Math.ceil((from - offset) / step) * step + offset
  for (let tk = first; tk <= to; tk += step) {
    ticks.push(tk)
    if (ticks.length > 200) break
  }
  return ticks
}

/** Keeps a domain inside the data bounds and above the minimum span. */
export function clampDomain(domain: Domain, bounds: Domain): Domain {
  const [lo, hi] = bounds
  if (!isFinite(lo) || !isFinite(hi) || hi <= lo) return bounds
  let [from, to] = domain
  if (!isFinite(from) || !isFinite(to)) return bounds
  const fullSpan = hi - lo

  const span = Math.max(
    Math.min(to - from, fullSpan),
    Math.min(MIN_SPAN_MS, fullSpan)
  )
  // Apply the corrected span unconditionally. An earlier version only wrote it back
  // when the view had hit a data edge, so a zoom to well inside the bounds could
  // still collapse below MIN_SPAN_MS: the limit existed but did nothing in the case
  // it was written for.
  to = from + span
  if (from < lo) {
    from = lo
    to = from + span
  }
  if (to > hi) {
    to = hi
    from = to - span
  }
  if (from < lo) from = lo
  return [from, to]
}

/**
 * Zooms around an anchor expressed as a fraction of the plot width (0 = left edge,
 * 1 = right edge). Anchoring on the cursor is what makes wheel zoom feel like it
 * is magnifying the point under the pointer rather than the middle of the chart.
 */
export function zoomDomain(
  domain: Domain,
  bounds: Domain,
  factor: number,
  anchorFrac: number
): Domain {
  const [from, to] = domain
  const span = to - from
  if (span <= 0) return domain
  // Math.min/max propagate NaN rather than clamping it, and a NaN anchor produces a
  // NaN domain that blanks the whole chart. The pointer-fraction helper can yield
  // NaN before layout has settled, so this needs an explicit test.
  const frac = isFinite(anchorFrac) ? Math.min(Math.max(anchorFrac, 0), 1) : 0.5
  const anchorMs = from + span * frac
  const newSpan = span * factor
  return clampDomain(
    [anchorMs - newSpan * frac, anchorMs + newSpan * (1 - frac)],
    bounds
  )
}

/**
 * Pans by a fraction of the visible span. Positive moves the view later in time.
 */
export function panDomain(
  domain: Domain,
  bounds: Domain,
  fracOfSpan: number
): Domain {
  const [from, to] = domain
  const delta = (to - from) * fracOfSpan
  return clampDomain([from + delta, to + delta], bounds)
}
