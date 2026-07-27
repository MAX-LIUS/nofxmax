// protectionPlan.ts — single source of truth for the exit taxonomy and for
// turning a pct-based ProtectionSnapshot into concrete, price-anchored plan
// items. Mirrors the backend taxonomy in store/attribution.go (category +
// mechanism) so the UI labels every open/close/protection consistently and can
// map an actual close event back to the plan item that fired.
import type {
  ProtectionSnapshot,
  ProtectionSnapshotDrawdown,
} from '../../types'

export type CloseCategory =
  | 'ai'
  | 'protection'
  | 'manual'
  | 'exchange'
  | 'system'

export interface CategoryMeta {
  zh: string
  en: string
  color: string
  bg: string
  border: string
}

// Coarse origin buckets. Colors match the dashboard palette used elsewhere.
export const CATEGORY_META: Record<CloseCategory, CategoryMeta> = {
  ai: {
    zh: 'AI 主动',
    en: 'AI',
    color: '#60A5FA',
    bg: 'rgba(96,165,250,0.12)',
    border: 'rgba(96,165,250,0.3)',
  },
  protection: {
    zh: '保护单',
    en: 'Protection',
    color: '#C084FC',
    bg: 'rgba(168,85,247,0.12)',
    border: 'rgba(168,85,247,0.3)',
  },
  manual: {
    zh: '手动',
    en: 'Manual',
    color: '#0ECB81',
    bg: 'rgba(14,203,129,0.12)',
    border: 'rgba(14,203,129,0.3)',
  },
  exchange: {
    zh: '交易所',
    en: 'Exchange',
    color: '#F0B90B',
    bg: 'rgba(240,185,11,0.12)',
    border: 'rgba(240,185,11,0.3)',
  },
  system: {
    zh: '系统',
    en: 'System',
    color: '#848E9C',
    bg: 'rgba(132,142,156,0.12)',
    border: 'rgba(132,142,156,0.25)',
  },
}

// Fine-grained mechanism -> (label, owning category). Keys are the stable enums
// emitted by ClassifyClose in store/attribution.go.
export const MECHANISM_META: Record<
  string,
  { zh: string; en: string; category: CloseCategory }
> = {
  ladder_tp: { zh: '阶梯止盈', en: 'Ladder TP', category: 'protection' },
  ladder_sl: { zh: '阶梯止损', en: 'Ladder SL', category: 'protection' },
  full_tp: { zh: '全量止盈', en: 'Full TP', category: 'protection' },
  full_sl: { zh: '全量止损', en: 'Full SL', category: 'protection' },
  fallback_maxloss_sl: {
    zh: '兜底止损',
    en: 'Fallback SL',
    category: 'protection',
  },
  native_trailing: { zh: '移动止损', en: 'Trailing', category: 'protection' },
  managed_drawdown: { zh: '回撤止盈', en: 'Drawdown', category: 'protection' },
  break_even_stop: { zh: '保本止损', en: 'Break-even', category: 'protection' },
  time_stop: { zh: '时间止损', en: 'Time stop', category: 'protection' },
  max_hold: { zh: '超时平仓', en: 'Max hold', category: 'protection' },
  trailing_take_profit: {
    zh: '移动止盈',
    en: 'Trailing TP',
    category: 'protection',
  },
  breadth_breaker: {
    zh: '广度熔断',
    en: 'Breadth breaker',
    category: 'protection',
  },
  emergency_protection_close: {
    zh: '紧急平仓',
    en: 'Emergency',
    category: 'protection',
  },
  trend_reversal_flip: { zh: '趋势反转', en: 'Reversal flip', category: 'ai' },
  ai_close: { zh: 'AI 平仓', en: 'AI close', category: 'ai' },
  manual_close: { zh: '手动平仓', en: 'Manual close', category: 'manual' },
  liquidation: { zh: '强平/ADL', en: 'Liquidation', category: 'exchange' },
  sync_external: {
    zh: '交易所平仓(未归因)',
    en: 'Exchange (unattributed)',
    category: 'exchange',
  },
  legacy_unknown: {
    zh: '历史未记录',
    en: 'Legacy unknown',
    category: 'system',
  },
  unknown_close: { zh: '未知', en: 'Unknown', category: 'system' },
}

// classifyMechanism mirrors store/attribution.go ClassifyClose: tolerant
// substring matching so stage-suffixed reasons (managed_drawdown_runner_exit)
// still resolve. Returns the canonical mechanism key.
export function classifyMechanism(rawReason?: string): string {
  const r = String(rawReason || '')
    .toLowerCase()
    .trim()
  if (r === '') return 'unknown_close'
  if (r.startsWith('manual_close') || r === 'manual') return 'manual_close'
  if (r.includes('liquidat') || r === 'adl') return 'liquidation'
  if (r.includes('breadth') || r.includes('giveback_guard'))
    return 'breadth_breaker'
  if (r.includes('managed_drawdown')) return 'managed_drawdown'
  if (r.includes('trailing_take_profit')) return 'trailing_take_profit'
  if (r.includes('native_trailing') || r === 'trailing')
    return 'native_trailing'
  if (r.includes('break_even')) return 'break_even_stop'
  if (r.includes('ladder_tp')) return 'ladder_tp'
  if (r.includes('ladder_sl')) return 'ladder_sl'
  if (r.includes('fallback_maxloss')) return 'fallback_maxloss_sl'
  if (r.includes('full_tp')) return 'full_tp'
  if (r.includes('full_sl')) return 'full_sl'
  if (r.includes('time_stop')) return 'time_stop'
  if (r.includes('max_hold')) return 'max_hold'
  if (r.includes('emergency')) return 'emergency_protection_close'
  if (r.includes('trend_reversal')) return 'trend_reversal_flip'
  if (r.startsWith('ai_close')) return 'ai_close'
  if (r.includes('sync_absent') || r.includes('sync_external'))
    return 'sync_external'
  if (r === 'close_long' || r === 'close_short') return 'sync_external'
  if (r.includes('legacy_unknown')) return 'legacy_unknown'
  return 'unknown_close'
}

export function mechanismLabel(lang: string, mechanism: string): string {
  const m = MECHANISM_META[mechanism]
  if (!m) return mechanism
  return lang === 'zh' ? m.zh : m.en
}

export function categoryOf(mechanism: string): CloseCategory {
  return MECHANISM_META[mechanism]?.category || 'system'
}

export function categoryMeta(category: CloseCategory): CategoryMeta {
  return CATEGORY_META[category] || CATEGORY_META.system
}

// PlanItem is one concrete, price-anchored protection level derived from the
// entry snapshot. `mechanism` links it to the exit taxonomy so an actual close
// event can be matched back to the plan item that fired.
export interface PlanItem {
  mechanism: string // taxonomy key (ladder_tp, full_sl, break_even_stop, ...)
  kind: 'tp' | 'sl' | 'be' | 'drawdown' | 'trailing' | 'structural'
  label: string // short human label (localized by caller via mechanismLabel)
  triggerPct: number // signed % move from entry that arms/fires it (+ favorable)
  // triggerPrice is where the level starts to act. For static levels (TP/SL/BE/
  // structural) that IS the fill price; for a drawdown tier it is only the
  // ACTIVATION price — reaching it starts peak tracking and fills nothing.
  triggerPrice?: number // absolute price when computable from entry+pct
  // executionPrice is where the level actually fills: peak × (1 ∓ callback) for a
  // drawdown tier, == triggerPrice for static levels. This is the ordering key.
  executionPrice?: number
  executionPct?: number
  closeRatioPct?: number // portion of position this level closes
  note?: string // extra context (e.g. "min +3% first", "offset +0.3%")
}

// signedToPrice converts a pct move from entry into an absolute price for the
// given side. favorable=true means the move is in the position's profit
// direction (TP/BE-offset); favorable=false means adverse (SL/drawdown floor).
function signedToPrice(
  entry: number,
  pct: number,
  isLong: boolean,
  favorable: boolean
): number | undefined {
  if (!entry || entry <= 0 || !isFinite(pct)) return undefined
  const dir = favorable === isLong ? 1 : -1 // long+favorable => up; short+favorable => down
  return entry * (1 + (dir * Math.abs(pct)) / 100)
}

// buildProtectionPlan turns the entry-time snapshot into an ordered list of
// concrete protection levels. Ladder TP/SL tiers, break-even arm point, and
// drawdown floors are all resolved to absolute prices from the entry price so
// the detail view shows exactly where each protection would fire.
export function buildProtectionPlan(
  snapshot: ProtectionSnapshot | undefined,
  entryPrice: number,
  isLong: boolean
): PlanItem[] {
  if (!snapshot) return []
  const items: PlanItem[] = []

  // Ladder tiers: each rule may carry a TP leg and/or an SL leg.
  const ladder = snapshot.ladder_tp_sl
  if (ladder?.enabled && Array.isArray(ladder.rules)) {
    ladder.rules.forEach((rule, idx) => {
      if (
        typeof rule.take_profit_pct === 'number' &&
        ladder.take_profit_enabled
      ) {
        items.push({
          mechanism: 'ladder_tp',
          kind: 'tp',
          label: `TP${idx + 1}`,
          triggerPct: rule.take_profit_pct,
          triggerPrice:
            rule.take_profit_price ??
            signedToPrice(entryPrice, rule.take_profit_pct, isLong, true),
          closeRatioPct: rule.take_profit_close_ratio_pct,
        })
      }
      if (typeof rule.stop_loss_pct === 'number' && ladder.stop_loss_enabled) {
        items.push({
          mechanism: 'ladder_sl',
          kind: 'sl',
          label: `SL${idx + 1}`,
          triggerPct: -Math.abs(rule.stop_loss_pct),
          triggerPrice:
            rule.stop_loss_price ??
            signedToPrice(entryPrice, rule.stop_loss_pct, isLong, false),
          closeRatioPct: rule.stop_loss_close_ratio_pct,
        })
      }
    })
  }

  // Full TP/SL (non-laddered) — values are stored as multipliers/pcts on the
  // value source; surface them as plan rows without a computed price when the
  // pct is not directly available.
  const full = snapshot.full_tp_sl
  if (full?.enabled) {
    if (full.take_profit && typeof full.take_profit.value === 'number') {
      items.push({
        mechanism: 'full_tp',
        kind: 'tp',
        label: 'Full TP',
        triggerPct: full.take_profit.value,
        triggerPrice: signedToPrice(
          entryPrice,
          full.take_profit.value,
          isLong,
          true
        ),
      })
    }
    if (full.stop_loss && typeof full.stop_loss.value === 'number') {
      items.push({
        mechanism: 'full_sl',
        kind: 'sl',
        label: 'Full SL',
        triggerPct: -Math.abs(full.stop_loss.value),
        triggerPrice: signedToPrice(
          entryPrice,
          full.stop_loss.value,
          isLong,
          false
        ),
      })
    }
  }

  // Break-even: arms at trigger_value% profit, then parks the stop at
  // +offset_pct (a small locked profit) above/below entry.
  const be = snapshot.break_even
  if (be?.enabled) {
    const armPct = be.trigger_value
    const offsetPrice = signedToPrice(entryPrice, be.offset_pct, isLong, true)
    items.push({
      mechanism: 'break_even_stop',
      kind: 'be',
      label: 'Break-even',
      triggerPct: armPct,
      triggerPrice: offsetPrice,
      note:
        typeof be.offset_pct === 'number'
          ? `arms +${armPct}% → stop @ +${be.offset_pct}%`
          : `arms +${armPct}%`,
    })
  }

  // Drawdown floors: once profit reaches min_profit_pct, a giveback beyond
  // max_drawdown_pct from the peak closes close_ratio_pct of the position.
  const dd: ProtectionSnapshotDrawdown[] = snapshot.drawdown || []
  dd.forEach((stage, idx) => {
    items.push({
      mechanism: 'managed_drawdown',
      kind: 'drawdown',
      label: dd.length > 1 ? `Drawdown ${idx + 1}` : 'Drawdown',
      triggerPct: stage.min_profit_pct,
      closeRatioPct: stage.close_ratio_pct,
      note: `min +${stage.min_profit_pct}% · giveback ${stage.max_drawdown_pct}%${
        stage.runner_mode_active ? ' · runner' : ''
      }`,
    })
  })

  return items
}
