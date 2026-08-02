/**
 * Merges several traders' equity histories into the row shape the comparison chart
 * draws. Extracted from the component so the alignment and carry-forward rules are
 * unit-testable.
 */

export interface MergeTraderRef {
  trader_id: string
}

export interface MergedRow {
  ts: number
  [key: string]: any
}

interface Sample {
  pnl_pct: number
  equity: number
  positionCount?: number
  positionCountRecon?: number
  notional?: number
  longNotional?: number
  shortNotional?: number
  marginUsedPct?: number
  originalMs: number
}

/**
 * Buckets samples to the minute so independently-timed traders share rows, then
 * carries each trader's last known value forward across minutes where only other
 * traders reported.
 *
 * Two rules worth stating because they are easy to get wrong in opposite directions:
 *
 *   - Carrying forward is required, otherwise every line becomes dashed wherever a
 *     trader happened not to sample. Carried rows are flagged `<id>_stale` so the
 *     tooltip can say the book reading is not fresh.
 *   - Carrying BACKWARD is not done. A trader that started later must be absent from
 *     earlier rows, or the chart would show it competing before it existed.
 *
 * The row key is numeric epoch ms (`ts`), which is what allows the x-axis to be a
 * real time scale. With a category axis of formatted labels, a multi-hour gap in the
 * data rendered as a single sample-width step and hid the outage.
 */
export function mergeTraderHistories(
  traders: MergeTraderRef[],
  histories: any[][]
): MergedRow[] {
  const byMinute = new Map<number, Map<string, Sample>>()

  histories.forEach((history, index) => {
    const trader = traders[index]
    if (!trader || !history) return

    history.forEach((point: any) => {
      const ms = new Date(point?.timestamp).getTime()
      if (!isFinite(ms)) return
      const bucket = Math.floor(ms / 60_000) * 60_000

      if (!byMinute.has(bucket)) byMinute.set(bucket, new Map())
      const slot = byMinute.get(bucket)!
      const existing = slot.get(trader.trader_id)
      if (!existing || ms > existing.originalMs) {
        slot.set(trader.trader_id, {
          pnl_pct: point.total_pnl_pct || 0,
          equity: point.total_equity,
          positionCount: point.position_count,
          positionCountRecon: point.position_count_recon,
          notional: point.position_notional,
          longNotional: point.long_notional,
          shortNotional: point.short_notional,
          marginUsedPct: point.margin_used_pct,
          originalMs: ms,
        })
      }
    })
  })

  const sortedBuckets = Array.from(byMinute.keys()).sort((a, b) => a - b)
  const lastKnown = new Map<string, Sample>()

  return sortedBuckets.map((bucket) => {
    const slot = byMinute.get(bucket)!
    const entry: MergedRow = { ts: bucket }

    traders.forEach((trader) => {
      const id = trader.trader_id
      const fresh = slot.get(id)
      const use = fresh || lastKnown.get(id)
      if (fresh) lastKnown.set(id, fresh)
      if (!use) return
      entry[`${id}_pnl_pct`] = use.pnl_pct
      entry[`${id}_equity`] = use.equity
      entry[`${id}_pos_count`] = use.positionCount
      entry[`${id}_pos_count_recon`] = use.positionCountRecon
      entry[`${id}_notional`] = use.notional
      entry[`${id}_long_notional`] = use.longNotional
      entry[`${id}_short_notional`] = use.shortNotional
      entry[`${id}_margin_pct`] = use.marginUsedPct
      entry[`${id}_stale`] = !fresh
      // Age of the reading being shown, in ms. Book fields ARE carried forward
      // because the traders almost never sample in the same minute - on live data
      // ~70% of minute buckets contain only one trader - so suppressing them on
      // carried rows would leave three of four tooltip entries blank nearly always.
      // Carrying them means the tooltip must say how old the reading is rather than
      // implying it was taken at the hovered instant.
      entry[`${id}_age_ms`] = bucket - use.originalMs
    })

    return entry
  })
}
