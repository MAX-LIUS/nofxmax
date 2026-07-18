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

// ProtectionFamily groups every close-triggering mechanism into a color family so
// the panel reads as one map: where (and by what) the position will close.
//   liquidation — exchange hard floor (never should be hit)
//   sl          — stop-loss orders (full/ladder/fallback)
//   be          — break-even stop
//   tp          — take-profit orders (full/ladder)
//   dynamic     — trailing / managed-drawdown / runner (price moves with peak)
//   structural  — range-anchored structural stop (boundary + backstop)
type ProtectionFamily =
  | 'liquidation'
  | 'sl'
  | 'be'
  | 'tp'
  | 'dynamic'
  | 'structural'

type ProtectionRow = {
  zone: string
  family: ProtectionFamily
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

// FAMILY_CLS maps a family to its Tailwind text+border classes. Kept in one place
// so the legend and the rows never drift apart.
const FAMILY_CLS: Record<ProtectionFamily, string> = {
  liquidation: 'text-red-400 border-red-500/50',
  sl: 'text-orange-300 border-orange-400/30',
  be: 'text-amber-300 border-amber-400/30',
  tp: 'text-nofx-green border-emerald-400/30',
  dynamic: 'text-purple-300 border-purple-400/30',
  structural: 'text-blue-300 border-blue-400/40',
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
  is_armed?: boolean
  is_activated?: boolean
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
    // callback_rate arrives as a RATIO (e.g. 0.024) for OKX but as a PERCENT
    // (e.g. 2.4) for Binance/Bitget (backend multiplies by 100 for the exchange
    // API). Every consumer below expects a ratio, so normalize here: a value > 1
    // can only be a percent (a >100% trailing callback is nonsensical), so /100.
    const rawCallback = tier.callback_rate || 0
    const callbackRate = rawCallback > 1 ? rawCallback / 100 : rawCallback
    // Activation/armed are distinct: is_activated means the peak reached the
    // trigger so the trailing stop is live and tracking the peak; is_armed means
    // an order rests on the exchange but price has not yet reached activation.
    // Fall back to is_satisfied for older backends that only sent that field.
    const isActivated = tier.is_activated ?? tier.is_satisfied ?? false
    const isArmed = tier.is_armed ?? false

    // Trigger price = peak * (1 - callback) ONLY once the stop is genuinely
    // activated (tracking the peak). Before activation the meaningful number is
    // the activation price, not a peak-derived level (which would otherwise
    // render a misleading below-entry "trigger").
    let triggerPrice = 0
    if (isActivated && peakPnlPct > 0 && entryPrice > 0 && callbackRate > 0) {
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
    } else if (isActivated) {
      status = language === 'zh' ? '已激活' : 'Active'
      statusCls = 'text-emerald-300'
    } else if (isArmed) {
      status = language === 'zh' ? '已布单' : 'Armed'
      statusCls = 'text-amber-300'
    } else {
      status = language === 'zh' ? '待满足' : 'Waiting'
      statusCls = 'text-nofx-text-muted'
    }

    // Detail: once activated show the live peak + callback distance; otherwise
    // show the configured activation/callback distances (callback % == price
    // retracement % under the unified trailing semantics).
    let detail: string
    if (isActivated && peakPnlPct > 0) {
      detail = `peak${formatPct(peakPnlPct, 1)} cb${(callbackRate * 100).toFixed(1)}%`
    } else {
      detail = `min${tier.min_profit_pct.toFixed(1)}% dd${tier.max_drawdown_pct.toFixed(1)}%`
    }

    rows.push({
      zone,
      family: 'dynamic',
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

  // Runner stop: an explicit stop parked under a profit runner (distinct from the
  // trailing tiers above). Shown as its own dynamic-family row when present.
  if (rt?.runner_mode_active && Number(rt?.runner_stop_price ?? 0) > 0) {
    const rsp = Number(rt.runner_stop_price)
    const rawDelta =
      entryPrice > 0 ? ((rsp - entryPrice) / entryPrice) * 100 : 0
    const deltaPct = rawDelta * dirMul
    rows.push({
      zone: 'Runner',
      family: 'dynamic',
      price: rsp,
      sortPrice: rsp,
      deltaPct,
      atrMult: toAtrMult(deltaPct),
      ratioPct: 100 - Number(rt.runner_keep_pct ?? 0),
      usdValue: 0,
      status: language === 'zh' ? '跟随中' : 'Runner',
      statusCls: 'text-purple-300',
      detail:
        typeof rt.runner_keep_pct === 'number'
          ? `keep ${rt.runner_keep_pct}%`
          : undefined,
    })
  }

  // Build rows from exchange orders (BE + Ladder + fallback, skip trailing since
  // DD comes from tiers). Family is inferred from zone + profit direction.
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

    const id = String(order.client_order_id || '').toLowerCase()
    const isTP =
      type.includes('TAKE_PROFIT') || type.includes('TP') || id.includes('_tp')
    const isFallback = id.includes('fallback') || id.includes('maxloss')

    let family: ProtectionFamily
    if (zone === 'BE') {
      beIndex++
      zone = `BE-${beIndex}`
      family = 'be'
    } else if (isFallback) {
      zone = 'Fallback'
      family = 'sl'
    } else if (isTP) {
      family = 'tp'
    } else {
      // A profit-side stop that isn't tagged BE is still a stop (ladder/full SL).
      family = 'sl'
    }

    const status = language === 'zh' ? '委托中' : 'Live'
    const statusCls = 'text-emerald-300'

    rows.push({
      zone,
      family,
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

  // Structural stop-loss (range-anchored) — auto-shown when enabled. Two levels:
  // the frozen boundary (Phase 2, close-confirm) and the wide backstop (Phase 1).
  const structEnabled = Boolean(rt?.structural_sl_enabled)
  if (structEnabled) {
    const boundary = Number(rt?.structural_boundary_price ?? 0)
    if (boundary > 0) {
      const rawDelta =
        entryPrice > 0 ? ((boundary - entryPrice) / entryPrice) * 100 : 0
      rows.push({
        zone: 'Struct',
        family: 'structural',
        price: boundary,
        sortPrice: boundary,
        deltaPct: rawDelta * dirMul,
        atrMult: toAtrMult(rawDelta * dirMul),
        ratioPct: 100,
        usdValue: 0,
        status: rt?.structural_close_confirm
          ? language === 'zh'
            ? '收盘确认'
            : 'Close-confirm'
          : language === 'zh'
            ? '已布单'
            : 'Armed',
        statusCls: 'text-blue-300',
        detail:
          language === 'zh'
            ? '结构位:K线收盘跌破边界即平'
            : 'Structural: closes on bar close beyond boundary',
      })
    }
    const backstop = Number(rt?.structural_backstop_price ?? 0)
    if (backstop > 0) {
      const rawDelta =
        entryPrice > 0 ? ((backstop - entryPrice) / entryPrice) * 100 : 0
      rows.push({
        zone: 'Backstop',
        family: 'structural',
        price: backstop,
        sortPrice: backstop,
        deltaPct: rawDelta * dirMul,
        atrMult: toAtrMult(rawDelta * dirMul),
        ratioPct: 100,
        usdValue: 0,
        status: language === 'zh' ? '安全网' : 'Backstop',
        statusCls: 'text-blue-300',
        detail:
          language === 'zh'
            ? '安全网止损(挂交易所,防宕机/跳空)'
            : 'Resting safety-net stop (downtime/gap cover)',
      })
    }
  }

  // Liquidation — the exchange hard floor. Always last-resort; render if present.
  const liqPrice = Number(position.liquidation_price ?? 0)
  if (liqPrice > 0 && entryPrice > 0) {
    const rawDelta = ((liqPrice - entryPrice) / entryPrice) * 100
    rows.push({
      zone: 'Liq',
      family: 'liquidation',
      price: liqPrice,
      sortPrice: liqPrice,
      deltaPct: rawDelta * dirMul,
      // Liquidation distance in ATR multiples is meaningless: for a cross-margin
      // or small position the exchange liq price sits enormously far away, yielding
      // absurd figures like -135×. Suppress the ×ATR for liq (0 hides it at both
      // render sites) and keep only the % distance, which is the meaningful metric.
      atrMult: 0,
      ratioPct: 100,
      usdValue: 0,
      status: language === 'zh' ? '强平线' : 'Liquidation',
      statusCls: 'text-red-400',
      detail:
        language === 'zh'
          ? '交易所强制平仓价(最后底线)'
          : 'Exchange forced-liquidation price (hard floor)',
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
      family: 'dynamic',
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
  // Excursion (MFE/MAE) tracked server-side and persisted: favorable peak + adverse
  // trough profit%, each in open-time ATR multiples. Shown so the running envelope
  // (and the worst adverse dip a position weathered) is visible, not just current PnL.
  const excPeakPct = Number(position.peak_pnl_pct ?? 0)
  const excTroughPct = Number(position.trough_pnl_pct ?? 0)
  const excPeakAtr = Number(position.peak_atr_mult ?? 0)
  const excTroughAtr = Number(position.trough_atr_mult ?? 0)
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
        {(excPeakPct !== 0 || excTroughPct !== 0) && (
          <span>
            {language === 'zh' ? '峰/谷' : 'MFE/MAE'}{' '}
            <span className="font-mono text-nofx-green">
              {formatPct(excPeakPct)}
            </span>
            {excPeakAtr !== 0 && (
              <span className="font-mono text-nofx-text-muted">
                {' '}
                {excPeakAtr > 0 ? '+' : ''}
                {excPeakAtr.toFixed(1)}×
              </span>
            )}
            <span className="text-nofx-text-muted mx-0.5">/</span>
            <span className="font-mono text-nofx-red">
              {formatPct(excTroughPct)}
            </span>
            {excTroughAtr !== 0 && (
              <span className="font-mono text-nofx-text-muted">
                {' '}
                {excTroughAtr.toFixed(1)}×
              </span>
            )}
          </span>
        )}
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
              const zoneCls = FAMILY_CLS[row.family] || FAMILY_CLS.dynamic
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

      <ConditionTriggers rt={rt} language={language} />
    </div>
  )
})

// ConditionTriggers renders the close mechanisms that have NO fixed price — they
// fire on elapsed time / loss conditions, not a level. Kept visually separate
// (gray) from the price ladder so the user does not read them as price lines.
function ConditionTriggers({
  rt,
  language,
}: {
  rt: Position['protection_runtime']
  language: Language
}) {
  const chips: { label: string; title: string }[] = []
  const tsh = Number(rt?.time_stop_hours ?? 0)
  const tsl = Number(rt?.time_stop_loss_pct ?? 0)
  if (tsh > 0) {
    chips.push({
      label:
        language === 'zh'
          ? `时间止损 ${tsh}h${tsl ? ` / 亏损<${tsl}%` : ''}`
          : `Time-stop ${tsh}h${tsl ? ` / loss<${tsl}%` : ''}`,
      title:
        language === 'zh'
          ? `持仓超过 ${tsh} 小时且仍亏损差于 ${tsl}% 时强制平仓`
          : `Force-close when held > ${tsh}h and still in loss worse than ${tsl}%`,
    })
  }
  const mhh = Number(rt?.max_hold_hours ?? 0)
  const mhe = Number(rt?.max_hold_profit_exempt_pct ?? 0)
  if (mhh > 0) {
    chips.push({
      label:
        language === 'zh'
          ? `超时平仓 ${mhh}h${mhe ? ` / 盈利≥${mhe}%豁免` : ''}`
          : `Max-hold ${mhh}h${mhe ? ` / exempt≥${mhe}%` : ''}`,
      title:
        language === 'zh'
          ? `持仓超过 ${mhh} 小时强制平仓,除非盈利≥${mhe}%(盈利runner豁免)`
          : `Force-close after ${mhh}h unless profit ≥ ${mhe}% (runner exempt)`,
    })
  }
  if (chips.length === 0) return null
  return (
    <div className="flex flex-wrap items-center gap-1.5 pt-1">
      <span className="text-[10px] uppercase tracking-wider text-nofx-text-muted">
        {language === 'zh'
          ? '条件触发(无固定价位)'
          : 'Condition triggers (no price)'}
      </span>
      {chips.map((c, i) => (
        <span
          key={i}
          className="rounded border border-white/15 bg-white/5 px-1.5 py-0.5 text-[10px] text-nofx-text-muted"
          title={c.title}
        >
          {c.label}
        </span>
      ))}
    </div>
  )
}

// ProtectionLegend explains the color families once at the top so every price
// row below is self-describing.
function ProtectionLegend({ language }: { language: Language }) {
  const items: { family: ProtectionFamily; zh: string; en: string }[] = [
    { family: 'tp', zh: '止盈', en: 'TP' },
    { family: 'be', zh: '保本', en: 'BE' },
    { family: 'sl', zh: '止损/兜底', en: 'SL/Fallback' },
    { family: 'dynamic', zh: '移动/回撤', en: 'Trailing/DD' },
    { family: 'structural', zh: '结构位', en: 'Structural' },
    { family: 'liquidation', zh: '强平', en: 'Liq' },
  ]
  return (
    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 mb-3 text-[10px] text-nofx-text-muted">
      {items.map((it) => (
        <span key={it.family} className="inline-flex items-center gap-1">
          <span
            className={`inline-block w-2 h-2 rounded-sm border ${FAMILY_CLS[it.family]}`}
          />
          {language === 'zh' ? it.zh : it.en}
        </span>
      ))}
    </div>
  )
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

      <ProtectionLegend language={language} />

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
