import { useEffect, useMemo, useState } from 'react'
import { api } from '../../lib/api'
import { formatPrice, formatQuantity } from '../../utils/format'
import type { Language } from '../../i18n/translations'
import type { OpenOrder, Position } from '../../types'

interface PositionProtectionPanelProps {
  traderId?: string
  positions?: Position[]
  language: Language
  exchange?: string
  onSymbolClick?: (symbol: string) => void
}

type ProtectionRow = {
  zone: string
  price: number
  sortPrice: number
  deltaPct: number
  ratioPct: number
  callbackPct?: number
  status: string
  statusCls: string
  anchor?: string
  isCurrentPrice?: boolean
  isTrailing?: boolean
}

function normalizeSide(side?: string): string {
  return String(side || '').toUpperCase()
}

function formatPct(value: number | undefined | null, digits = 2): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  const sign = value > 0 ? '+' : ''
  return `${sign}${value.toFixed(digits)}%`
}

function classifyZone(
  order: OpenOrder,
  entryPrice: number,
  side: string
): string {
  const id = String(order.client_order_id || '').toLowerCase()
  const type = String(order.type || '').toUpperCase()
  const triggerPrice = order.stop_price || order.price || 0

  // Trailing = DD
  if (
    type.includes('TRAILING') ||
    id.includes('drawdown') ||
    id.includes('trailing')
  ) {
    return 'DD'
  }
  // Break-even tagged
  if (id.includes('break_even') || id.includes('breakeven')) {
    return 'BE'
  }
  // Positive offset from entry = BE (profit protection stop)
  if (triggerPrice > 0 && entryPrice > 0) {
    const isLong = side === 'LONG'
    const isProfitSide = isLong
      ? triggerPrice > entryPrice
      : triggerPrice < entryPrice
    if (isProfitSide && (type.includes('STOP') || id.includes('sl'))) {
      return 'BE'
    }
  }
  return 'Ladder'
}

function getAnchorFromPlanned(
  triggerPrice: number,
  plannedLadder:
    | {
        stop_loss?: Array<{
          price: number
          anchor_source?: string
          anchor_timeframe?: string
          anchor_price?: number
        }>
        take_profit?: Array<{
          price: number
          anchor_source?: string
          anchor_timeframe?: string
          anchor_price?: number
        }>
      }
    | undefined
): string | undefined {
  if (!plannedLadder || triggerPrice <= 0) return undefined
  const allOrders = [
    ...(plannedLadder.stop_loss || []),
    ...(plannedLadder.take_profit || []),
  ]
  const tolerance = triggerPrice * 0.001
  const match = allOrders.find(
    (p) => Math.abs(p.price - triggerPrice) < tolerance
  )
  if (!match?.anchor_source) return undefined
  const parts = [
    match.anchor_timeframe,
    match.anchor_source,
    match.anchor_price ? formatPrice(match.anchor_price) : '',
  ].filter(Boolean)
  return parts.join(' ')
}

function buildProtectionRows(
  position: Position,
  orders: OpenOrder[],
  language: Language
): ProtectionRow[] {
  const entryPrice = position.entry_price || 0
  const side = normalizeSide(position.side)
  const dirMul = side === 'LONG' ? 1 : -1
  const markPrice = position.mark_price || 0
  const entryQty = position.entry_quantity || position.quantity || 0
  const rows: ProtectionRow[] = []

  const rt = position.protection_runtime
  const plannedLadder = rt?.planned_ladder_orders as
    | {
        stop_loss?: Array<{
          price: number
          anchor_source?: string
          anchor_timeframe?: string
          anchor_price?: number
        }>
        take_profit?: Array<{
          price: number
          anchor_source?: string
          anchor_timeframe?: string
          anchor_price?: number
        }>
      }
    | undefined

  // Track BE indices for numbering
  let beIndex = 0

  for (const order of orders) {
    const type = String(order.type || '').toUpperCase()
    const isTrailing = type.includes('TRAILING')
    const triggerPrice = order.stop_price || order.price || 0
    if (triggerPrice <= 0 && !isTrailing) continue

    const rawDelta =
      entryPrice > 0 ? ((triggerPrice - entryPrice) / entryPrice) * 100 : 0
    const deltaPct = rawDelta * dirMul
    const ratioPct =
      entryQty > 0 && order.quantity > 0 ? (order.quantity / entryQty) * 100 : 0

    let zone = classifyZone(order, entryPrice, side)

    // Number BE zones
    if (zone === 'BE') {
      beIndex++
      zone = `BE-${beIndex}`
    }

    // DD trailing: show activation status
    let status: string
    let statusCls: string
    let callbackPct: number | undefined

    if (isTrailing) {
      zone = 'DD'
      callbackPct = order.callback_rate ? order.callback_rate * 100 : undefined
      if (order.activation_status === 'activated') {
        status = language === 'zh' ? '已激活' : 'Active'
        statusCls = 'text-emerald-300'
      } else {
        status = language === 'zh' ? '未激活' : 'Pending'
        statusCls = 'text-amber-300'
      }
    } else {
      status = language === 'zh' ? '委托中' : 'Live'
      statusCls = 'text-emerald-300'
    }

    const anchor = getAnchorFromPlanned(triggerPrice, plannedLadder)

    // For trailing orders with no trigger price yet, use markPrice for sorting
    const sortPrice = triggerPrice > 0 ? triggerPrice : markPrice

    rows.push({
      zone,
      price: triggerPrice,
      sortPrice,
      deltaPct,
      ratioPct,
      callbackPct,
      status,
      statusCls,
      anchor,
      isTrailing,
    })
  }

  // Insert current price marker
  if (markPrice > 0) {
    const markDelta =
      entryPrice > 0
        ? ((markPrice - entryPrice) / entryPrice) * 100 * dirMul
        : 0
    rows.push({
      zone: '',
      price: markPrice,
      sortPrice: markPrice,
      deltaPct: markDelta,
      ratioPct: 0,
      status: '',
      statusCls: '',
      isCurrentPrice: true,
    })
  }

  // Sort by price: for LONG, highest price first (profit protection on top, SL on bottom)
  // For SHORT, lowest price first
  if (side === 'LONG') {
    rows.sort((a, b) => b.sortPrice - a.sortPrice)
  } else {
    rows.sort((a, b) => a.sortPrice - b.sortPrice)
  }

  return rows
}

export function PositionProtectionPanel({
  traderId,
  positions,
  language,
  onSymbolClick,
}: PositionProtectionPanelProps) {
  const [ordersBySymbol, setOrdersBySymbol] = useState<
    Record<string, OpenOrder[]>
  >({})
  const [loading, setLoading] = useState(false)

  const symbolKeys = useMemo(() => {
    const keys = new Set<string>()
    for (const pos of positions || [])
      keys.add(String(pos.symbol || '').toUpperCase())
    return [...keys]
  }, [positions])

  useEffect(() => {
    let cancelled = false
    async function load() {
      if (!traderId || !positions || positions.length === 0) {
        setOrdersBySymbol({})
        return
      }
      setLoading(true)
      try {
        const entries = await Promise.all(
          symbolKeys.map(async (symbol) => {
            const data = await api.getOpenOrders(traderId, symbol)
            return [symbol, Array.isArray(data) ? data : []] as const
          })
        )
        if (!cancelled) setOrdersBySymbol(Object.fromEntries(entries))
      } catch {
        // silent
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    const timer = window.setInterval(load, 60000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [traderId, positions, symbolKeys])

  if (!positions || positions.length === 0) {
    return (
      <div className="nofx-glass p-6 relative overflow-hidden">
        <h2 className="text-lg font-bold text-nofx-text-main uppercase tracking-wide flex items-center gap-2 mb-4">
          <span className="text-purple-400">◈</span>
          {language === 'zh' ? '持仓保护' : 'Position Protection'}
        </h2>
        <div className="text-xs text-nofx-text-muted">
          {language === 'zh' ? '当前没有持仓。' : 'No open positions.'}
        </div>
      </div>
    )
  }

  return (
    <div className="nofx-glass p-6 relative overflow-hidden">
      <h2 className="text-lg font-bold text-nofx-text-main uppercase tracking-wide flex items-center gap-2 mb-5">
        <span className="text-purple-400">◈</span>
        {language === 'zh' ? '持仓保护' : 'Position Protection'}
        {loading && (
          <span className="text-[10px] text-nofx-text-muted ml-2">⟳</span>
        )}
      </h2>

      <div className="space-y-4">
        {positions.map((position, index) => {
          const symbol = String(position.symbol || '').toUpperCase()
          const side = normalizeSide(position.side)
          const entryPrice = position.entry_price || 0
          const markPrice = position.mark_price || 0
          const entryQty = position.entry_quantity || position.quantity || 0
          const rt = position.protection_runtime
          const currentPnlPct = Number(
            rt?.current_pnl_pct ?? position.unrealized_pnl_pct ?? 0
          )
          const peakPnlPct = Number(rt?.drawdown_peak_pnl_pct ?? currentPnlPct)

          const symbolOrders = ordersBySymbol[symbol] || []
          const filteredOrders = symbolOrders.filter((o) => {
            const s = normalizeSide(o.position_side)
            return !s || s === side
          })

          const rows = buildProtectionRows(position, filteredOrders, language)
          const pnlColor =
            currentPnlPct >= 0 ? 'text-nofx-green' : 'text-nofx-red'
          const sideCls =
            side === 'LONG'
              ? 'bg-nofx-green/15 text-nofx-green border-nofx-green/30'
              : 'bg-nofx-red/15 text-nofx-red border-nofx-red/30'

          return (
            <div
              key={`${symbol}-${side}-${index}`}
              className="rounded-lg border border-white/10 bg-black/20 p-4 space-y-3"
            >
              {/* Header */}
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => onSymbolClick?.(symbol)}
                    className="text-base font-bold text-nofx-text-main hover:text-cyan-300 transition-colors"
                  >
                    {symbol}
                  </button>
                  <span
                    className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-semibold ${sideCls}`}
                  >
                    {side}
                  </span>
                  {position.leverage && (
                    <span className="text-xs font-mono text-nofx-text-muted">
                      {position.leverage}x
                    </span>
                  )}
                </div>
                <span className={`text-base font-bold font-mono ${pnlColor}`}>
                  {formatPct(currentPnlPct)}
                </span>
              </div>

              {/* Info line: Mark / Entry / Qty / Peak */}
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-nofx-text-muted">
                {markPrice > 0 && (
                  <span>
                    {language === 'zh' ? '现价' : 'Mark'}{' '}
                    <span className="font-mono text-nofx-text-main">
                      {formatPrice(markPrice)}
                    </span>
                  </span>
                )}
                <span>
                  {language === 'zh' ? '入场' : 'Entry'}{' '}
                  <span className="font-mono text-nofx-text-main">
                    {formatPrice(entryPrice)}
                  </span>
                </span>
                <span>
                  Qty{' '}
                  <span className="font-mono text-nofx-text-main">
                    {formatQuantity(entryQty)}
                  </span>
                </span>
                <span>
                  Peak{' '}
                  <span className="font-mono text-nofx-text-main">
                    {formatPct(peakPnlPct)}
                  </span>
                </span>
              </div>

              {/* Protection price ladder */}
              {rows.length > 0 ? (
                <div className="overflow-x-auto">
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="text-nofx-text-muted border-b border-white/10">
                        <th className="text-left py-1 pr-2 font-medium">
                          {language === 'zh' ? '区域' : 'Zone'}
                        </th>
                        <th className="text-right py-1 px-2 font-medium">
                          {language === 'zh' ? '价格' : 'Price'}
                        </th>
                        <th className="text-right py-1 px-2 font-medium">Δ%</th>
                        <th className="text-right py-1 px-2 font-medium">%</th>
                        <th className="text-center py-1 px-2 font-medium">
                          {language === 'zh' ? '状态' : 'Status'}
                        </th>
                        <th className="text-left py-1 pl-2 font-medium">
                          {language === 'zh' ? '锚点' : 'Anchor'}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((row, ri) => {
                        if (row.isCurrentPrice) {
                          return (
                            <tr
                              key={`price-line-${ri}`}
                              className="border-y border-cyan-500/40"
                            >
                              <td colSpan={6} className="py-0.5">
                                <div className="flex items-center gap-2">
                                  <div className="flex-1 h-px bg-cyan-500/40" />
                                  <span className="text-[10px] font-mono text-cyan-300 whitespace-nowrap">
                                    ▸ {formatPrice(row.price)} (
                                    {formatPct(row.deltaPct)})
                                  </span>
                                  <div className="flex-1 h-px bg-cyan-500/40" />
                                </div>
                              </td>
                            </tr>
                          )
                        }

                        const deltaColor =
                          row.deltaPct > 0
                            ? 'text-nofx-green'
                            : row.deltaPct < 0
                              ? 'text-nofx-red'
                              : 'text-nofx-text-muted'
                        const zoneCls = row.zone.startsWith('DD')
                          ? 'text-purple-300'
                          : row.zone.startsWith('BE')
                            ? 'text-amber-300'
                            : 'text-blue-300'

                        return (
                          <tr
                            key={`row-${ri}`}
                            className="border-b border-white/5 hover:bg-white/5"
                          >
                            <td className="py-1 pr-2">
                              <span className={`font-medium ${zoneCls}`}>
                                {row.zone}
                              </span>
                            </td>
                            <td className="py-1 px-2 text-right font-mono text-nofx-text-main">
                              {row.price > 0 ? (
                                formatPrice(row.price)
                              ) : row.isTrailing ? (
                                <span className="text-nofx-text-muted text-[10px]">
                                  {language === 'zh' ? '跟踪中' : 'Tracking'}
                                </span>
                              ) : (
                                '—'
                              )}
                              {row.callbackPct ? (
                                <span className="text-nofx-text-muted ml-1">
                                  cb{row.callbackPct.toFixed(1)}%
                                </span>
                              ) : null}
                            </td>
                            <td
                              className={`py-1 px-2 text-right font-mono ${deltaColor}`}
                            >
                              {row.price > 0 ? formatPct(row.deltaPct) : '—'}
                            </td>
                            <td className="py-1 px-2 text-right font-mono text-nofx-text-main">
                              {row.ratioPct > 0
                                ? `${row.ratioPct.toFixed(0)}%`
                                : '—'}
                            </td>
                            <td className="py-1 px-2 text-center">
                              <span
                                className={`inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium border ${row.statusCls} ${
                                  row.statusCls.includes('emerald')
                                    ? 'bg-emerald-500/10 border-emerald-500/20'
                                    : row.statusCls.includes('amber')
                                      ? 'bg-amber-500/10 border-amber-500/20'
                                      : 'bg-white/5 border-white/10'
                                }`}
                              >
                                {row.status}
                              </span>
                            </td>
                            <td
                              className="py-1 pl-2 text-nofx-text-muted truncate max-w-[150px]"
                              title={row.anchor || ''}
                            >
                              {row.anchor || '—'}
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              ) : (
                <div className="text-xs text-nofx-text-muted border border-white/10 rounded px-3 py-2">
                  {language === 'zh' ? '无保护委托' : 'No protection orders'}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
