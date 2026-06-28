import { memo, useEffect, useMemo, useState } from 'react'
import { api } from '../../lib/api'
import { formatPrice } from '../../utils/format'
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
  atrMult: number
  ratioPct: number
  usdValue: number
  status: string
  statusCls: string
  detail?: string
  isCurrentPrice?: boolean
}

interface ScheduledTier {
  index: number
  min_profit_pct: number
  max_drawdown_pct: number
  close_ratio_pct: number
  callback_rate: number
  activation_price: number
  planned_quantity: number
  is_satisfied: boolean
  is_triggered: boolean
  stage_name: string
  reason_anchor: string
  execution_mode: string
}

function normalizeSide(side?: string): string {
  return String(side || '').toUpperCase()
}

function formatPct(value: number | undefined | null, digits = 2): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  const sign = value > 0 ? '+' : ''
  return `${sign}${value.toFixed(digits)}%`
}

function formatUsdx(qty: number, price: number): string {
  const val = qty * price
  if (val >= 1000) return `$${(val / 1000).toFixed(1)}k`
  return `$${val.toFixed(1)}`
}

function formatUsd(value: number | undefined | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  const sign = value > 0 ? '+' : value < 0 ? '-' : ''
  const abs = Math.abs(value)
  return `${sign}$${abs.toFixed(2)}`
}

// formatHoldTime renders ms-since-entry as a compact duration (e.g. "2h", "1d 4h", "45m").
function formatHoldTime(entryTimeMs: number | undefined): string {
  if (!entryTimeMs || entryTimeMs <= 0) return '—'
  const mins = Math.floor((Date.now() - entryTimeMs) / 60000)
  if (mins < 1) return '<1m'
  if (mins < 60) return `${mins}m`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) {
    const m = mins % 60
    return m > 0 ? `${hrs}h ${m}m` : `${hrs}h`
  }
  const days = Math.floor(hrs / 24)
  const h = hrs % 24
  return h > 0 ? `${days}d ${h}h` : `${days}d`
}

function classifyZone(
  order: OpenOrder,
  entryPrice: number,
  side: string
): string {
  const id = String(order.client_order_id || '').toLowerCase()
  const type = String(order.type || '').toUpperCase()
  const triggerPrice = order.stop_price || order.price || 0

  if (
    type.includes('TRAILING') ||
    id.includes('drawdown') ||
    id.includes('trailing')
  ) {
    return 'DD'
  }
  if (id.includes('break_even') || id.includes('breakeven')) {
    return 'BE'
  }
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
  const peakPnlPct = Number(rt?.drawdown_peak_pnl_pct ?? 0)
  const scheduledTiers = (rt?.scheduled_tiers || []) as ScheduledTier[]
  const atrAtEntry = Number(rt?.atr_at_entry ?? 0)
  const atrEntryPct =
    entryPrice > 0 && atrAtEntry > 0 ? (atrAtEntry / entryPrice) * 100 : 0
  // toAtrMult converts a delta% distance into ATR multiples using entry-frozen ATR.
  const toAtrMult = (deltaPct: number) =>
    atrEntryPct > 0 ? deltaPct / atrEntryPct : 0

  // Build DD rows from scheduled_tiers (authoritative source)
  for (const tier of scheduledTiers) {
    const tierIdx = tier.index || 0
    const zone = `DD-${tierIdx}`
    const callbackRate = tier.callback_rate || 0

    // Calculate trigger price from peak and callback when tier is active
    // If peak is 0 (new position), show the activation price instead
    let triggerPrice = 0
    if (
      peakPnlPct > 0 &&
      entryPrice > 0 &&
      callbackRate > 0 &&
      tier.is_satisfied
    ) {
      const peakPrice =
        side === 'LONG'
          ? entryPrice * (1 + peakPnlPct / 100)
          : entryPrice * (1 - peakPnlPct / 100)
      triggerPrice =
        side === 'LONG'
          ? peakPrice * (1 - callbackRate)
          : peakPrice * (1 + callbackRate)
    } else if (tier.activation_price > 0) {
      triggerPrice = tier.activation_price
    }

    const rawDelta =
      triggerPrice > 0 && entryPrice > 0
        ? ((triggerPrice - entryPrice) / entryPrice) * 100
        : 0
    const deltaPct = rawDelta * dirMul
    const ratioPct = tier.close_ratio_pct || 0
    const tierQty = tier.planned_quantity || (entryQty * ratioPct) / 100
    const usdValue =
      tierQty > 0 && triggerPrice > 0 ? tierQty * triggerPrice : 0

    let status: string
    let statusCls: string
    if (tier.is_triggered) {
      status = language === 'zh' ? '已触发' : 'Triggered'
      statusCls = 'text-nofx-red'
    } else if (tier.is_satisfied) {
      status = language === 'zh' ? '已激活' : 'Active'
      statusCls = 'text-emerald-300'
    } else {
      status = language === 'zh' ? '待满足' : 'Waiting'
      statusCls = 'text-nofx-text-muted'
    }

    let detail: string
    if (tier.is_satisfied && peakPnlPct > 0) {
      detail = `peak${formatPct(peakPnlPct, 1)} cb${(callbackRate * 100).toFixed(1)}%`
    } else {
      detail = `min${tier.min_profit_pct.toFixed(1)}% dd${tier.max_drawdown_pct.toFixed(0)}%`
    }

    rows.push({
      zone,
      price: triggerPrice,
      sortPrice: triggerPrice > 0 ? triggerPrice : markPrice,
      deltaPct,
      atrMult: toAtrMult(deltaPct),
      ratioPct,
      usdValue,
      status,
      statusCls,
      detail,
    })
  }

  // Build rows from exchange orders (BE + Ladder only, skip trailing since DD comes from tiers)
  let beIndex = 0
  for (const order of orders) {
    const type = String(order.type || '').toUpperCase()
    if (type.includes('TRAILING')) continue

    const triggerPrice = order.stop_price || order.price || 0
    if (triggerPrice <= 0) continue

    const rawDelta =
      entryPrice > 0 ? ((triggerPrice - entryPrice) / entryPrice) * 100 : 0
    const deltaPct = rawDelta * dirMul
    const ratioPct =
      entryQty > 0 && order.quantity > 0 ? (order.quantity / entryQty) * 100 : 0

    let zone = classifyZone(order, entryPrice, side)
    if (zone === 'DD') continue

    if (zone === 'BE') {
      beIndex++
      zone = `BE-${beIndex}`
    }

    const status = language === 'zh' ? '委托中' : 'Live'
    const statusCls = 'text-emerald-300'

    rows.push({
      zone,
      price: triggerPrice,
      sortPrice: triggerPrice,
      deltaPct,
      atrMult: toAtrMult(deltaPct),
      ratioPct,
      usdValue: order.quantity > 0 ? order.quantity * triggerPrice : 0,
      status,
      statusCls,
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
      atrMult: toAtrMult(markDelta),
      ratioPct: 0,
      usdValue: 0,
      status: '',
      statusCls: '',
      isCurrentPrice: true,
    })
  }

  // Sort: for LONG highest first, for SHORT lowest first
  if (side === 'LONG') {
    rows.sort((a, b) => b.sortPrice - a.sortPrice)
  } else {
    rows.sort((a, b) => a.sortPrice - b.sortPrice)
  }

  return rows
}

const PositionCard = memo(function PositionCard({
  position,
  orders,
  language,
  onSymbolClick,
}: {
  position: Position
  orders: OpenOrder[]
  language: Language
  onSymbolClick?: (symbol: string) => void
}) {
  const symbol = String(position.symbol || '').toUpperCase()
  const side = normalizeSide(position.side)
  const entryPrice = position.entry_price || 0
  const markPrice = position.mark_price || 0
  const entryQty = position.entry_quantity || position.quantity || 0
  const nowQty = position.quantity || 0
  const rt = position.protection_runtime
  const currentPnlPct = Number(
    rt?.current_pnl_pct ?? position.unrealized_pnl_pct ?? 0
  )
  const peakPnlPct = Number(rt?.drawdown_peak_pnl_pct ?? currentPnlPct)
  const currentDrawdownPct = Number(rt?.current_drawdown_pct ?? 0)
  const atrAtEntry = Number(rt?.atr_at_entry ?? 0)
  const currentATR = Number(rt?.current_atr ?? 0)
  const atrTimeframe = String(rt?.atr_timeframe ?? '')
  const atrEntryPct =
    entryPrice > 0 && atrAtEntry > 0 ? (atrAtEntry / entryPrice) * 100 : 0
  const atrDriftPct =
    atrAtEntry > 0 && currentATR > 0
      ? ((currentATR - atrAtEntry) / atrAtEntry) * 100
      : 0

  // Real overall result of the position: accumulated realized (from partial closes)
  // + current unrealized - total fees. Falls back to raw unrealized when net_pnl absent.
  const entryTimeMs = position.entry_time
  const realizedPnl = Number(position.realized_pnl ?? 0)
  const totalFee = Number(position.fee ?? 0)
  const netPnl = Number(position.net_pnl ?? position.unrealized_pnl ?? 0)
  const isPartiallyClosed =
    entryQty > 0 && nowQty > 0 && nowQty < entryQty - 1e-9

  const filteredOrders = useMemo(
    () =>
      orders.filter((o) => {
        const s = normalizeSide(o.position_side)
        return !s || s === side
      }),
    [orders, side]
  )

  const rows = useMemo(
    () => buildProtectionRows(position, filteredOrders, language),
    [position, filteredOrders, language]
  )

  const pnlColor = currentPnlPct >= 0 ? 'text-nofx-green' : 'text-nofx-red'
  const netPnlColor = netPnl >= 0 ? 'text-nofx-green' : 'text-nofx-red'
  const sideCls =
    side === 'LONG'
      ? 'bg-nofx-green/15 text-nofx-green border-nofx-green/30'
      : 'bg-nofx-red/15 text-nofx-red border-nofx-red/30'

  return (
    <div className="rounded-lg border border-white/10 bg-black/20 p-4 space-y-3">
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
        <div className="flex items-baseline gap-2">
          <span className={`text-base font-bold font-mono ${netPnlColor}`}>
            {formatUsd(netPnl)}
          </span>
          <span className={`text-base font-bold font-mono ${pnlColor}`}>
            {formatPct(currentPnlPct)}
          </span>
        </div>
      </div>

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
          Full{' '}
          <span className="font-mono text-nofx-text-main">
            {formatUsdx(entryQty, entryPrice)}
          </span>
          <span className="text-nofx-text-muted mx-0.5">/</span>
          Now{' '}
          <span className="font-mono text-nofx-text-main">
            {formatUsdx(nowQty, markPrice)}
          </span>
        </span>
        <span>
          Peak{' '}
          <span className="font-mono text-nofx-text-main">
            {formatPct(peakPnlPct)}
          </span>
          {currentDrawdownPct > 0 && (
            <>
              <span className="text-nofx-text-muted mx-0.5">↓</span>
              <span className="font-mono text-nofx-red">
                {currentDrawdownPct.toFixed(1)}%
              </span>
            </>
          )}
        </span>
        {entryTimeMs && entryTimeMs > 0 && (
          <span>
            {language === 'zh' ? '持仓' : 'Held'}{' '}
            <span className="font-mono text-nofx-text-main">
              {formatHoldTime(entryTimeMs)}
            </span>
          </span>
        )}
        {atrAtEntry > 0 && (
          <span>
            ATR{atrTimeframe ? ` ${atrTimeframe}` : ''}{' '}
            <span className="text-nofx-text-muted">
              {language === 'zh' ? '开仓' : 'entry'}
            </span>{' '}
            <span className="font-mono text-nofx-text-main">
              {formatPrice(atrAtEntry)}
            </span>
            {atrEntryPct > 0 && (
              <span className="text-nofx-text-muted">
                {' '}
                ({atrEntryPct.toFixed(2)}%)
              </span>
            )}
            {currentATR > 0 && (
              <>
                <span className="text-nofx-text-muted mx-0.5">→</span>
                <span className="text-nofx-text-muted">
                  {language === 'zh' ? '当前' : 'now'}
                </span>{' '}
                <span className="font-mono text-nofx-text-main">
                  {formatPrice(currentATR)}
                </span>
                {Math.abs(atrDriftPct) >= 1 && (
                  <span
                    className={`font-mono ${
                      atrDriftPct > 0 ? 'text-nofx-red' : 'text-nofx-green'
                    }`}
                  >
                    {' '}
                    {atrDriftPct > 0 ? '+' : ''}
                    {atrDriftPct.toFixed(0)}%
                  </span>
                )}
              </>
            )}
          </span>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        <span className="text-nofx-text-muted">
          {language === 'zh' ? '净盈亏' : 'Net PnL'}{' '}
          <span className={`font-mono font-semibold ${netPnlColor}`}>
            {formatUsd(netPnl)}
          </span>
          <span className="text-nofx-text-muted ml-1">
            ({language === 'zh' ? '含手续费' : 'incl. fees'})
          </span>
        </span>
        {isPartiallyClosed && (
          <span className="text-nofx-text-muted">
            {language === 'zh' ? '已实现' : 'Realized'}{' '}
            <span
              className={`font-mono ${
                realizedPnl >= 0 ? 'text-nofx-green' : 'text-nofx-red'
              }`}
            >
              {formatUsd(realizedPnl)}
            </span>
            <span className="text-nofx-text-muted mx-0.5">+</span>
            {language === 'zh' ? '浮动' : 'Unreal.'}{' '}
            <span
              className={`font-mono ${
                position.unrealized_pnl >= 0
                  ? 'text-nofx-green'
                  : 'text-nofx-red'
              }`}
            >
              {formatUsd(position.unrealized_pnl)}
            </span>
          </span>
        )}
        {totalFee > 0 && (
          <span className="text-nofx-text-muted">
            {language === 'zh' ? '手续费' : 'Fees'}{' '}
            <span className="font-mono text-nofx-red">
              -${totalFee.toFixed(2)}
            </span>
          </span>
        )}
      </div>

      {rows.length > 0 ? (
        <div className="overflow-x-auto pb-1">
          <div className="flex items-stretch gap-1.5 min-w-min">
            {rows.map((row, ri) => {
              if (row.isCurrentPrice) {
                return (
                  <div
                    key={`price-line-${ri}`}
                    className="flex flex-col items-center justify-center px-2 rounded border border-cyan-500/40 bg-cyan-500/5 shrink-0"
                  >
                    <span className="text-[9px] text-cyan-300/70 uppercase tracking-wider">
                      {language === 'zh' ? '现价' : 'Now'}
                    </span>
                    <span className="text-[11px] font-mono font-bold text-cyan-300 whitespace-nowrap">
                      {formatPrice(row.price)}
                    </span>
                    <span className="text-[10px] font-mono text-cyan-300/80 whitespace-nowrap">
                      {formatPct(row.deltaPct)}
                      {row.atrMult !== 0
                        ? ` / ${row.atrMult >= 0 ? '+' : ''}${row.atrMult.toFixed(1)}×`
                        : ''}
                    </span>
                  </div>
                )
              }

              const deltaColor =
                row.deltaPct > 0
                  ? 'text-nofx-green'
                  : row.deltaPct < 0
                    ? 'text-nofx-red'
                    : 'text-nofx-text-muted'
              const zoneCls = row.zone.startsWith('DD')
                ? 'text-purple-300 border-purple-400/30'
                : row.zone.startsWith('BE')
                  ? 'text-amber-300 border-amber-400/30'
                  : 'text-blue-300 border-blue-400/30'
              const statusDot = row.statusCls.includes('emerald')
                ? 'bg-emerald-400'
                : row.statusCls.includes('amber')
                  ? 'bg-amber-400'
                  : row.statusCls.includes('red')
                    ? 'bg-red-400'
                    : 'bg-white/30'

              return (
                <div
                  key={`row-${ri}`}
                  className={`flex flex-col px-2 py-1 rounded border bg-black/20 hover:bg-white/5 shrink-0 min-w-[78px] ${zoneCls}`}
                  title={row.detail || ''}
                >
                  <div className="flex items-center justify-between gap-1">
                    <span
                      className={`text-[10px] font-bold ${zoneCls.split(' ')[0]}`}
                    >
                      {row.zone}
                    </span>
                    <span
                      className={`w-1.5 h-1.5 rounded-full ${statusDot}`}
                      title={row.status}
                    />
                  </div>
                  <span className="text-[11px] font-mono font-semibold text-nofx-text-main whitespace-nowrap">
                    {row.price > 0 ? formatPrice(row.price) : '—'}
                  </span>
                  <span
                    className={`text-[10px] font-mono whitespace-nowrap ${deltaColor}`}
                  >
                    {row.price > 0 ? formatPct(row.deltaPct) : '—'}
                    {row.price > 0 && row.atrMult !== 0
                      ? ` / ${row.atrMult >= 0 ? '+' : ''}${row.atrMult.toFixed(1)}×`
                      : ''}
                  </span>
                  <span className="text-[10px] font-mono text-nofx-text-muted whitespace-nowrap">
                    {row.ratioPct > 0 ? (
                      <>
                        {row.ratioPct.toFixed(0)}%
                        {row.usdValue > 0
                          ? ` ${formatUsd(row.usdValue).replace('+', '')}`
                          : ''}
                      </>
                    ) : (
                      '—'
                    )}
                  </span>
                </div>
              )
            })}
          </div>
        </div>
      ) : (
        <div className="text-xs text-nofx-text-muted border border-white/10 rounded px-3 py-2">
          {language === 'zh' ? '无保护委托' : 'No protection orders'}
        </div>
      )}
    </div>
  )
})

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

  // Stable symbol key string to avoid re-fetching on every positions reference change
  const symbolKeyStr = useMemo(() => {
    const keys = new Set<string>()
    for (const pos of positions || [])
      keys.add(String(pos.symbol || '').toUpperCase())
    return [...keys].sort().join(',')
  }, [positions])

  useEffect(() => {
    let cancelled = false
    async function load() {
      if (!traderId || !symbolKeyStr) {
        setOrdersBySymbol({})
        return
      }
      setLoading(true)
      const symbols = symbolKeyStr.split(',')
      try {
        const entries = await Promise.all(
          symbols.map(async (symbol) => {
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
  }, [traderId, symbolKeyStr])

  if (!positions || positions.length === 0) {
    return (
      <div className="nofx-glass p-4 relative overflow-hidden">
        <h2 className="text-sm font-bold text-nofx-text-main uppercase tracking-wide flex items-center gap-2 mb-4">
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
    <div className="nofx-glass p-4 relative overflow-hidden">
      <h2 className="text-sm font-bold text-nofx-text-main uppercase tracking-wide flex items-center gap-2 mb-3">
        <span className="text-purple-400">◈</span>
        {language === 'zh' ? '持仓保护' : 'Position Protection'}
        {loading && (
          <span className="text-[10px] text-nofx-text-muted ml-2">⟳</span>
        )}
      </h2>

      <div className="space-y-4">
        {positions.map((position, index) => (
          <PositionCard
            key={`${String(position.symbol || '').toUpperCase()}-${normalizeSide(position.side)}-${index}`}
            position={position}
            orders={
              ordersBySymbol[String(position.symbol || '').toUpperCase()] || []
            }
            language={language}
            onSymbolClick={onSymbolClick}
          />
        ))}
      </div>
    </div>
  )
}
